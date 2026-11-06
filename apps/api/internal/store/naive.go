package store

// The naive path is the comparison baseline: a typical CRUD implementation that
// reads availability, decides in application code, and then writes, with no
// transaction, no row locks, and no uniqueness constraint. Each statement is
// individually correct; the race lives in the gap between them. It is written
// in plain Go on purpose so the difference from the safe path is easy to read.

import (
	"context"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/google/uuid"
)

// NaiveTaken returns seats the naive path believes are unavailable.
func (s *Store) NaiveTaken(ctx context.Context, eventID string) ([]int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT seat_index FROM naive_claims
		WHERE event_id=$1 AND (status='sold' OR (status='held' AND expires_at>now()))`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var i int
		if err = rows.Scan(&i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// NaiveHold writes claims for seats the caller already checked were free.
func (s *Store) NaiveHold(ctx context.Context, eventID, userID string, seats []reservation.Seat, ttl time.Duration, actor string) (HoldResult, error) {
	ref := uuid.NewString()
	expires := time.Now().UTC().Add(ttl)
	idx := make([]int, len(seats))
	prices := make([]int, len(seats))
	for i, seat := range seats {
		idx[i], prices[i] = seat.Index, seat.PriceCents
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO naive_claims(event_id,hold_ref,user_id,seat_index,price_cents,status,expires_at)
		SELECT $1,$2,$3,i,p,'held',$4 FROM unnest($5::int[],$6::int[]) AS t(i,p)`, eventID, ref, userID, expires, idx, prices)
	if err != nil {
		return HoldResult{}, err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO reservation_events(event_id,hold_id,user_id,event_type,seat_indexes,actor) VALUES($1,$2,$3,'naive.held',$4,$5)`,
		eventID, ref, userID, idx, actor)
	return HoldResult{ID: ref, ExpiresAt: expires, Indexes: idx}, err
}

func (s *Store) NaiveCheckout(ctx context.Context, holdRef, userID, actor string) (CheckoutResult, error) {
	rows, err := s.Pool.Query(ctx, `UPDATE naive_claims SET status='sold'
		WHERE hold_ref=$1 AND user_id=$2 AND status='held' AND expires_at>now() RETURNING seat_index, price_cents, event_id`, holdRef, userID)
	if err != nil {
		return CheckoutResult{}, err
	}
	var r CheckoutResult
	var eventID string
	for rows.Next() {
		var i, p int
		if err = rows.Scan(&i, &p, &eventID); err != nil {
			rows.Close()
			return r, err
		}
		r.Indexes = append(r.Indexes, i)
		r.TotalCents += p
	}
	rows.Close()
	if len(r.Indexes) == 0 {
		return r, reservation.ErrExpired
	}
	r.OrderID = uuid.NewString()
	r.Reference = "NV-" + r.OrderID[:8]
	r.EventID = eventID
	r.CreatedAt = time.Now().UTC()
	_, err = s.Pool.Exec(ctx, `INSERT INTO reservation_events(event_id,hold_id,user_id,event_type,seat_indexes,actor) VALUES($1,$2,$3,'naive.sold',$4,$5)`,
		eventID, holdRef, userID, r.Indexes, actor)
	return r, err
}

func (s *Store) NaiveRelease(ctx context.Context, holdRef, userID, actor string) ([]int, error) {
	rows, err := s.Pool.Query(ctx, `UPDATE naive_claims SET status='released'
		WHERE hold_ref=$1 AND user_id=$2 AND status='held' RETURNING seat_index, event_id`, holdRef, userID)
	if err != nil {
		return nil, err
	}
	var idx []int
	var eventID string
	for rows.Next() {
		var i int
		if err = rows.Scan(&i, &eventID); err != nil {
			rows.Close()
			return nil, err
		}
		idx = append(idx, i)
	}
	rows.Close()
	if len(idx) == 0 {
		return nil, reservation.ErrNotFound
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO reservation_events(event_id,hold_id,user_id,event_type,seat_indexes,actor) VALUES($1,$2,$3,'naive.released',$4,$5)`,
		eventID, holdRef, userID, idx, actor)
	return idx, err
}

// NaiveExpireDue expires lapsed naive claims and logs them for the change feed.
func (s *Store) NaiveExpireDue(ctx context.Context) (int, error) {
	tag, err := s.Pool.Exec(ctx, `WITH expired AS (
		UPDATE naive_claims SET status='expired' WHERE status='held' AND expires_at<=now()
		RETURNING event_id, hold_ref, user_id, seat_index)
		INSERT INTO reservation_events(event_id,hold_id,user_id,event_type,seat_indexes)
		SELECT event_id, hold_ref, user_id, 'naive.expired', array_agg(seat_index ORDER BY seat_index) FROM expired GROUP BY event_id, hold_ref, user_id`)
	return int(tag.RowsAffected()), err
}
