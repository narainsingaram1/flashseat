package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/inventory"
	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/flashseat/flashseat/apps/api/internal/telemetry"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// pickAttempts bounds server-side retries when a chosen block is claimed by a
// concurrent buyer between reading the bitmap and running the claim script.
const pickAttempts = 4

// naivePrefix marks hold ids that belong to the unsafe comparison path.
const naivePrefix = "naive-"

type holdInput struct {
	EventID  string   `json:"eventId"`
	SeatIDs  []string `json:"seatIds"`
	Quantity int      `json:"quantity"`
	Tier     int      `json:"tier"`
}

type holdOutcome struct {
	key    string
	status int
	detail string
}

// slowHold is the latency above which a hold logs its per-phase timings.
const slowHold = 200 * time.Millisecond

// phases records how long each step of a request took.
type phases struct {
	last  time.Time
	marks []any
}

func (p *phases) mark(name string) {
	now := time.Now()
	p.marks = append(p.marks, name, now.Sub(p.last).Round(10*time.Microsecond).String())
	p.last = now
}

func (a *API) createHold(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var in holdInput
	ph := &phases{last: start}
	out := a.doCreateHold(w, r, &in, ph)
	took := time.Since(start)
	a.outcomes.WithLabelValues(out.key).Inc()
	a.tel.Record(in.EventID, out.key, "hold", took, telemetry.Sample{Actor: actor(r), Kind: "hold", Status: out.status, Detail: out.detail})
	if took > slowHold {
		slog.Warn("slow hold", append([]any{"took", took.Round(time.Millisecond).String(), "outcome", out.key}, ph.marks...)...)
	}
}

