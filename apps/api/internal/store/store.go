// Package store is the PostgreSQL authority. Reservation writes go through
// PL/pgSQL functions (infra/migrations/007_onsale_engine.sql) so each hold,
// checkout, or release is one network round trip.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/inventory"
	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
		cfg.MinConns = min(maxConns, 4)
	}
	cfg.MaxConnIdleTime = 5 * time.Minute
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &Store{Pool: p}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// mapErr turns the reservation functions' SQLSTATEs into domain errors.
func mapErr(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		if errors.Is(err, pgx.ErrNoRows) {
			return reservation.ErrNotFound
		}
		return err
	}
	switch pgErr.Code {
	case "FS404":
		return reservation.ErrNotFound
	case "FS403":
		return reservation.ErrForbidden
	case "FS409":
		return reservation.ErrConflict
	case "FS410":
		return reservation.ErrExpired
	case "FS422":
		return reservation.ErrInvalidSeats
	case "FS423":
		return reservation.ErrSaleClosed
	case "FS429":
		return reservation.ErrHoldLimit
	case "FS402":
		return reservation.ErrPaymentDeclined
	case "23505":
		switch pgErr.ConstraintName {
		case "one_active_hold_per_user":
			return reservation.ErrHoldLimit
		case "holds_user_id_idempotency_key_key", "orders_user_id_idempotency_key_key":
			return ErrReplayRace
		}
		return reservation.ErrConflict
	}
	return err
}

// seatBackstops are the unique indexes that keep a seat from being held or
// sold twice. The reservation functions check for both conditions before
// writing, so hitting one of these means those checks let a collision through.
var seatBackstops = map[string]bool{
	"one_live_hold_per_seat":  true, // hold_items: one live hold per seat
	"order_items_seat_id_key": true, // order_items: each seat sold once
}

// backstopConstraint returns the seat-safety index err violated, if any.
func backstopConstraint(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && seatBackstops[pgErr.ConstraintName] {
		return pgErr.ConstraintName, true
	}
	return "", false
}

// recordBackstop durably logs a backstop hit. It runs on its own short
// context so a buyer cancelling the request cannot erase the evidence. The
// query names the event directly for holds, or through the hold for checkout.
func (s *Store) recordBackstop(ctx context.Context, err error, op, eventSQL string, eventArg any, actor string) error {
	name, ok := backstopConstraint(err)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, recErr := s.Pool.Exec(ctx, `INSERT INTO backstop_hits(event_id,operation,constraint_name,actor) SELECT `+eventSQL+`,$2,$3,$4`,
		eventArg, op, name, actor)
	if recErr != nil {
		return fmt.Errorf("record backstop hit on %s: %w", name, recErr)
	}
	return nil
}

// ErrReplayRace means a concurrent request with the same idempotency key won;
// retrying the call returns its result.
var ErrReplayRace = errors.New("concurrent request with the same idempotency key")

// EventMeta is the per-event configuration every request needs.
type EventMeta struct {
	ID, Name, Venue, SaleStatus, Mode string
	StartsAt                          time.Time
	HoldTTL                           time.Duration
	QueueRate                         int
	QueueOpenedAt                     *time.Time
	QueueBase                         int64
	Version                           int64
}

const metaColumns = `id,name,venue,starts_at,sale_status,allocation_mode,hold_ttl_seconds,queue_rate,queue_opened_at,queue_base,inventory_version`

func scanMeta(row pgx.Row) (EventMeta, error) {
	var m EventMeta
	var ttl int
	err := row.Scan(&m.ID, &m.Name, &m.Venue, &m.StartsAt, &m.SaleStatus, &m.Mode, &ttl, &m.QueueRate, &m.QueueOpenedAt, &m.QueueBase, &m.Version)
	m.HoldTTL = time.Duration(ttl) * time.Second
	return m, err
}

