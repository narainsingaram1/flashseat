package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/admission"
	"github.com/flashseat/flashseat/apps/api/internal/config"
	"github.com/flashseat/flashseat/apps/api/internal/inventory"
	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/flashseat/flashseat/apps/api/internal/telemetry"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
)

// SimEvent is the event every simulation rebuilds and sells.
const SimEvent = "arena"

// Service runs the control plane's loops.
type Service struct {
	cfg   config.Config
	store *store.Store
	inv   *inventory.Inventory
	Hub   *Hub
	Chaos *Chaos

	mu      sync.Mutex
	cursor  Cursor
	tailing bool
	version int64
	lost    bool // the active inventory was missing at the last sweep
}

func NewService(cfg config.Config, s *store.Store, inv *inventory.Inventory) *Service {
	hub := NewHub()
	return &Service{cfg: cfg, store: s, inv: inv, Hub: hub, Chaos: NewChaos(cfg.ToxiproxyURL, cfg.APIURL, inv.Client, hub)}
}

// Run starts every loop and blocks until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if err := s.store.FailInterruptedRuns(ctx); err != nil {
		slog.Error("could not reconcile interrupted runs", "error", err)
	}
	var wg sync.WaitGroup
	loop := func(every time.Duration, f func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					f(ctx)
				}
			}
		}()
	}
	loop(FrameInterval, func(context.Context) { s.Hub.Tick(time.Now()) })
	loop(100*time.Millisecond, s.tail)
	loop(s.cfg.SweepInterval, s.sweep)
	loop(time.Second, s.checkInvariants)
	loop(500*time.Millisecond, s.queueStats)
	loop(500*time.Millisecond, s.pollRuns)
	wg.Wait()
}

func (s *Service) tail(ctx context.Context) {
	s.mu.Lock()
	if !s.tailing {
		s.mu.Unlock()
		return
	}
	after := s.cursor.Last
	s.mu.Unlock()
	rows, err := s.store.FeedAfter(ctx, after, 5000)
	if err != nil || len(rows) == 0 {
		return
	}
	s.mu.Lock()
	ready := s.cursor.Accept(rows, time.Now())
	s.mu.Unlock()
	s.Hub.ApplyFeed(ready)
}

// sweep expires lapsed holds in PostgreSQL and frees their Redis claims. Hot
// paths expire holds lazily too; this keeps seats from waiting on the next
// buyer to touch them.
func (s *Service) sweep(ctx context.Context) {
	versions := map[string]int64{}
	for range 10 {
		batch, err := s.store.ExpireDue(ctx, 500)
		if err != nil {
			slog.Error("expiry sweep failed", "error", err)
			return
		}
		for _, c := range batch {
			v, ok := versions[c.EventID]
			if !ok {
				m, err := s.store.EventMeta(ctx, c.EventID)
				if err != nil {
					continue
				}
				v, versions[c.EventID] = m.Version, m.Version
			}
			_ = s.inv.Release(ctx, inventory.KeysFor(c.EventID, v), c.Token)
		}
		if len(batch) < 500 {
			break
		}
	}
	if _, err := s.store.NaiveExpireDue(ctx); err != nil {
		slog.Error("naive expiry sweep failed", "error", err)
	}
	s.mu.Lock()
	v := s.version
	s.mu.Unlock()
	if v > 0 {
		keys := inventory.KeysFor(SimEvent, v)
		_, _ = s.inv.Sweep(ctx, keys)
		// If Redis lost the inventory, rebuild it now rather than waiting for
		// the next buyer to discover it.
		ready, err := s.inv.Ready(ctx, keys)
		if err == nil && !ready {
			s.lost = true
			go s.rebuild(keys)
		} else if err == nil && s.lost {
			s.lost = false
			s.Hub.MarkChaos("redis-wipe", "end", "seat bitmap restored from PostgreSQL")
		}
	}
}

func (s *Service) rebuild(keys inventory.Keys) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := time.Now()
	done, err := s.inv.Rebuild(ctx, keys, func(ctx context.Context) (inventory.Snapshot, error) {
		return s.store.InventorySnapshot(ctx, SimEvent)
	})
	if err != nil {
		slog.Error("inventory rebuild failed", "error", err)
	} else if done {
		slog.Warn("inventory rebuilt from postgres", "took", time.Since(started).Round(time.Millisecond))
	}
}