func (a *API) doCreateHold(w http.ResponseWriter, r *http.Request, in *holdInput, ph *phases) holdOutcome {
	fail := func(status int, code, msg string) holdOutcome {
		problem(w, status, code, msg)
		return holdOutcome{key: fmt.Sprintf("hold_%d_%s", status, code), status: status, detail: msg}
	}
	user, ok := requireUser(w, r)
	if !ok {
		return holdOutcome{key: "hold_401", status: 401}
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return holdOutcome{key: "hold_422", status: 422}
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(in); err != nil || in.EventID == "" {
		return fail(422, "invalid_json", "invalid request body")
	}
	ph.mark("decode")
	m, v, ok := a.eventAndVenue(w, r, in.EventID)
	ph.mark("event")
	if !ok {
		return holdOutcome{key: "hold_error", status: 500}
	}
	if m.SaleStatus != "open" {
		return fail(423, "sale_closed", "the sale is not open")
	}
	if m.QueueRate > 0 {
		if err := a.signer.ValidPass(m.ID, m.Version, user, r.Header.Get("X-Admission-Token"), time.Now()); err != nil {
			return fail(403, "admission_required", "join the waiting room first: "+err.Error())
		}
	}

	req := venue.Request{Quantity: in.Quantity, Tier: in.Tier}
	var explicit []int
	if len(in.SeatIDs) > 0 {
		seen := map[int]bool{}
		for _, id := range in.SeatIDs {
			i, found := v.Index(id)
			if !found || seen[i] {
				return fail(422, "invalid_seats", "unknown or duplicate seat id")
			}
			seen[i] = true
			explicit = append(explicit, i)
		}
		req.Quantity = len(explicit)
	}
	if req.Quantity < 1 || req.Quantity > reservation.MaxSeatsPerHold {
		return fail(422, "invalid_seats", fmt.Sprintf("request between 1 and %d seats", reservation.MaxSeatsPerHold))
	}
	if m.Mode == "naive" {
		return a.naiveHold(w, r, m, v, user, req, explicit)
	}

	keys := inventory.KeysFor(m.ID, m.Version)
	token := uuid.NewString()
	var indexes []int
	won := false
	for attempt := 0; attempt < pickAttempts && !won; attempt++ {
		indexes = explicit
		if indexes == nil {
			bits, err := a.inv.Bits(r.Context(), keys)
			ph.mark("bits")
			if err != nil {
				return fail(503, "redis_unavailable", "admission layer unavailable; failing closed")
			}
			picked, found := v.Pick(bits, req, rand.Float64())
			ph.mark("pick")
			if !found {
				return fail(409, "sold_out", fmt.Sprintf("no %d adjacent seats left", req.Quantity))
			}
			indexes = picked
		}
		var err error
		won, err = a.inv.Claim(r.Context(), keys, token, m.HoldTTL, indexes)
		ph.mark("claim")
		if errors.Is(err, inventory.ErrNotReady) {
			a.rebuild(m)
			w.Header().Set("Retry-After", "3")
			return fail(503, "inventory_rebuilding", "seat inventory is being rebuilt from the database")
		}
		if err != nil {
			return fail(503, "redis_unavailable", "admission layer unavailable; failing closed")
		}
		if explicit != nil {
			break
		}
	}
	if !won {
		return fail(409, "seat_unavailable", "seats were taken by other buyers")
	}

	ctx, cancel := context.WithTimeout(r.Context(), inventory.HoldTxTimeout)
	defer cancel()
	res, err := a.store.CreateHold(ctx, m.ID, user, key, token, indexes, actor(r))
	ph.mark("postgres")
	if errors.Is(err, store.ErrReplayRace) {
		res, err = a.store.CreateHold(ctx, m.ID, user, key, token, indexes, actor(r))
	}
	if err != nil {
		// Release the Redis claim only when PostgreSQL definitely did not
		// commit. On a timeout the outcome is unknown, so the claim is left to
		// expire on its own: briefly unavailable seats are safe, a seat that
		// looks free while held is not.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) || isDomainErr(err) {
			a.releaseClaim(keys, token)
		}
		writeErr(w, r, err)
		return holdOutcome{key: "hold_" + errKey(err), status: statusOf(err), detail: err.Error()}
	}
	if res.Replayed {
		a.releaseClaim(keys, token)
	}
	seats, total := seatsOf(v, res.Indexes)
	status := 201
	if res.Replayed {
		status = 200
	}
	jsonOut(w, status, reservation.Hold{ID: res.ID, EventID: m.ID, Status: "active", ExpiresAt: res.ExpiresAt, Seats: seats, TotalCents: total})
	return holdOutcome{key: fmt.Sprintf("hold_%d", status), status: status, detail: describe(v, res.Indexes)}
}

// naiveHold is the unsafe comparison path: read, decide, write. No Redis, no
// locks, no transaction, no constraint.
func (a *API) naiveHold(w http.ResponseWriter, r *http.Request, m store.EventMeta, v *venue.Venue, user string, req venue.Request, explicit []int) holdOutcome {
	taken, err := a.store.NaiveTaken(r.Context(), m.ID)
	if err != nil {
		internal(w, r, err)
		return holdOutcome{key: "hold_500", status: 500}
	}
	bits := make([]byte, (len(v.Seats)+7)/8)
	for _, i := range taken {
		bits[i>>3] |= 0x80 >> (i & 7)
	}
	indexes := explicit
	if indexes == nil {
		picked, found := v.Pick(bits, req, rand.Float64())
		if !found {
			problem(w, 409, "sold_out", "no adjacent seats left")
			return holdOutcome{key: "hold_409_sold_out", status: 409, detail: "sold out"}
		}
		indexes = picked
	} else {
		for _, i := range indexes {
			if venue.Taken(bits, i) {
				problem(w, 409, "seat_unavailable", reservation.ErrConflict.Error())
				return holdOutcome{key: "hold_409_seat_unavailable", status: 409, detail: "seat taken"}
			}
		}
	}
	seats, total := seatsOf(v, indexes)
	res, err := a.store.NaiveHold(r.Context(), m.ID, user, seats, m.HoldTTL, actor(r))
	if err != nil {
		internal(w, r, err)
		return holdOutcome{key: "hold_500", status: 500}
	}
	jsonOut(w, 201, reservation.Hold{ID: naivePrefix + res.ID, EventID: m.ID, Status: "active", ExpiresAt: res.ExpiresAt, Seats: seats, TotalCents: total})
	return holdOutcome{key: "hold_201", status: 201, detail: describe(v, indexes)}
}

