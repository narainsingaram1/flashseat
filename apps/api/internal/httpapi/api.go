// Package httpapi is the data plane: sessions, the waiting room, holds,
// checkout, and orders. It is stateless apart from caches, so any number of
// replicas can run behind the gateway.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/admission"
	"github.com/flashseat/flashseat/apps/api/internal/config"
	"github.com/flashseat/flashseat/apps/api/internal/inventory"
	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/flashseat/flashseat/apps/api/internal/telemetry"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type API struct {
	cfg      config.Config
	store    *store.Store
	inv      *inventory.Inventory
	events   *eventCache
	signer   admission.Signer
	tel      *telemetry.Recorder
	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	outcomes *prometheus.CounterVec
	exit     func(int)
}

func New(cfg config.Config, s *store.Store, inv *inventory.Inventory, tel *telemetry.Recorder) *API {
	a := &API{cfg: cfg, store: s, inv: inv, events: newEventCache(s), signer: admission.NewSigner(cfg.AdmissionSecret), tel: tel, exit: os.Exit,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "flashseat_http_requests_total", Help: "HTTP requests."}, []string{"method", "route", "status"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "flashseat_http_duration_seconds", Help: "HTTP latency.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}}, []string{"method", "route"}),
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "flashseat_reservation_outcomes_total", Help: "Reservation outcomes by kind."}, []string{"outcome"}),
	}
	prometheus.MustRegister(a.requests, a.latency, a.outcomes)
	return a
}

// Run starts background work: keeping event configuration fresh.
func (a *API) Run(ctx context.Context) { a.events.run(ctx) }

func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(a.requestID, a.cors, a.measure)
	r.Get("/health/live", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/health/ready", a.ready)
	r.Handle("/metrics", promhttp.Handler())
	if a.cfg.LabEnabled {
		// Chaos hook for the simulator. The gateway does not route /internal.
		r.Post("/internal/crash", a.crash)
	}
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/sessions/demo", a.session)
		r.Get("/events", a.listEvents)
		r.Get("/events/{eventID}", a.event)
		r.Get("/events/{eventID}/layout", a.layout)
		r.Get("/events/{eventID}/seats", a.seats)
		r.Post("/events/{eventID}/queue", a.joinQueue)
		r.Get("/events/{eventID}/queue/{ticket}", a.queueStatus)
		r.Post("/holds", a.createHold)
		r.Get("/holds/{holdID}", a.hold)
		r.Delete("/holds/{holdID}", a.release)
		r.Post("/holds/{holdID}/checkout", a.checkout)
		r.Get("/orders/{orderID}", a.order)
	})
	return r
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *API) measure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		route := "unmatched"
		if c := chi.RouteContext(r.Context()); c != nil && c.RoutePattern() != "" {
			route = c.RoutePattern()
		}
		a.requests.WithLabelValues(r.Method, route, fmt.Sprint(sw.status)).Inc()
		a.latency.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

type requestIDKey struct{}

func (a *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", a.cfg.WebOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-Request-ID, X-Admission-Token, X-Buyer")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Pool.Ping(r.Context()); err != nil {
		problem(w, 503, "not_ready", "database unavailable")
		return
	}
	if err := a.inv.Ping(r.Context()); err != nil {
		problem(w, 503, "not_ready", "redis unavailable")
		return
	}
	jsonOut(w, 200, map[string]string{"status": "ready", "replica": a.cfg.Replica})
}

func (a *API) crash(w http.ResponseWriter, r *http.Request) {
	slog.Warn("chaos: replica crashing on request", "replica", a.cfg.Replica)
	jsonOut(w, 202, map[string]string{"replica": a.cfg.Replica})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() { time.Sleep(50 * time.Millisecond); a.exit(1) }()
}

func (a *API) session(w http.ResponseWriter, r *http.Request) {
	id := userID(r)
	if id == "" {
		id = uuid.NewString()
	}
	http.SetCookie(w, &http.Cookie{Name: "flashseat_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 7})
	jsonOut(w, 200, map[string]string{"userId": id})
}

func userID(r *http.Request) string {
	c, err := r.Cookie("flashseat_session")
	if err != nil {
		return ""
	}
	if _, err = uuid.Parse(c.Value); err != nil {
		return ""
	}
	return c.Value
}

func requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := userID(r)
	if id == "" {
		problem(w, 401, "session_required", "create a demo session first")
		return "", false
	}
	return id, true
}

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(v) < 8 || len(v) > 100 {
		problem(w, 422, "invalid_idempotency_key", "Idempotency-Key must be 8–100 characters")
		return "", false
	}
	return v, true
}

// actor is the buyer label shown in the live feed (k6 sets X-Buyer).
func actor(r *http.Request) string {
	v := r.Header.Get("X-Buyer")
	if v == "" || len(v) > 32 {
		return "web"
	}
	for _, c := range v {
		if !(c == '-' || c == '#' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return "web"
		}
	}
	return v
}

