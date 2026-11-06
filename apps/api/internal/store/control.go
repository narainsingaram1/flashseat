package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SaleConfig is how the control plane configures an event for one run.
type SaleConfig struct {
	EventID, Name string
	Preset        venue.Preset
	Mode          string
	HoldTTL       time.Duration
	QueueRate     int
}

// ResetEvent rebuilds an event from scratch for a new run: wipes its holds,
// orders, naive claims, and change feed, regenerates the venue, and bumps the
// inventory version so no Redis state from a previous run can be reused.
func (s *Store) ResetEvent(ctx context.Context, c SaleConfig) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO events(id,name,venue,starts_at,sale_status) VALUES($1,$2,$3,now()+interval '30 days','closed')
		ON CONFLICT (id) DO UPDATE SET name=excluded.name, venue=excluded.venue, sale_status='closed'`, c.EventID, c.Name, c.Preset.Name)
	if err != nil {
		return 0, err
	}
	for _, q := range []string{
		`DELETE FROM reservation_events WHERE event_id=$1`,
		`DELETE FROM order_items WHERE order_id IN (SELECT o.id FROM orders o JOIN holds h ON h.id=o.hold_id WHERE h.event_id=$1)`,
		`DELETE FROM orders WHERE hold_id IN (SELECT id FROM holds WHERE event_id=$1)`,
		`DELETE FROM hold_items WHERE hold_id IN (SELECT id FROM holds WHERE event_id=$1)`,
		`DELETE FROM holds WHERE event_id=$1`,
		`DELETE FROM naive_claims WHERE event_id=$1`,
		`DELETE FROM seats WHERE event_id=$1`,
		`DELETE FROM sections WHERE event_id=$1`,
	} {
		if _, err = tx.Exec(ctx, q, c.EventID); err != nil {
			return 0, fmt.Errorf("reset: %w", err)
		}
	}
	for i, sec := range c.Preset.Sections {
		_, err = tx.Exec(ctx, `INSERT INTO sections(id,event_id,name,sort_order,tier,grid_x,grid_y) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			c.EventID+"-"+sec.ID, c.EventID, sec.Name, i+1, sec.Tier, sec.GridX, sec.GridY)
		if err != nil {
			return 0, err
		}
	}
	seats := venue.Generate(c.EventID, c.Preset)
	rows := make([][]any, len(seats))
	for i, g := range seats {
		rows[i] = []any{g.ID, c.EventID, c.EventID + "-" + g.SectionID, g.Row, g.Number, g.PriceCents, false, g.Index, g.RowIndex}
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"seats"}, []string{"id", "event_id", "section_id", "row_label", "seat_number", "price_cents", "accessible", "seat_index", "row_index"}, pgx.CopyFromRows(rows))
	if err != nil {
		return 0, err
	}
	var version int64
	err = tx.QueryRow(ctx, `UPDATE events SET inventory_version=inventory_version+1, allocation_mode=$2, hold_ttl_seconds=$3,
		queue_rate=$4, queue_opened_at=NULL,
		queue_base=(SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM queue_ticket_seq), sale_status='open'
		WHERE id=$1 RETURNING inventory_version`, c.EventID, c.Mode, int(c.HoldTTL/time.Second), c.QueueRate).Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, tx.Commit(ctx)
}

// OpenQueue starts waiting-room admission for an event if it has not
// started yet, and returns when it did. The first buyer to arrive opens it.
func (s *Store) OpenQueue(ctx context.Context, eventID string) (time.Time, error) {
	var at time.Time
	err := s.Pool.QueryRow(ctx, `UPDATE events SET queue_opened_at=coalesce(queue_opened_at, now()) WHERE id=$1 RETURNING queue_opened_at`, eventID).Scan(&at)
	return at, err
}

// CloseSale stops new holds once a run is over.
func (s *Store) CloseSale(ctx context.Context, eventID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE events SET sale_status='closed' WHERE id=$1`, eventID)
	return err
}

// QueueIssued returns tickets issued since the event's queue base.
func (s *Store) QueueIssued(ctx context.Context, base int64) (int64, error) {
	var last int64
	var called bool
	if err := s.Pool.QueryRow(ctx, `SELECT last_value, is_called FROM queue_ticket_seq`).Scan(&last, &called); err != nil || !called {
		return 0, err
	}
	return max(0, last-base), nil
}

// Run is one simulation run.
type Run struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	Mode           string          `json:"mode"`
	Preset         string          `json:"preset"`
	Seats          int             `json:"seats"`
	Buyers         int             `json:"buyers"`
	ArrivalSeconds int             `json:"arrivalSeconds"`
	AdmitRate      int             `json:"admitRate"`
	HoldTTLSeconds int             `json:"holdTtlSeconds"`
	Metrics        json.RawMessage `json:"metrics"`
	Invariants     json.RawMessage `json:"invariants"`
	K6Summary      json.RawMessage `json:"k6Summary,omitempty"`
	Chaos          json.RawMessage `json:"chaos"`
	HasTimeline    bool            `json:"hasTimeline"`
	FailureMessage string          `json:"failureMessage,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	CompletedAt    *time.Time      `json:"completedAt,omitempty"`
}