func (a *API) releaseClaim(keys inventory.Keys, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.inv.Release(ctx, keys, token); err != nil {
		slog.Warn("redis claim release failed; it will expire", "error", err)
	}
}

// rebuild restores a lost Redis inventory from PostgreSQL in the background.
func (a *API) rebuild(m store.EventMeta) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		keys := inventory.KeysFor(m.ID, m.Version)
		started := time.Now()
		done, err := a.inv.Rebuild(ctx, keys, func(ctx context.Context) (inventory.Snapshot, error) {
			return a.store.InventorySnapshot(ctx, m.ID)
		})
		if err != nil {
			slog.Error("inventory rebuild failed", "event", m.ID, "error", err)
		} else if done {
			slog.Warn("inventory rebuilt from postgres", "event", m.ID, "took", time.Since(started))
		}
	}()
}

func (a *API) hold(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "holdID")
	if strings.HasPrefix(id, naivePrefix) {
		problem(w, 404, "not_found", "naive holds are write-only")
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		problem(w, 404, "not_found", reservation.ErrNotFound.Error())
		return
	}
	h, err := a.store.Hold(r.Context(), id, user)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if h.Status == "active" && reservation.IsExpired(h.ExpiresAt, time.Now()) {
		writeErr(w, r, reservation.ErrExpired)
		return
	}
	m, err := a.events.meta(r.Context(), h.EventID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := a.events.venue(r.Context(), m)
	if err != nil {
		internal(w, r, err)
		return
	}
	seats, total := seatsOf(v, h.Indexes)
	jsonOut(w, 200, reservation.Hold{ID: h.ID, EventID: h.EventID, Status: h.Status, ExpiresAt: h.ExpiresAt, Seats: seats, TotalCents: total})
}