type eventView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Venue      string    `json:"venue"`
	StartsAt   time.Time `json:"startsAt"`
	SaleStatus string    `json:"saleStatus"`
	Mode       string    `json:"mode"`
	HoldTTL    int       `json:"holdTtlSeconds"`
	QueueRate  int       `json:"queueRate"`
	Version    int64     `json:"inventoryVersion"`
}

func viewEvent(m store.EventMeta) eventView {
	return eventView{ID: m.ID, Name: m.Name, Venue: m.Venue, StartsAt: m.StartsAt, SaleStatus: m.SaleStatus, Mode: m.Mode, HoldTTL: int(m.HoldTTL / time.Second), QueueRate: m.QueueRate, Version: m.Version}
}

func (a *API) listEvents(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.Events(r.Context())
	if err != nil {
		internal(w, r, err)
		return
	}
	out := make([]eventView, len(list))
	for i, m := range list {
		out[i] = viewEvent(m)
	}
	jsonOut(w, 200, map[string]any{"events": out})
}

func (a *API) event(w http.ResponseWriter, r *http.Request) {
	m, err := a.events.meta(r.Context(), chi.URLParam(r, "eventID"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	jsonOut(w, 200, viewEvent(m))
}

func (a *API) layout(w http.ResponseWriter, r *http.Request) {
	m, v, ok := a.eventAndVenue(w, r, chi.URLParam(r, "eventID"))
	if !ok {
		return
	}
	jsonOut(w, 200, map[string]any{"eventId": m.ID, "version": m.Version, "seats": len(v.Seats), "width": v.Width, "height": v.Height, "sections": v.Sections, "rows": v.Rows})
}

// seats returns one character per seat in bitmap order: 0 free, 1 held, 2 sold.
func (a *API) seats(w http.ResponseWriter, r *http.Request) {
	m, v, ok := a.eventAndVenue(w, r, chi.URLParam(r, "eventID"))
	if !ok {
		return
	}
	states := make([]byte, len(v.Seats))
	for i := range states {
		states[i] = '0'
	}
	if m.Mode == "naive" {
		taken, err := a.store.NaiveTaken(r.Context(), m.ID)
		if err != nil {
			internal(w, r, err)
			return
		}
		for _, i := range taken {
			states[i] = '1'
		}
	} else {
		sold, held, err := a.store.TakenIndexes(r.Context(), m.ID)
		if err != nil {
			internal(w, r, err)
			return
		}
		for _, i := range held {
			states[i] = '1'
		}
		for _, i := range sold {
			states[i] = '2'
		}
	}
	jsonOut(w, 200, map[string]any{"eventId": m.ID, "version": m.Version, "states": string(states), "serverTime": time.Now().UTC()})
}

func (a *API) eventAndVenue(w http.ResponseWriter, r *http.Request, eventID string) (store.EventMeta, *venue.Venue, bool) {
	m, err := a.events.meta(r.Context(), eventID)
	if err != nil {
		writeErr(w, r, err)
		return m, nil, false
	}
	v, err := a.events.venue(r.Context(), m)
	if err != nil {
		internal(w, r, err)
		return m, nil, false
	}
	return m, v, true
}

func seatsOf(v *venue.Venue, indexes []int) ([]reservation.Seat, int) {
	out := make([]reservation.Seat, 0, len(indexes))
	total := 0
	for _, i := range indexes {
		if i < 0 || i >= len(v.Seats) {
			continue
		}
		s := v.Seats[i]
		out = append(out, reservation.Seat{ID: s.ID, Index: s.Index, Section: s.Section, Row: s.Row, Number: s.Number, PriceCents: s.PriceCents})
		total += s.PriceCents
	}
	return out, total
}

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, status int, code, message string) {
	jsonOut(w, status, map[string]any{"code": code, "message": message, "status": status})
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, reservation.ErrConflict):
		problem(w, 409, "seat_unavailable", err.Error())
	case errors.Is(err, reservation.ErrSoldOut):
		problem(w, 409, "sold_out", err.Error())
	case errors.Is(err, reservation.ErrHoldLimit):
		problem(w, 409, "hold_limit", err.Error())
	case errors.Is(err, reservation.ErrExpired):
		problem(w, 410, "hold_expired", err.Error())
	case errors.Is(err, reservation.ErrForbidden):
		problem(w, 403, "forbidden", err.Error())
	case errors.Is(err, reservation.ErrNotFound):
		problem(w, 404, "not_found", err.Error())
	case errors.Is(err, reservation.ErrInvalidSeats):
		problem(w, 422, "invalid_seats", err.Error())
	case errors.Is(err, reservation.ErrSaleClosed):
		problem(w, 423, "sale_closed", err.Error())
	case errors.Is(err, reservation.ErrPaymentDeclined):
		problem(w, 402, "payment_declined", err.Error())
	case isTimeout(err):
		problem(w, 503, "timeout", "the reservation could not be confirmed in time; retry with the same Idempotency-Key")
	default:
		internal(w, r, err)
	}
}

// isTimeout reports whether the database outcome is unknown because the call
// ran out of time, which the client should retry with the same key.
func isTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err)
}

func internal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "request_id", r.Context().Value(requestIDKey{}), "error", err)
	problem(w, 500, "internal_error", "the request could not be completed")
}
