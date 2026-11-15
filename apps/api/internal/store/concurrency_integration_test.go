//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const testEvent = "integration"

func open(t *testing.T, ttl time.Duration) *Store {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is required")
	}
	ctx := context.Background()
	s, err := Open(ctx, url, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := s.ResetEvent(ctx, SaleConfig{EventID: testEvent, Name: "Integration", Preset: venue.Presets["club"], Mode: "safe", HoldTTL: ttl}); err != nil {
		t.Fatal(err)
	}
	return s
}

func hold(s *Store, idx ...int) error {
	_, err := s.CreateHold(context.Background(), testEvent, uuid.NewString(), uuid.NewString(), uuid.NewString(), idx, "test")
	return err
}

func TestExactlyOneWinnerPerSeat(t *testing.T) {
	s := open(t, time.Minute)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := hold(s, 7)
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, reservation.ErrConflict) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("winners=%d, want 1", got)
	}
}

// Overlapping multi-seat requests lock rows in index order, so they must never
// deadlock and must never leave a seat under two live holds.
func TestOverlappingBlocksNeverDeadlockOrOverlap(t *testing.T) {
	s := open(t, time.Minute)
	var wg sync.WaitGroup
	var won atomic.Int32
	for i := range 200 {
		wg.Add(1)
		go func(start int) {
			defer wg.Done()
			idx := []int{start, start + 1, start + 2, start + 3}
			if start%2 == 1 {
				idx = []int{start + 3, start + 2, start + 1, start} // reversed input order
			}
			err := hold(s, idx...)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				t.Errorf("deadlock: %v", err)
			} else if err == nil {
				won.Add(1)
			} else if !errors.Is(err, reservation.ErrConflict) {
				t.Errorf("unexpected error: %v", err)
			}
		}(i % 40)
	}
	wg.Wait()
	inv, err := s.CheckInvariants(context.Background(), testEvent, "safe")
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Passed() || inv.OverlappingClaims != 0 {
		t.Fatalf("invariants failed: %+v", inv)
	}
	if won.Load() == 0 || inv.SeatsHeld != int(won.Load())*4 {
		t.Fatalf("won=%d but %d seats held", won.Load(), inv.SeatsHeld)
	}
}

// A lapsed hold must not block a new buyer even if the sweeper has not run.
func TestLazyExpiryFreesSeatsWithoutSweeper(t *testing.T) {
	s := open(t, 3*time.Second)
	if err := hold(s, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := hold(s, 2); !errors.Is(err, reservation.ErrConflict) {
		t.Fatalf("seat should be held: %v", err)
	}
	time.Sleep(3100 * time.Millisecond)
	if err := hold(s, 2); err != nil {
		t.Fatalf("expired hold still blocks the seat: %v", err)
	}
}

func TestCheckoutIsIdempotentAndSellsOnce(t *testing.T) {
	s := open(t, time.Minute)
	ctx := context.Background()
	user := uuid.NewString()
	h, err := s.CreateHold(ctx, testEvent, user, "hold-key-1", uuid.NewString(), []int{10, 11}, "test")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateHold(ctx, testEvent, user, "hold-key-1", uuid.NewString(), []int{20}, "test")
	if err != nil || !again.Replayed || again.ID != h.ID {
		t.Fatalf("hold replay: %+v %v", again, err)
	}
	var wg sync.WaitGroup
	orders := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := s.Checkout(ctx, h.ID, user, "checkout-key-1", "test")
			if err != nil && !errors.Is(err, ErrReplayRace) {
				t.Errorf("checkout: %v", err)
				return
			}
			orders <- o.OrderID
		}()
	}
	wg.Wait()
	close(orders)
	ids := map[string]bool{}
	for id := range orders {
		if id != "" {
			ids[id] = true
		}
	}
	if len(ids) != 1 {
		t.Fatalf("checkout created %d distinct orders", len(ids))
	}
	inv, _ := s.CheckInvariants(ctx, testEvent, "safe")
	if inv.SeatsSold != 2 || inv.Orders != 1 || !inv.Passed() {
		t.Fatalf("invariants after checkout: %+v", inv)
	}
	if err := hold(s, 11); !errors.Is(err, reservation.ErrConflict) {
		t.Fatalf("sold seat was held again: %v", err)
	}
}

func TestOneActiveHoldPerBuyer(t *testing.T) {
	s := open(t, time.Minute)
	ctx := context.Background()
	user := uuid.NewString()
	if _, err := s.CreateHold(ctx, testEvent, user, "first-hold", uuid.NewString(), []int{30}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateHold(ctx, testEvent, user, "second-hold", uuid.NewString(), []int{31}, "test"); !errors.Is(err, reservation.ErrHoldLimit) {
		t.Fatalf("second concurrent hold allowed: %v", err)
	}
}

// A write the unique index rejects is logged and fails the run's invariants,
// even though no seat ends up sold twice.
func TestBackstopHitFailsInvariants(t *testing.T) {
	s := open(t, time.Minute)
	ctx := context.Background()
	if err := hold(s, 3); err != nil {
		t.Fatal(err)
	}
	if err := hold(s, 5); err != nil {
		t.Fatal(err)
	}
	inv, err := s.CheckInvariants(ctx, testEvent, "safe")
	if err != nil || !inv.Passed() || inv.BackstopHits != 0 {
		t.Fatalf("before: %+v err=%v", inv, err)
	}
	// Bypass fs_create_hold's checks and add seat 3 to the other buyer's hold,
	// as a regressed engine would; only the unique index stops it.
	_, err = s.Pool.Exec(ctx, `INSERT INTO hold_items(hold_id,seat_id,price_cents)
		SELECT hi.hold_id, s3.id, s3.price_cents
		  FROM hold_items hi JOIN seats s5 ON s5.id=hi.seat_id AND s5.seat_index=5,
		       seats s3
		 WHERE s5.event_id=$1 AND s3.event_id=$1 AND s3.seat_index=3`, testEvent)
	if _, ok := backstopConstraint(err); !ok {
		t.Fatalf("want a one_live_hold_per_seat violation, got %v", err)
	}
	if err := s.recordBackstop(ctx, err, "hold", "$1::text", testEvent, "test"); err != nil {
		t.Fatal(err)
	}
	inv, err = s.CheckInvariants(ctx, testEvent, "safe")
	if err != nil {
		t.Fatal(err)
	}
	if inv.DoubleSold != 0 || inv.BackstopHits != 1 || inv.Passed() {
		t.Fatalf("after: %+v, want 0 double-sold, 1 backstop hit, not passed", inv)
	}
}