const runColumns = `id::text,status,mode,preset,seats,buyers,arrival_seconds,admit_rate,hold_ttl_seconds,metrics,invariants,k6_summary,chaos,timeline IS NOT NULL,failure_message,created_at,started_at,completed_at`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.Status, &r.Mode, &r.Preset, &r.Seats, &r.Buyers, &r.ArrivalSeconds, &r.AdmitRate, &r.HoldTTLSeconds,
		&r.Metrics, &r.Invariants, &r.K6Summary, &r.Chaos, &r.HasTimeline, &r.FailureMessage, &r.CreatedAt, &r.StartedAt, &r.CompletedAt)
	return r, err
}

// ErrRunActive means another simulation is queued or running.
var ErrRunActive = errors.New("a simulation is already queued or running")

func (s *Store) CreateRun(ctx context.Context, r Run) (Run, error) {
	out, err := scanRun(s.Pool.QueryRow(ctx, `INSERT INTO simulation_runs(status,mode,preset,seats,buyers,arrival_seconds,admit_rate,hold_ttl_seconds)
		VALUES('queued',$1,$2,$3,$4,$5,$6,$7) RETURNING `+runColumns, r.Mode, r.Preset, r.Seats, r.Buyers, r.ArrivalSeconds, r.AdmitRate, r.HoldTTLSeconds))
	if pgErr := new(pgconn.PgError); errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return out, ErrRunActive
	}
	return out, err
}

// ClaimQueuedRun moves the oldest queued run to running.
func (s *Store) ClaimQueuedRun(ctx context.Context) (Run, bool, error) {
	r, err := scanRun(s.Pool.QueryRow(ctx, `UPDATE simulation_runs SET status='running', started_at=now()
		WHERE id=(SELECT id FROM simulation_runs WHERE status='queued' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+runColumns))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.Pool.QueryRow(ctx, `SELECT `+runColumns+` FROM simulation_runs WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, reservation.ErrNotFound
	}
	return r, err
}

func (s *Store) Runs(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+runColumns+` FROM simulation_runs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Timeline(ctx context.Context, id string) ([]byte, error) {
	var b []byte
	err := s.Pool.QueryRow(ctx, `SELECT timeline FROM simulation_runs WHERE id=$1`, id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && b == nil) {
		return nil, reservation.ErrNotFound
	}
	return b, err
}

// RunUpdate records progress or the final result of a run. Nil fields are kept.
type RunUpdate struct {
	Status                                string
	Seats                                 int
	Metrics, Invariants, K6Summary, Chaos json.RawMessage
	Timeline                              []byte
	Failure                               string
	Final                                 bool
}

func (s *Store) UpdateRun(ctx context.Context, id string, u RunUpdate) error {
	_, err := s.Pool.Exec(ctx, `UPDATE simulation_runs SET
		status=coalesce(nullif($2,''),status), seats=CASE WHEN $3>0 THEN $3 ELSE seats END,
		metrics=coalesce($4,metrics), invariants=coalesce($5,invariants), k6_summary=coalesce($6,k6_summary), chaos=coalesce($7,chaos),
		timeline=coalesce($8,timeline), failure_message=coalesce(nullif($9,''),failure_message),
		completed_at=CASE WHEN $10 THEN now() ELSE completed_at END
		WHERE id=$1`, id, u.Status, u.Seats, nullJSON(u.Metrics), nullJSON(u.Invariants), nullJSON(u.K6Summary), nullJSON(u.Chaos), u.Timeline, u.Failure, u.Final)
	return err
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

// FailInterruptedRuns marks runs orphaned by a control-plane restart.
func (s *Store) FailInterruptedRuns(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `UPDATE simulation_runs SET status='failed', failure_message='control plane restarted during the run', completed_at=now() WHERE status IN ('queued','running')`)
	return err
}

// FeedEvent is one row of the reservation change feed.
type FeedEvent struct {
	ID        int64
	EventID   string
	Type      string
	Indexes   []int
	Actor     string
	CreatedAt time.Time
}

// FeedAfter reads change-feed rows in id order across all events. Readers
// must see every id to detect gaps left by uncommitted transactions, so the
// event filter is applied by the caller.
func (s *Store) FeedAfter(ctx context.Context, after int64, limit int) ([]FeedEvent, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,event_id,event_type,seat_indexes,actor,created_at FROM reservation_events
		WHERE id>$1 ORDER BY id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedEvent
	for rows.Next() {
		var e FeedEvent
		var idx []int32
		if err = rows.Scan(&e.ID, &e.EventID, &e.Type, &idx, &e.Actor, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Indexes = ints(idx)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MaxFeedID is the change-feed high-water mark.
func (s *Store) MaxFeedID(ctx context.Context) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM reservation_events`).Scan(&id)
	return id, err
}