func (s *Store) EventMeta(ctx context.Context, id string) (EventMeta, error) {
	m, err := scanMeta(s.Pool.QueryRow(ctx, `SELECT `+metaColumns+` FROM events WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return m, reservation.ErrNotFound
	}
	return m, err
}

func (s *Store) Events(ctx context.Context) ([]EventMeta, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+metaColumns+` FROM events ORDER BY starts_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventMeta
	for rows.Next() {
		m, err := scanMeta(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Layout loads an event's seats in bitmap order.
func (s *Store) Layout(ctx context.Context, eventID string) ([]venue.LayoutSeat, error) {
	rows, err := s.Pool.Query(ctx, `SELECT s.id,s.seat_index,sec.name,s.row_label,s.seat_number,s.price_cents,sec.id,sec.tier,s.row_index,sec.grid_x,sec.grid_y
		FROM seats s JOIN sections sec ON sec.id=s.section_id WHERE s.event_id=$1 ORDER BY s.seat_index`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []venue.LayoutSeat
	for rows.Next() {
		var l venue.LayoutSeat
		if err := rows.Scan(&l.ID, &l.Index, &l.Section, &l.Row, &l.Number, &l.PriceCents, &l.SectionID, &l.Tier, &l.RowIndex, &l.GridX, &l.GridY); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// TakenIndexes returns seats that are sold or under a live hold.
func (s *Store) TakenIndexes(ctx context.Context, eventID string) (sold, held []int, err error) {
	rows, err := s.Pool.Query(ctx, `SELECT s.seat_index, true FROM order_items oi JOIN seats s ON s.id=oi.seat_id WHERE s.event_id=$1
		UNION ALL
		SELECT s.seat_index, false FROM hold_items hi JOIN holds h ON h.id=hi.hold_id JOIN seats s ON s.id=hi.seat_id
		 WHERE h.event_id=$1 AND hi.released_at IS NULL AND h.status='active' AND h.expires_at>now()`, eventID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int
		var isSold bool
		if err = rows.Scan(&i, &isSold); err != nil {
			return nil, nil, err
		}
		if isSold {
			sold = append(sold, i)
		} else {
			held = append(held, i)
		}
	}
	return sold, held, rows.Err()
}

// InventorySnapshot is PostgreSQL's view used to (re)build the Redis bitmap.
func (s *Store) InventorySnapshot(ctx context.Context, eventID string) (inventory.Snapshot, error) {
	var snap inventory.Snapshot
	sold, _, err := s.TakenIndexes(ctx, eventID)
	if err != nil {
		return snap, err
	}
	snap.Sold = sold
	rows, err := s.Pool.Query(ctx, `SELECT h.redis_token::text, h.expires_at, array_agg(s.seat_index ORDER BY s.seat_index)
		FROM holds h JOIN hold_items hi ON hi.hold_id=h.id AND hi.released_at IS NULL JOIN seats s ON s.id=hi.seat_id
		WHERE h.event_id=$1 AND h.status='active' AND h.expires_at>now() GROUP BY h.id`, eventID)
	if err != nil {
		return snap, err
	}
	defer rows.Close()
	for rows.Next() {
		var c inventory.LiveClaim
		var idx []int32
		if err = rows.Scan(&c.Token, &c.ExpiresAt, &idx); err != nil {
			return snap, err
		}
		c.Indexes = ints(idx)
		snap.Claims = append(snap.Claims, c)
	}
	return snap, rows.Err()
}

func ints(v []int32) []int {
	out := make([]int, len(v))
	for i, x := range v {
		out[i] = int(x)
	}
	return out
}

// HoldResult is what fs_create_hold returns.
type HoldResult struct {
	ID        string
	Replayed  bool
	ExpiresAt time.Time
	Indexes   []int
}

func (s *Store) CreateHold(ctx context.Context, eventID, userID, key, token string, indexes []int, actor string) (HoldResult, error) {
	var r HoldResult
	var idx []int32
	err := s.Pool.QueryRow(ctx, `SELECT o_hold::text,o_replayed,o_expires,o_indexes FROM fs_create_hold($1,$2,$3,$4,$5,$6)`,
		eventID, userID, key, token, indexes, actor).Scan(&r.ID, &r.Replayed, &r.ExpiresAt, &idx)
	r.Indexes = ints(idx)
	if recErr := s.recordBackstop(ctx, err, "hold", "$1::text", eventID, actor); recErr != nil {
		return r, recErr
	}
	return r, mapErr(err)
}

// CheckoutResult is what fs_checkout returns.
type CheckoutResult struct {
	OrderID, Reference, EventID, Token string
	TotalCents                         int
	CreatedAt                          time.Time
	Replayed                           bool
	Indexes                            []int
}

func (s *Store) Checkout(ctx context.Context, holdID, userID, key, actor string) (CheckoutResult, error) {
	var r CheckoutResult
	var idx []int32
	err := s.Pool.QueryRow(ctx, `SELECT o_order::text,o_reference,o_total,o_created,o_replayed,o_event,o_token::text,o_indexes FROM fs_checkout($1,$2,$3,$4)`,
		holdID, userID, key, actor).Scan(&r.OrderID, &r.Reference, &r.TotalCents, &r.CreatedAt, &r.Replayed, &r.EventID, &r.Token, &idx)
	r.Indexes = ints(idx)
	if recErr := s.recordBackstop(ctx, err, "checkout", "(SELECT event_id FROM holds WHERE id=$1::uuid)", holdID, actor); recErr != nil {
		return r, recErr
	}
	return r, mapErr(err)
}

// Release returns the event, token, and seats of a buyer-released hold.
func (s *Store) Release(ctx context.Context, holdID, userID, actor string) (string, string, []int, error) {
	var event, token string
	var idx []int32
	err := s.Pool.QueryRow(ctx, `SELECT o_event,o_token::text,o_indexes FROM fs_release($1,$2,$3)`, holdID, userID, actor).Scan(&event, &token, &idx)
	return event, token, ints(idx), mapErr(err)
}

// HoldView is a stored hold with its seats as bitmap indexes.
type HoldView struct {
	ID, EventID, Status string
	ExpiresAt           time.Time
	Indexes             []int
}

func (s *Store) Hold(ctx context.Context, id, userID string) (HoldView, error) {
	var h HoldView
	var owner string
	var idx []int32
	err := s.Pool.QueryRow(ctx, `SELECT h.id::text,h.event_id,h.status,h.expires_at,h.user_id::text,
		coalesce((SELECT array_agg(s.seat_index ORDER BY s.seat_index) FROM hold_items hi JOIN seats s ON s.id=hi.seat_id WHERE hi.hold_id=h.id),'{}')
		FROM holds h WHERE h.id=$1`, id).Scan(&h.ID, &h.EventID, &h.Status, &h.ExpiresAt, &owner, &idx)
	if err != nil {
		return h, mapErr(err)
	}
	if owner != userID {
		return h, reservation.ErrForbidden
	}
	h.Indexes = ints(idx)
	return h, nil
}

// OrderView is a stored order.
type OrderView struct {
	ID, Reference, HoldID, EventID string
	TotalCents                     int
	CreatedAt                      time.Time
	Indexes                        []int
}

func (s *Store) Order(ctx context.Context, id, userID string) (OrderView, error) {
	var o OrderView
	var owner string
	var idx []int32
	err := s.Pool.QueryRow(ctx, `SELECT o.id::text,o.reference,o.hold_id::text,h.event_id,o.total_cents,o.created_at,o.user_id::text,
		coalesce((SELECT array_agg(s.seat_index ORDER BY s.seat_index) FROM order_items oi JOIN seats s ON s.id=oi.seat_id WHERE oi.order_id=o.id),'{}')
		FROM orders o JOIN holds h ON h.id=o.hold_id WHERE o.id=$1`, id).Scan(&o.ID, &o.Reference, &o.HoldID, &o.EventID, &o.TotalCents, &o.CreatedAt, &owner, &idx)
	if err != nil {
		return o, mapErr(err)
	}
	if owner != userID {
		return o, reservation.ErrForbidden
	}
	o.Indexes = ints(idx)
	return o, nil
}

// NextTicket issues a waiting-room ticket number.
func (s *Store) NextTicket(ctx context.Context) (int64, error) {
	var n int64
	err := s.Pool.QueryRow(ctx, `SELECT nextval('queue_ticket_seq')`).Scan(&n)
	return n, err
}

// ExpiredClaim is one hold released by the sweeper.
type ExpiredClaim struct {
	EventID, Token string
	Indexes        []int
}

// ExpireDue expires up to limit past-due holds. Safe to run from any number
// of processes: rows are claimed with FOR UPDATE SKIP LOCKED.
func (s *Store) ExpireDue(ctx context.Context, limit int) ([]ExpiredClaim, error) {
	rows, err := s.Pool.Query(ctx, `SELECT o_event,o_token::text,o_indexes FROM fs_expire_due($1)`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExpiredClaim
	for rows.Next() {
		var c ExpiredClaim
		var idx []int32
		if err = rows.Scan(&c.EventID, &c.Token, &idx); err != nil {
			return nil, err
		}
		c.Indexes = ints(idx)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) String() string { return fmt.Sprintf("store(%d conns)", s.Pool.Stat().TotalConns()) }

// OpenWithRetry keeps trying to connect until ctx ends, so a service that
// starts before its database (or the proxy in front of it) does not exit.
func OpenWithRetry(ctx context.Context, url string, maxConns int32, log func(error)) (*Store, error) {
	delay := 250 * time.Millisecond
	for {
		s, err := Open(ctx, url, maxConns)
		if err == nil {
			return s, nil
		}
		log(err)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(delay):
		}
		delay = min(delay*2, 3*time.Second)
	}
}
