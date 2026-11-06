// Package control is the control plane: it runs simulations, drives the load
// generator, tails PostgreSQL's change feed into a live seat map, checks
// correctness invariants, injects faults, and records replays. It holds no
// reservation state of its own; the data plane keeps working without it.
package control

import (
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/store"
)

// Seat states, one ASCII digit per seat so a snapshot is a plain string.
const (
	Free     byte = '0'
	Held     byte = '1'
	Sold     byte = '2'
	Returned byte = '3' // free again because a hold expired
	Conflict byte = '4' // claimed or sold by more than one buyer at once
)

// Board replays the change feed into per-seat state. It counts live claims
// and sales per seat rather than trusting a single status, so if two buyers
// ever hold or buy the same seat the board shows it, whichever path wrote it.
type Board struct {
	State   []byte
	held    []uint16
	sold    []uint16
	expired []bool
	pending []int32

	HoldsCreated, HoldsExpired, HoldsReleased, Orders, SeatsSold, ResoldSeats int
	FirstSale, LastSale                                                       time.Time
}

func NewBoard(seats int) *Board {
	b := &Board{State: make([]byte, seats), held: make([]uint16, seats), sold: make([]uint16, seats), expired: make([]bool, seats)}
	for i := range b.State {
		b.State[i] = Free
	}
	return b
}

func state(held, sold uint16, returned bool) byte {
	switch {
	case sold > 1 || held > 1 || (sold == 1 && held > 0):
		return Conflict
	case sold == 1:
		return Sold
	case held == 1:
		return Held
	case returned:
		return Returned
	}
	return Free
}

// Apply folds one change-feed row into the board.
func (b *Board) Apply(e store.FeedEvent) {
	switch e.Type {
	case "hold.created", "naive.held":
		b.HoldsCreated++
	case "hold.expired", "naive.expired":
		b.HoldsExpired++
	case "hold.released", "naive.released":
		b.HoldsReleased++
	case "order.created", "naive.sold":
		b.Orders++
		if b.FirstSale.IsZero() {
			b.FirstSale = e.CreatedAt
		}
		b.LastSale = e.CreatedAt
	default:
		return
	}
	for _, i := range e.Indexes {
		if i < 0 || i >= len(b.State) {
			continue
		}
		returned := false
		switch e.Type {
		case "hold.created", "naive.held":
			b.held[i]++
		case "hold.released", "naive.released":
			if b.held[i] > 0 {
				b.held[i]--
			}
		case "hold.expired", "naive.expired":
			if b.held[i] > 0 {
				b.held[i]--
			}
			b.expired[i] = true
			returned = true
		case "order.created", "naive.sold":
			if b.held[i] > 0 {
				b.held[i]--
			}
			b.sold[i]++
			b.SeatsSold++
			if b.expired[i] {
				b.ResoldSeats++
			}
		}
		next := state(b.held[i], b.sold[i], returned)
		if next != b.State[i] {
			b.State[i] = next
			b.pending = append(b.pending, int32(i), int32(next-'0'))
		}
	}
}

// Drain returns state changes since the last call as flat [index, state] pairs.
func (b *Board) Drain() []int32 {
	out := b.pending
	b.pending = nil
	return out
}

// Counts tallies seats per state.
func (b *Board) Counts() map[string]int {
	c := map[string]int{"free": 0, "held": 0, "sold": 0, "conflict": 0}
	for _, s := range b.State {
		switch s {
		case Free, Returned:
			c["free"]++
		case Held:
			c["held"]++
		case Sold:
			c["sold"]++
		case Conflict:
			c["conflict"]++
		}
	}
	return c
}

// gapWait is how long the cursor waits for a missing id before concluding its
// transaction rolled back.
const gapWait = time.Second

// Cursor consumes the change feed in strict id order. Sequence values are
// assigned when a row is inserted but become visible only at commit, so a
// lower id can appear after a higher one. The cursor stops at a gap and waits
// for it to fill; a gap that persists is a rolled-back insert and is skipped.
type Cursor struct {
	Last     int64
	gapSince time.Time
}

// Accept returns the rows that can be applied now, in order.
func (c *Cursor) Accept(rows []store.FeedEvent, now time.Time) []store.FeedEvent {
	var out []store.FeedEvent
	for _, r := range rows {
		if r.ID <= c.Last {
			continue
		}
		if r.ID != c.Last+1 {
			if c.gapSince.IsZero() {
				c.gapSince = now
			}
			if now.Sub(c.gapSince) < gapWait {
				return out
			}
		}
		c.gapSince = time.Time{}
		c.Last = r.ID
		out = append(out, r)
	}
	return out
}