// Check computes invariants and, in safe mode, compares Redis with PostgreSQL.
func (s *Service) Check(ctx context.Context, mode string) (store.Invariants, error) {
	inv, err := s.store.CheckInvariants(ctx, SimEvent, mode)
	if err != nil || mode != "safe" {
		return inv, err
	}
	s.mu.Lock()
	v := s.version
	s.mu.Unlock()
	keys := inventory.KeysFor(SimEvent, v)
	ready, err := s.inv.Ready(ctx, keys)
	if err != nil || !ready {
		inv.RedisDrift = -1 // unknown: Redis unreachable or rebuilding
		return inv, nil
	}
	bits, err := s.inv.Bits(ctx, keys)
	if err != nil {
		inv.RedisDrift = -1
		return inv, nil
	}
	sold, held, err := s.store.TakenIndexes(ctx, SimEvent)
	if err != nil {
		return inv, err
	}
	var soldDrift []int
	for _, i := range sold {
		if !venue.Taken(bits, i) {
			inv.RedisDrift++
			soldDrift = append(soldDrift, i)
		}
	}
	for _, i := range held {
		if !venue.Taken(bits, i) {
			inv.RedisDrift++
		}
	}
	// Repair the only direction that is always safe to repair.
	_ = s.inv.AssertSold(ctx, keys, soldDrift)
	return inv, nil
}

func (s *Service) checkInvariants(ctx context.Context) {
	p := s.Hub.Phase()
	if p != PhaseSelling && p != PhaseSettling {
		return
	}
	inv, err := s.Check(ctx, s.Hub.Mode())
	if err != nil {
		slog.Error("invariant check failed", "error", err)
		return
	}
	s.Hub.SetInvariants(inv)
}

func (s *Service) queueStats(ctx context.Context) {
	p := s.Hub.Phase()
	if p != PhaseSelling && p != PhaseSettling {
		return
	}
	m, err := s.store.EventMeta(ctx, SimEvent)
	if err != nil {
		return
	}
	issued, err := s.store.QueueIssued(ctx, m.QueueBase)
	if err != nil {
		return
	}
	q := Queue{Issued: issued, Admitted: issued}
	if m.QueueRate > 0 && m.QueueOpenedAt != nil {
		q.Admitted = min(issued, admission.Gate{Rate: m.QueueRate, OpenedAt: *m.QueueOpenedAt}.Admitted(time.Now()))
	} else if m.QueueRate > 0 {
		q.Admitted = 0
	}
	q.Waiting = q.Issued - q.Admitted
	s.Hub.SetQueue(q)
}

func (s *Service) pollRuns(ctx context.Context) {
	run, ok, err := s.store.ClaimQueuedRun(ctx)
	if err != nil {
		slog.Error("could not claim queued run", "error", err)
		return
	}
	if !ok {
		return
	}
	slog.Info("simulation starting", "run", run.ID, "mode", run.Mode, "preset", run.Preset, "buyers", run.Buyers)
	if err := s.execute(ctx, run); err != nil {
		slog.Error("simulation failed", "run", run.ID, "error", err)
		s.Hub.SetPhase(PhaseFailed)
		_ = s.store.UpdateRun(context.Background(), run.ID, store.RunUpdate{Status: "failed", Failure: err.Error(), Final: true})
		_ = s.store.CloseSale(context.Background(), SimEvent)
	}
}

// LoadRequest is what the load generator accepts.
type LoadRequest struct {
	RunID          string `json:"runId"`
	EventID        string `json:"eventId"`
	Buyers         int    `json:"buyers"`
	ArrivalSeconds int    `json:"arrivalSeconds"`
	TargetURL      string `json:"targetUrl"`
}

// LoadResult is what the load generator returns.
type LoadResult struct {
	K6Version string          `json:"k6Version"`
	Summary   json.RawMessage `json:"summary"`
}

