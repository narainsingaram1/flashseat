//go:build integration

package inventory

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func setup(t *testing.T) (*Inventory, Keys) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is required")
	}
	inv := New(addr, 64)
	t.Cleanup(func() { inv.Close() })
	k := KeysFor("itest-"+uuid.NewString()[:8], 1)
	t.Cleanup(func() { inv.Drop(context.Background(), k) })
	if err := inv.Load(context.Background(), k, Snapshot{}); err != nil {
		t.Fatal(err)
	}
	return inv, k
}

func TestClaimIsAllOrNothing(t *testing.T) {
	inv, k := setup(t)
	ctx := context.Background()
	if ok, err := inv.Claim(ctx, k, "a", time.Minute, []int{1, 2, 3}); !ok || err != nil {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := inv.Claim(ctx, k, "b", time.Minute, []int{3, 4}); ok {
		t.Fatal("overlapping claim succeeded")
	}
	bits, _ := inv.Bits(ctx, k)
	if bits[0] != 0b01110000 {
		t.Fatalf("losing claim wrote bits: %08b", bits[0])
	}
}

func TestOnlyOneConcurrentClaimWins(t *testing.T) {
	inv, k := setup(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 500 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := inv.Claim(context.Background(), k, uuid.NewString(), time.Minute, []int{i % 3, 42}); err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims on seat 42 won", wins.Load())
	}
}

func TestReleaseIsTokenScopedAndExpirySweeps(t *testing.T) {
	inv, k := setup(t)
	ctx := context.Background()
	_, _ = inv.Claim(ctx, k, "owner", -Settle+300*time.Millisecond, []int{5})
	_ = inv.Release(ctx, k, "intruder")
	if ok, _ := inv.Claim(ctx, k, "other", time.Minute, []int{5}); ok {
		t.Fatal("release by a non-owner freed the seat")
	}
	time.Sleep(400 * time.Millisecond)
	if ok, _ := inv.Claim(ctx, k, "other", time.Minute, []int{5}); !ok {
		t.Fatal("expired claim was not swept by the next claim")
	}
	// The stale owner's release must not free the new owner's seat.
	_ = inv.Release(ctx, k, "owner")
	if ok, _ := inv.Claim(ctx, k, "third", time.Minute, []int{5}); ok {
		t.Fatal("stale release freed another buyer's seat")
	}
}

func TestConfirmKeepsSeatTakenAfterExpiry(t *testing.T) {
	inv, k := setup(t)
	ctx := context.Background()
	_, _ = inv.Claim(ctx, k, "buyer", -Settle+200*time.Millisecond, []int{9})
	if err := inv.Confirm(ctx, k, "buyer", []int{9}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n, _ := inv.Sweep(ctx, k); n != 0 {
		t.Fatalf("sweep found %d claims after confirm", n)
	}
	if ok, _ := inv.Claim(ctx, k, "late", time.Minute, []int{9}); ok {
		t.Fatal("sold seat became claimable")
	}
}

func TestMissingInventoryFailsClosedUntilRebuilt(t *testing.T) {
	inv, k := setup(t)
	ctx := context.Background()
	_ = inv.Drop(ctx, k)
	if _, err := inv.Claim(ctx, k, "x", time.Minute, []int{1}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("claim on a missing inventory: %v", err)
	}
	done, err := inv.Rebuild(ctx, k, func(context.Context) (Snapshot, error) {
		return Snapshot{Sold: []int{1}, Claims: []LiveClaim{{Token: "live", ExpiresAt: time.Now().Add(time.Minute), Indexes: []int{2}}}}, nil
	})
	if err != nil || !done {
		t.Fatalf("rebuild: %v %v", done, err)
	}
	for _, seat := range []int{1, 2} {
		if ok, _ := inv.Claim(ctx, k, "y", time.Minute, []int{seat}); ok {
			t.Fatalf("seat %d free after rebuild", seat)
		}
	}
	_ = inv.Release(ctx, k, "live")
	if ok, _ := inv.Claim(ctx, k, "y", time.Minute, []int{2}); !ok {
		t.Fatal("restored claim could not be released by its token")
	}
}