// Invariants are correctness checks computed directly from PostgreSQL.
type Invariants struct {
	CheckedAt         time.Time `json:"checkedAt"`
	DoubleSold        int       `json:"doubleSold"`        // seats sold to more than one buyer
	OverlappingClaims int       `json:"overlappingClaims"` // seats under two live claims, or a live claim on a sold seat
	StaleHolds        int       `json:"staleHolds"`        // active holds more than 5 s past expiry
	OrderTotalErrors  int       `json:"orderTotalErrors"`  // orders whose total differs from their line items
	SeatsSold         int       `json:"seatsSold"`
	SeatsHeld         int       `json:"seatsHeld"`
	Orders            int       `json:"orders"`
	RevenueCents      int64     `json:"revenueCents"`
	RedisDrift        int       `json:"redisDrift"` // seats PostgreSQL says are taken that Redis reports free
}

// Passed reports whether every safety invariant holds.
func (i Invariants) Passed() bool {
	return i.DoubleSold == 0 && i.OverlappingClaims == 0 && i.OrderTotalErrors == 0
}

func (s *Store) CheckInvariants(ctx context.Context, eventID, mode string) (Invariants, error) {
	inv := Invariants{CheckedAt: time.Now().UTC()}
	if mode == "naive" {
		err := s.Pool.QueryRow(ctx, `WITH live AS (
			SELECT seat_index, count(*) FILTER (WHERE status='sold') AS sold, count(*) FILTER (WHERE status='held' AND expires_at>now()) AS held
			  FROM naive_claims WHERE event_id=$1 GROUP BY seat_index)
			SELECT count(*) FILTER (WHERE sold>1), count(*) FILTER (WHERE held>1 OR (held>0 AND sold>0)),
			       count(*) FILTER (WHERE sold>0), count(*) FILTER (WHERE held>0 AND sold=0),
			       (SELECT count(DISTINCT hold_ref) FROM naive_claims WHERE event_id=$1 AND status='sold'),
			       (SELECT coalesce(sum(price_cents),0) FROM naive_claims WHERE event_id=$1 AND status='sold')
			FROM live`, eventID).Scan(&inv.DoubleSold, &inv.OverlappingClaims, &inv.SeatsSold, &inv.SeatsHeld, &inv.Orders, &inv.RevenueCents)
		return inv, err
	}
	err := s.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM (SELECT oi.seat_id FROM order_items oi JOIN seats s ON s.id=oi.seat_id WHERE s.event_id=$1 GROUP BY oi.seat_id HAVING count(*)>1) d),
		(SELECT count(*) FROM (SELECT hi.seat_id FROM hold_items hi JOIN seats s ON s.id=hi.seat_id WHERE s.event_id=$1 AND hi.released_at IS NULL GROUP BY hi.seat_id HAVING count(*)>1) o)
		 + (SELECT count(*) FROM hold_items hi JOIN order_items oi ON oi.seat_id=hi.seat_id JOIN seats s ON s.id=hi.seat_id WHERE s.event_id=$1 AND hi.released_at IS NULL),
		(SELECT count(*) FROM holds WHERE event_id=$1 AND status='active' AND expires_at < now() - interval '5 seconds'),
		(SELECT count(*) FROM orders o JOIN holds h ON h.id=o.hold_id WHERE h.event_id=$1
		   AND o.total_cents <> (SELECT coalesce(sum(price_cents),0) FROM order_items WHERE order_id=o.id)),
		(SELECT count(*) FROM order_items oi JOIN seats s ON s.id=oi.seat_id WHERE s.event_id=$1),
		(SELECT count(*) FROM hold_items hi JOIN holds h ON h.id=hi.hold_id WHERE h.event_id=$1 AND hi.released_at IS NULL AND h.status='active' AND h.expires_at>now()),
		(SELECT count(*) FROM orders o JOIN holds h ON h.id=o.hold_id WHERE h.event_id=$1),
		(SELECT coalesce(sum(o.total_cents),0) FROM orders o JOIN holds h ON h.id=o.hold_id WHERE h.event_id=$1)`, eventID).
		Scan(&inv.DoubleSold, &inv.OverlappingClaims, &inv.StaleHolds, &inv.OrderTotalErrors, &inv.SeatsSold, &inv.SeatsHeld, &inv.Orders, &inv.RevenueCents)
	return inv, err
}