func (s *Service) execute(ctx context.Context, run store.Run) error {
	preset, ok := venue.Presets[run.Preset]
	if !ok {
		return fmt.Errorf("unknown venue preset %q", run.Preset)
	}
	s.Chaos.Heal(ctx)
	s.mu.Lock()
	oldVersion := s.version
	s.tailing = false
	s.mu.Unlock()

	version, err := s.store.ResetEvent(ctx, store.SaleConfig{EventID: SimEvent, Name: "FlashSeat Live — " + preset.Name, Preset: preset,
		Mode: run.Mode, HoldTTL: time.Duration(run.HoldTTLSeconds) * time.Second, QueueRate: run.AdmitRate})
	if err != nil {
		return fmt.Errorf("reset venue: %w", err)
	}
	if oldVersion > 0 {
		_ = s.inv.Drop(ctx, inventory.KeysFor(SimEvent, oldVersion))
	}
	keys := inventory.KeysFor(SimEvent, version)
	if err := s.inv.Load(ctx, keys, inventory.Snapshot{}); err != nil {
		return fmt.Errorf("load redis inventory: %w", err)
	}
	layout, err := s.store.Layout(ctx, SimEvent)
	if err != nil {
		return err
	}
	v, err := venue.Build(layout)
	if err != nil {
		return err
	}
	cursor, err := s.store.MaxFeedID(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.version, s.cursor, s.tailing = version, Cursor{Last: cursor}, true
	s.mu.Unlock()
	run.Seats = len(v.Seats)
	_ = s.store.UpdateRun(ctx, run.ID, store.RunUpdate{Seats: len(v.Seats)})
	s.Hub.Begin(run, SimEvent, v)

	// API replicas refresh event configuration every 500 ms. The sale clock
	// starts when the first buyer reaches the API (see Hub.AddReport), not
	// while k6 is still starting its virtual users.
	time.Sleep(1500 * time.Millisecond)

	load, loadErr := s.callLoadgen(ctx, LoadRequest{RunID: run.ID, EventID: SimEvent, Buyers: run.Buyers, ArrivalSeconds: run.ArrivalSeconds, TargetURL: s.cfg.LoadTargetURL},
		time.Duration(run.ArrivalSeconds)*time.Second+6*time.Minute)
	clientDone := time.Now()
	saleStart := s.Hub.SaleStart()

	s.Hub.SetPhase(PhaseSettling)
	settleBy := time.Now().Add(time.Duration(run.HoldTTLSeconds)*time.Second + inventory.Settle + 3*time.Second)
	var final store.Invariants
	for {
		final, err = s.Check(ctx, run.Mode)
		if err == nil {
			s.Hub.SetInvariants(final)
		}
		if (err == nil && final.SeatsHeld == 0) || time.Now().After(settleBy) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = s.store.CloseSale(ctx, SimEvent)
	time.Sleep(gapWait + 300*time.Millisecond) // let the tail catch up to the last commits
	final, err = s.Check(ctx, run.Mode)
	if err != nil {
		return fmt.Errorf("final invariant check: %w", err)
	}
	s.Hub.SetInvariants(final)
	s.Hub.Tick(time.Now())

	metrics := s.metrics(run, final, saleStart, clientDone, load)
	timeline, err := s.Hub.Timeline(&final)
	if err != nil {
		return err
	}
	_, _, _, _, _, chaos := s.Hub.Summary()
	invJSON, _ := json.Marshal(map[string]any{"final": final, "passed": final.Passed()})
	metricsJSON, _ := json.Marshal(metrics)
	chaosJSON, _ := json.Marshal(chaos)
	update := store.RunUpdate{Status: "succeeded", Metrics: metricsJSON, Invariants: invJSON, Chaos: chaosJSON, Timeline: timeline, Final: true}
	if load != nil {
		update.K6Summary = load.Summary
	}
	if loadErr != nil {
		update.Status, update.Failure = "failed", loadErr.Error()
	}
	if err := s.store.UpdateRun(context.Background(), run.ID, update); err != nil {
		return err
	}
	if loadErr != nil {
		s.Hub.SetPhase(PhaseFailed)
	} else {
		s.Hub.SetPhase(PhaseDone)
	}
	s.Hub.Tick(time.Now())
	slog.Info("simulation finished", "run", run.ID, "sold", final.SeatsSold, "double_sold", final.DoubleSold, "status", update.Status)
	return nil
}

func (s *Service) metrics(run store.Run, final store.Invariants, saleStart, clientDone time.Time, load *LoadResult) map[string]any {
	board, totals, hold, checkout, series, _ := s.Hub.Summary()
	var peak, requests int64
	var peakHolds int64
	for _, p := range series {
		peak = max(peak, p.Requests)
		peakHolds = max(peakHolds, p.Holds+p.Conflicts+p.SoldOut)
	}
	for _, v := range totals {
		requests += v
	}
	m := map[string]any{
		"seats": run.Seats, "buyers": run.Buyers, "mode": run.Mode,
		"seatsSold": final.SeatsSold, "orders": final.Orders, "revenueCents": final.RevenueCents,
		"sellThroughPct":         pct(final.SeatsSold, run.Seats),
		"doubleSold":             final.DoubleSold,
		"overlapping":            final.OverlappingClaims,
		"holdsCreated":           board.HoldsCreated,
		"holdsExpired":           board.HoldsExpired,
		"holdsReleased":          board.HoldsReleased,
		"resoldSeats":            board.ResoldSeats,
		"serverRequests":         requests,
		"peakRequestsPerSec":     peak,
		"peakHoldAttemptsPerSec": peakHolds,
		"holdP50Ms":              telemetry.Quantile(hold, .5),
		"holdP95Ms":              telemetry.Quantile(hold, .95),
		"holdP99Ms":              telemetry.Quantile(hold, .99),
		"checkoutP99Ms":          telemetry.Quantile(checkout, .99),
		"responses":              totals,
		"clientDurationMs":       clientDone.Sub(saleStart).Milliseconds(),
	}
	if !board.LastSale.IsZero() {
		m["lastSaleMs"] = board.LastSale.Sub(saleStart).Milliseconds()
		if final.SeatsSold >= run.Seats {
			m["soldOutMs"] = board.LastSale.Sub(saleStart).Milliseconds()
		}
	}
	if load != nil {
		m["k6Version"] = load.K6Version
		var client struct {
			BuyersPurchased int `json:"buyersPurchased"`
		}
		if json.Unmarshal(load.Summary, &client) == nil {
			// Orders PostgreSQL committed that no client saw confirmed: the
			// buyer's every retry timed out. Reported, never hidden.
			m["unconfirmedOrders"] = final.Orders - client.BuyersPurchased
		}
	}
	return m
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

func (s *Service) callLoadgen(ctx context.Context, in LoadRequest, timeout time.Duration) (*LoadResult, error) {
	body, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.LoadgenURL+"/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("load generator unavailable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("load generator returned %d: %s", resp.StatusCode, tail(string(raw), 2000))
	}
	var out LoadResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("invalid load generator response: %w", err)
	}
	return &out, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// RunInput is a request to start a simulation.
type RunInput struct {
	Mode           string `json:"mode"`
	Preset         string `json:"preset"`
	Buyers         int    `json:"buyers"`
	ArrivalSeconds int    `json:"arrivalSeconds"`
	AdmitRate      int    `json:"admitRate"`
	HoldTTLSeconds int    `json:"holdTtlSeconds"`
}

func (in RunInput) Validate() error {
	switch {
	case in.Mode != "safe" && in.Mode != "naive":
		return errors.New("mode must be safe or naive")
	case venue.Presets[in.Preset].ID == "":
		return errors.New("unknown venue preset")
	case in.Buyers < 10 || in.Buyers > 20000:
		return errors.New("buyers must be between 10 and 20,000")
	case in.ArrivalSeconds < 1 || in.ArrivalSeconds > 300:
		return errors.New("arrival window must be 1–300 seconds")
	case in.AdmitRate < 0 || in.AdmitRate > 5000:
		return errors.New("admission rate must be 0–5,000 buyers per second")
	case in.HoldTTLSeconds < 3 || in.HoldTTLSeconds > 600:
		return errors.New("hold time must be 3–600 seconds")
	}
	return nil
}