func (a *API) release(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "holdID")
	if ref, isNaive := strings.CutPrefix(id, naivePrefix); isNaive {
		if _, err := a.store.NaiveRelease(r.Context(), ref, user, actor(r)); err != nil {
			writeErr(w, r, err)
			return
		}
		a.tel.Record(a.simEvent(), "release_204", "release", time.Since(start), telemetry.Sample{Actor: actor(r), Kind: "release", Status: 204, Detail: "gave seats back"})
		w.WriteHeader(204)
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		problem(w, 404, "not_found", reservation.ErrNotFound.Error())
		return
	}
	event, token, _, err := a.store.Release(r.Context(), id, user, actor(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if m, err := a.events.meta(r.Context(), event); err == nil {
		a.releaseClaim(inventory.KeysFor(event, m.Version), token)
	}
	a.tel.Record(event, "release_204", "release", time.Since(start), telemetry.Sample{Actor: actor(r), Kind: "release", Status: 204, Detail: "gave seats back"})
	w.WriteHeader(204)
}

func (a *API) checkout(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "holdID")
	var res store.CheckoutResult
	var err error
	ref, isNaive := strings.CutPrefix(id, naivePrefix)
	switch {
	case isNaive:
		res, err = a.store.NaiveCheckout(r.Context(), ref, user, actor(r))
	case uuid.Validate(id) != nil:
		err = reservation.ErrNotFound
	default:
		ctx, cancel := context.WithTimeout(r.Context(), inventory.HoldTxTimeout)
		res, err = a.store.Checkout(ctx, id, user, key, actor(r))
		cancel()
	}
	event := res.EventID
	if event == "" {
		event = a.simEvent()
	}
	if err != nil {
		writeErr(w, r, err)
		a.tel.Record(event, "checkout_"+errKey(err), "checkout", time.Since(start), telemetry.Sample{Actor: actor(r), Kind: "checkout", Status: statusOf(err), Detail: err.Error()})
		return
	}
	m, err := a.events.meta(r.Context(), res.EventID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := a.events.venue(r.Context(), m)
	if err != nil {
		internal(w, r, err)
		return
	}
	if !isNaive && !res.Replayed {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := a.inv.Confirm(ctx, inventory.KeysFor(m.ID, m.Version), res.Token, res.Indexes); err != nil {
			slog.Warn("redis confirm failed; sold seats are re-asserted by the control plane", "error", err)
		}
		cancel()
	}
	seats, _ := seatsOf(v, res.Indexes)
	jsonOut(w, 200, reservation.Order{ID: res.OrderID, Reference: res.Reference, HoldID: id, TotalCents: res.TotalCents, CreatedAt: res.CreatedAt, Seats: seats})
	a.outcomes.WithLabelValues("checkout_200").Inc()
	a.tel.Record(m.ID, "checkout_200", "checkout", time.Since(start), telemetry.Sample{Actor: actor(r), Kind: "checkout", Status: 200, Detail: fmt.Sprintf("paid $%d · %s", res.TotalCents/100, describe(v, res.Indexes))})
}

func (a *API) order(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "orderID")
	if uuid.Validate(id) != nil {
		problem(w, 404, "not_found", reservation.ErrNotFound.Error())
		return
	}
	o, err := a.store.Order(r.Context(), id, user)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	m, err := a.events.meta(r.Context(), o.EventID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := a.events.venue(r.Context(), m)
	if err != nil {
		internal(w, r, err)
		return
	}
	seats, _ := seatsOf(v, o.Indexes)
	jsonOut(w, 200, reservation.Order{ID: o.ID, Reference: o.Reference, HoldID: o.HoldID, TotalCents: o.TotalCents, CreatedAt: o.CreatedAt, Seats: seats})
}

// simEvent is the event telemetry is attributed to when a request carries no
// event id of its own.
func (a *API) simEvent() string { return "arena" }

func describe(v *venue.Venue, indexes []int) string {
	if len(indexes) == 0 {
		return ""
	}
	row := v.RowOf(indexes[0])
	noun := "seats"
	if len(indexes) == 1 {
		noun = "seat"
	}
	return fmt.Sprintf("%d %s · %s row %s", len(indexes), noun, row.Section, row.Label)
}

func isDomainErr(err error) bool {
	for _, e := range []error{reservation.ErrConflict, reservation.ErrHoldLimit, reservation.ErrInvalidSeats, reservation.ErrSaleClosed, reservation.ErrNotFound, reservation.ErrForbidden, store.ErrReplayRace} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func statusOf(err error) int {
	switch {
	case errors.Is(err, reservation.ErrConflict), errors.Is(err, reservation.ErrSoldOut), errors.Is(err, reservation.ErrHoldLimit):
		return 409
	case errors.Is(err, reservation.ErrExpired):
		return 410
	case errors.Is(err, reservation.ErrForbidden):
		return 403
	case errors.Is(err, reservation.ErrNotFound):
		return 404
	case errors.Is(err, reservation.ErrInvalidSeats):
		return 422
	case errors.Is(err, reservation.ErrSaleClosed):
		return 423
	case errors.Is(err, reservation.ErrPaymentDeclined):
		return 402
	case isTimeout(err):
		return 503
	}
	return 500
}

func errKey(err error) string {
	switch statusOf(err) {
	case 409:
		if errors.Is(err, reservation.ErrHoldLimit) {
			return "409_hold_limit"
		}
		return "409_seat_unavailable"
	case 410:
		return "410"
	case 503:
		return "503_timeout"
	case 500:
		return "500"
	}
	return fmt.Sprint(statusOf(err))
}
