package control

import (
	"testing"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/store"
)

func ev(id int64, typ string, idx ...int) store.FeedEvent {
	return store.FeedEvent{ID: id, Type: typ, Indexes: idx, CreatedAt: time.Unix(id, 0)}
}

func TestBoardSafeLifecycle(t *testing.T) {
	b := NewBoard(4)
	b.Apply(ev(1, "hold.created", 0, 1))
	b.Apply(ev(2, "hold.expired", 0, 1))
	b.Apply(ev(3, "hold.created", 1, 2))
	b.Apply(ev(4, "order.created", 1, 2))
	if string(b.State) != "3220" {
		t.Fatalf("state=%s", b.State)
	}
	if b.SeatsSold != 2 || b.ResoldSeats != 1 || b.HoldsExpired != 1 || b.Orders != 1 {
		t.Fatalf("counters %+v", b)
	}
	d := b.Drain()
	if len(d)%2 != 0 || len(b.Drain()) != 0 {
		t.Fatal("drain must return pairs once")
	}
}

func TestBoardFlagsDoubleBooking(t *testing.T) {
	b := NewBoard(2)
	b.Apply(ev(1, "naive.held", 0))
	b.Apply(ev(2, "naive.held", 0))
	if b.State[0] != Conflict {
		t.Fatal("two live claims on one seat must show as a conflict")
	}
	b.Apply(ev(3, "naive.sold", 0))
	b.Apply(ev(4, "naive.sold", 0))
	if b.State[0] != Conflict || b.Counts()["conflict"] != 1 {
		t.Fatal("a seat sold twice must stay a conflict")
	}
}

func TestCursorWaitsForGapThenSkips(t *testing.T) {
	c := Cursor{Last: 10}
	now := time.Unix(100, 0)
	rows := []store.FeedEvent{{ID: 11}, {ID: 13}, {ID: 14}}
	got := c.Accept(rows, now)
	if len(got) != 1 || c.Last != 11 {
		t.Fatalf("applied %d rows, last=%d", len(got), c.Last)
	}
	// Row 12 commits shortly after: everything applies in order.
	got = c.Accept([]store.FeedEvent{{ID: 12}, {ID: 13}, {ID: 14}}, now.Add(50*time.Millisecond))
	if len(got) != 3 || c.Last != 14 {
		t.Fatalf("applied %d rows, last=%d", len(got), c.Last)
	}
	// Row 15 never commits: after gapWait the cursor moves on.
	if got = c.Accept([]store.FeedEvent{{ID: 16}}, now.Add(time.Second)); len(got) != 0 {
		t.Fatal("cursor skipped a gap immediately")
	}
	if got = c.Accept([]store.FeedEvent{{ID: 16}}, now.Add(2*time.Second+time.Millisecond)); len(got) != 1 || c.Last != 16 {
		t.Fatal("cursor never skipped a permanent gap")
	}
}
