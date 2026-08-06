package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Event struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Venue       string    `json:"venue"`
	SaleStatus  string    `json:"saleStatus"`
	AccentColor string    `json:"accentColor"`
	StartsAt    time.Time `json:"startsAt"`
}
type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	p, err := pgxpool.New(ctx, url)
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
func (s *Store) Events(ctx context.Context) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,venue,starts_at,sale_status,accent_color FROM events ORDER BY starts_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Name, &e.Venue, &e.StartsAt, &e.SaleStatus, &e.AccentColor); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) Event(ctx context.Context, id string) (Event, error) {
	var e Event
	err := s.Pool.QueryRow(ctx, `SELECT id,name,venue,starts_at,sale_status,accent_color FROM events WHERE id=$1`, id).Scan(&e.ID, &e.Name, &e.Venue, &e.StartsAt, &e.SaleStatus, &e.AccentColor)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, reservation.ErrNotFound
	}
	return e, err
}

func (s *Store) Seats(ctx context.Context, eventID, userID string) ([]reservation.Seat, error) {
	rows, err := s.Pool.Query(ctx, `SELECT s.id,sec.name,s.row_label,s.seat_number,s.price_cents,s.accessible,
	CASE WHEN oi.seat_id IS NOT NULL THEN 'sold' WHEN h.status='active' AND h.expires_at>now() THEN 'held' ELSE 'available' END,
	COALESCE(h.user_id::text=$2,false)
	FROM seats s JOIN sections sec ON sec.id=s.section_id
	LEFT JOIN order_items oi ON oi.seat_id=s.id
	LEFT JOIN hold_items hi ON hi.seat_id=s.id AND hi.released_at IS NULL
	LEFT JOIN holds h ON h.id=hi.hold_id
	WHERE s.event_id=$1 ORDER BY sec.sort_order,s.row_label,s.seat_number`, eventID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reservation.Seat
	for rows.Next() {
		var v reservation.Seat
		if err := rows.Scan(&v.ID, &v.Section, &v.Row, &v.Number, &v.PriceCents, &v.Accessible, &v.State, &v.Mine); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) HoldByKey(ctx context.Context, userID, key string) (reservation.Hold, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT id FROM holds WHERE user_id=$1 AND idempotency_key=$2`, userID, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return reservation.Hold{}, reservation.ErrNotFound
	}
	if err != nil {
		return reservation.Hold{}, err
	}
	return s.Hold(ctx, id, userID)
}
func (s *Store) CreateHold(ctx context.Context, eventID, userID, key, token string, seatIDs []string, ttl time.Duration) (reservation.Hold, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return reservation.Hold{}, err
	}
	defer tx.Rollback(ctx)
	var open bool
	err = tx.QueryRow(ctx, `SELECT sale_status='open' FROM events WHERE id=$1`, eventID).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		return reservation.Hold{}, reservation.ErrNotFound
	}
	if err != nil {
		return reservation.Hold{}, err
	}
	if !open {
		return reservation.Hold{}, fmt.Errorf("sale is not open")
	}
	rows, err := tx.Query(ctx, `SELECT id,section_id,row_label,seat_number,price_cents,accessible FROM seats WHERE event_id=$1 AND id=ANY($2) ORDER BY id FOR UPDATE`, eventID, seatIDs)
	if err != nil {
		return reservation.Hold{}, err
	}
	var seats []reservation.Seat
	for rows.Next() {
		var v reservation.Seat
		if err = rows.Scan(&v.ID, &v.Section, &v.Row, &v.Number, &v.PriceCents, &v.Accessible); err != nil {
			rows.Close()
			return reservation.Hold{}, err
		}
		seats = append(seats, v)
	}
	rows.Close()
	if len(seats) != len(seatIDs) {
		return reservation.Hold{}, fmt.Errorf("unknown seat in request")
	}
	var conflicts int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM unnest($1::text[]) x(id) WHERE EXISTS(SELECT 1 FROM order_items WHERE seat_id=x.id) OR EXISTS(SELECT 1 FROM hold_items hi JOIN holds h ON h.id=hi.hold_id WHERE hi.seat_id=x.id AND hi.released_at IS NULL AND h.status='active' AND h.expires_at>now())`, seatIDs).Scan(&conflicts)
	if err != nil {
		return reservation.Hold{}, err
	}
	if conflicts > 0 {
		return reservation.Hold{}, reservation.ErrConflict
	}
	expires := time.Now().UTC().Add(ttl)
	var holdID string
	err = tx.QueryRow(ctx, `INSERT INTO holds(event_id,user_id,status,expires_at,idempotency_key,redis_token) VALUES($1,$2,'active',$3,$4,$5) RETURNING id`, eventID, userID, expires, key, token).Scan(&holdID)
	if err != nil {
		return reservation.Hold{}, err
	}
	total := 0
	for _, seat := range seats {
		_, err = tx.Exec(ctx, `INSERT INTO hold_items(hold_id,seat_id,price_cents) VALUES($1,$2,$3)`, holdID, seat.ID, seat.PriceCents)
		if err != nil {
			if pgErr := new(pgconn.PgError); errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return reservation.Hold{}, reservation.ErrConflict
			}
			return reservation.Hold{}, err
		}
		total += seat.PriceCents
	}
	_, err = tx.Exec(ctx, `INSERT INTO reservation_events(event_id,hold_id,user_id,event_type,metadata) VALUES($1,$2,$3,'hold.created',jsonb_build_object('seat_count',$4))`, eventID, holdID, userID, len(seats))
	if err != nil {
		return reservation.Hold{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return reservation.Hold{}, err
	}
	return reservation.Hold{ID: holdID, EventID: eventID, UserID: userID, Status: "active", ExpiresAt: expires, Seats: seats, TotalCents: total}, nil
}
func (s *Store) Hold(ctx context.Context, id, userID string) (reservation.Hold, error) {
	var h reservation.Hold
	h.ID = id
	err := s.Pool.QueryRow(ctx, `SELECT event_id,user_id,status,expires_at FROM holds WHERE id=$1`, id).Scan(&h.EventID, &h.UserID, &h.Status, &h.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, reservation.ErrNotFound
	}
	if err != nil {
		return h, err
	}
	if h.UserID != userID {
		return h, reservation.ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, `SELECT s.id,sec.name,s.row_label,s.seat_number,hi.price_cents,s.accessible FROM hold_items hi JOIN seats s ON s.id=hi.seat_id JOIN sections sec ON sec.id=s.section_id WHERE hi.hold_id=$1 ORDER BY s.id`, id)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	for rows.Next() {
		var v reservation.Seat
		if err = rows.Scan(&v.ID, &v.Section, &v.Row, &v.Number, &v.PriceCents, &v.Accessible); err != nil {
			return h, err
		}
		v.State = "held"
		v.Mine = true
		h.Seats = append(h.Seats, v)
		h.TotalCents += v.PriceCents
	}
	return h, rows.Err()
}
func (s *Store) Release(ctx context.Context, id, userID, status string) (eventID, token string, seatIDs []string, err error) {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		err = e
		return
	}
	defer tx.Rollback(ctx)
	var owner, current string
	var expires time.Time
	e = tx.QueryRow(ctx, `SELECT event_id,user_id::text,status,expires_at,redis_token::text FROM holds WHERE id=$1 FOR UPDATE`, id).Scan(&eventID, &owner, &current, &expires, &token)
	if errors.Is(e, pgx.ErrNoRows) {
		err = reservation.ErrNotFound
		return
	}
	if e != nil {
		err = e
		return
	}
	if owner != userID {
		err = reservation.ErrForbidden
		return
	}
	if current != "active" {
		err = reservation.ErrConflict
		return
	}
	rows, e := tx.Query(ctx, `UPDATE hold_items SET released_at=now() WHERE hold_id=$1 AND released_at IS NULL RETURNING seat_id`, id)
	if e != nil {
		err = e
		return
	}
	for rows.Next() {
		var seat string
		_ = rows.Scan(&seat)
		seatIDs = append(seatIDs, seat)
	}
	rows.Close()
	_, e = tx.Exec(ctx, `UPDATE holds SET status=$2,updated_at=now() WHERE id=$1`, id, status)
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO reservation_events(event_id,hold_id,user_id,event_type) VALUES($1,$2,$3,$4)`, eventID, id, userID, "hold."+status)
	}
	if e != nil {
		err = e
		return
	}
	err = tx.Commit(ctx)
	return
}
func (s *Store) Checkout(ctx context.Context, holdID, userID, key string) (reservation.Order, string, []string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return reservation.Order{}, "", nil, err
	}
	defer tx.Rollback(ctx)
	var o reservation.Order
	err = tx.QueryRow(ctx, `SELECT id,reference,hold_id,user_id,total_cents,created_at FROM orders WHERE user_id=$1 AND idempotency_key=$2`, userID, key).Scan(&o.ID, &o.Reference, &o.HoldID, &o.UserID, &o.TotalCents, &o.CreatedAt)
	if err == nil {
		return o, "", nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return o, "", nil, err
	}
	var eventID, owner, status, token string
	var expires time.Time
	err = tx.QueryRow(ctx, `SELECT event_id,user_id::text,status,expires_at,redis_token::text FROM holds WHERE id=$1 FOR UPDATE`, holdID).Scan(&eventID, &owner, &status, &expires, &token)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, "", nil, reservation.ErrNotFound
	}
	if err != nil {
		return o, "", nil, err
	}
	if owner != userID {
		return o, "", nil, reservation.ErrForbidden
	}
	if status != "active" {
		return o, "", nil, reservation.ErrConflict
	}
	if reservation.IsExpired(expires, time.Now()) {
		return o, "", nil, reservation.ErrExpired
	}
	if !reservation.PaymentApproved(key) {
		return o, "", nil, fmt.Errorf("simulated payment declined")
	}
	rows, err := tx.Query(ctx, `SELECT s.id,sec.name,s.row_label,s.seat_number,hi.price_cents,s.accessible FROM hold_items hi JOIN seats s ON s.id=hi.seat_id JOIN sections sec ON sec.id=s.section_id WHERE hi.hold_id=$1 AND hi.released_at IS NULL ORDER BY s.id`, holdID)
	if err != nil {
		return o, "", nil, err
	}
	for rows.Next() {
		var v reservation.Seat
		if err = rows.Scan(&v.ID, &v.Section, &v.Row, &v.Number, &v.PriceCents, &v.Accessible); err != nil {
			rows.Close()
			return o, "", nil, err
		}
		o.Seats = append(o.Seats, v)
		o.TotalCents += v.PriceCents
	}
	rows.Close()
	o.ID = uuid.NewString()
	o.Reference = "FS-" + time.Now().UTC().Format("060102") + "-" + o.ID[:6]
	o.HoldID = holdID
	o.UserID = userID
	o.CreatedAt = time.Now().UTC()
	_, err = tx.Exec(ctx, `INSERT INTO orders(id,reference,hold_id,user_id,idempotency_key,total_cents,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, o.ID, o.Reference, holdID, userID, key, o.TotalCents, o.CreatedAt)
	if err != nil {
		return o, "", nil, err
	}
	ids := make([]string, 0, len(o.Seats))
	for _, v := range o.Seats {
		ids = append(ids, v.ID)
		_, err = tx.Exec(ctx, `INSERT INTO order_items(order_id,seat_id,section_name,row_label,seat_number,price_cents) VALUES($1,$2,$3,$4,$5,$6)`, o.ID, v.ID, v.Section, v.Row, v.Number, v.PriceCents)
		if err != nil {
			return o, "", nil, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE hold_items SET released_at=now() WHERE hold_id=$1`, holdID)
	if err != nil {
		return o, "", nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE holds SET status='purchased',updated_at=now() WHERE id=$1`, holdID)
	if err != nil {
		return o, "", nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO reservation_events(event_id,hold_id,order_id,user_id,event_type) VALUES($1,$2,$3,$4,'order.created')`, eventID, holdID, o.ID, userID)
	if err != nil {
		return o, "", nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return o, "", nil, err
	}
	return o, token, ids, nil
}
func (s *Store) Order(ctx context.Context, id, userID string) (reservation.Order, error) {
	var o reservation.Order
	err := s.Pool.QueryRow(ctx, `SELECT id,reference,hold_id,user_id,total_cents,created_at FROM orders WHERE id=$1`, id).Scan(&o.ID, &o.Reference, &o.HoldID, &o.UserID, &o.TotalCents, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, reservation.ErrNotFound
	}
	if err != nil {
		return o, err
	}
	if o.UserID != userID {
		return o, reservation.ErrForbidden
	}
	return o, nil
}
func (s *Store) ExpireBatch(ctx context.Context, limit int) ([][3]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,event_id,user_id::text,redis_token::text FROM holds WHERE status='active' AND expires_at<=now() ORDER BY expires_at FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	type rec struct{ id, event, user, token string }
	var rs []rec
	for rows.Next() {
		var r rec
		if err = rows.Scan(&r.id, &r.event, &r.user, &r.token); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	var out [][3]any
	for _, r := range rs {
		seatRows, e := tx.Query(ctx, `UPDATE hold_items SET released_at=now() WHERE hold_id=$1 AND released_at IS NULL RETURNING seat_id`, r.id)
		if e != nil {
			return nil, e
		}
		var ids []string
		for seatRows.Next() {
			var id string
			_ = seatRows.Scan(&id)
			ids = append(ids, id)
		}
		seatRows.Close()
		_, e = tx.Exec(ctx, `UPDATE holds SET status='expired',updated_at=now() WHERE id=$1`, r.id)
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO reservation_events(event_id,hold_id,user_id,event_type) VALUES($1,$2,$3,'hold.expired')`, r.event, r.id, r.user)
		if e != nil {
			return nil, e
		}
		out = append(out, [3]any{r.event, ids, r.token})
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
