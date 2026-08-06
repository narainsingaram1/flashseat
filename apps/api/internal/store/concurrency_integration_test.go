//go:build integration

package store

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/google/uuid"
)

func TestOneWinnerForSameSeat(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is required")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seat := "balcony-32"
	_, _ = s.Pool.Exec(ctx, `DELETE FROM reservation_events WHERE hold_id IN (SELECT id FROM holds WHERE idempotency_key LIKE 'integration-%')`)
	_, _ = s.Pool.Exec(ctx, `DELETE FROM order_items WHERE seat_id=$1`, seat)
	_, _ = s.Pool.Exec(ctx, `DELETE FROM orders WHERE hold_id IN (SELECT id FROM holds WHERE idempotency_key LIKE 'integration-%')`)
	_, _ = s.Pool.Exec(ctx, `DELETE FROM hold_items WHERE seat_id=$1`, seat)
	_, _ = s.Pool.Exec(ctx, `DELETE FROM holds WHERE idempotency_key LIKE 'integration-%'`)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := uuid.NewString()
			_, e := s.CreateHold(ctx, "aurora-2026", id, "integration-"+id, uuid.NewString(), []string{seat}, time.Minute)
			if e == nil {
				winners.Add(1)
			} else if e != reservation.ErrConflict {
				t.Errorf("unexpected error: %v", e)
			}
		}()
	}
	wg.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("winners=%d, want 1", got)
	}
}
