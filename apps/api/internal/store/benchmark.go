package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/reservation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type BenchmarkJob struct {
	ID                   string          `json:"id"`
	Status               string          `json:"status"`
	Scenario             string          `json:"scenario"`
	EventID              string          `json:"eventId"`
	SeatID               string          `json:"seatId"`
	VirtualUsers         int             `json:"virtualUsers"`
	Requests             int64           `json:"requests"`
	Winners              int64           `json:"winners"`
	Conflicts            int64           `json:"conflicts"`
	UnexpectedErrors     int64           `json:"unexpectedErrors"`
	BarrierMisses        int64           `json:"barrierMisses"`
	ThroughputRPS        float64         `json:"throughputRps"`
	P50MS                float64         `json:"p50Ms"`
	P95MS                float64         `json:"p95Ms"`
	P99MS                float64         `json:"p99Ms"`
	DatabaseActiveClaims int             `json:"databaseActiveClaims"`
	DoubleBookings       int             `json:"doubleBookings"`
	InvariantPassed      bool            `json:"invariantPassed"`
	K6Version            string          `json:"k6Version"`
	TargetURL            string          `json:"targetUrl"`
	RawSummary           json.RawMessage `json:"rawSummary"`
	FailureMessage       string          `json:"failureMessage,omitempty"`
	CreatedAt            time.Time       `json:"createdAt"`
	StartedAt            *time.Time      `json:"startedAt,omitempty"`
	CompletedAt          *time.Time      `json:"completedAt,omitempty"`
}

type BenchmarkResults struct {
	TotalRuns         int64          `json:"totalRuns"`
	TotalRequests     int64          `json:"totalRequests"`
	TotalConflicts    int64          `json:"totalConflicts"`
	TotalErrors       int64          `json:"totalErrors"`
	PassedRuns        int64          `json:"passedRuns"`
	DoubleBookings    int64          `json:"doubleBookings"`
	AverageP99MS      float64        `json:"averageP99Ms"`
	PeakThroughputRPS float64        `json:"peakThroughputRps"`
	MaxVirtualUsers   int            `json:"maxVirtualUsers"`
	Recent            []BenchmarkJob `json:"recent"`
}

type BenchmarkMetrics struct {
	Requests, Winners, Conflicts, UnexpectedErrors, BarrierMisses int64
	ThroughputRPS, P50MS, P95MS, P99MS                            float64
	DatabaseActiveClaims, DoubleBookings                          int
	InvariantPassed                                               bool
	K6Version, TargetURL                                          string
	RawSummary                                                    json.RawMessage
}

type ReleasedLease struct {
	EventID, Token string
	SeatIDs        []string
}

const benchmarkColumns = `id::text,status,scenario,event_id,seat_id,virtual_users,requests,winners,conflicts,unexpected_errors,barrier_misses,throughput_rps,p50_ms,p95_ms,p99_ms,database_active_claims,double_bookings,invariant_passed,k6_version,target_url,raw_summary,failure_message,created_at,started_at,completed_at`

func scanBenchmark(row pgx.Row) (BenchmarkJob, error) {
	var j BenchmarkJob
	err := row.Scan(&j.ID, &j.Status, &j.Scenario, &j.EventID, &j.SeatID, &j.VirtualUsers, &j.Requests, &j.Winners, &j.Conflicts, &j.UnexpectedErrors, &j.BarrierMisses, &j.ThroughputRPS, &j.P50MS, &j.P95MS, &j.P99MS, &j.DatabaseActiveClaims, &j.DoubleBookings, &j.InvariantPassed, &j.K6Version, &j.TargetURL, &j.RawSummary, &j.FailureMessage, &j.CreatedAt, &j.StartedAt, &j.CompletedAt)
	return j, err
}

func (s *Store) CreateBenchmark(ctx context.Context, eventID, seatID string, users int) (BenchmarkJob, error) {
	j, err := scanBenchmark(s.Pool.QueryRow(ctx, `INSERT INTO benchmark_jobs(status,scenario,event_id,seat_id,virtual_users) VALUES('queued','same-seat-http',$1,$2,$3) RETURNING `+benchmarkColumns, eventID, seatID, users))
	if pgErr := new(pgconn.PgError); errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return j, reservation.ErrConflict
	}
	return j, err
}

func (s *Store) Benchmark(ctx context.Context, id string) (BenchmarkJob, error) {
	j, err := scanBenchmark(s.Pool.QueryRow(ctx, `SELECT `+benchmarkColumns+` FROM benchmark_jobs WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return j, reservation.ErrNotFound
	}
	return j, err
}

func (s *Store) StartBenchmark(ctx context.Context, id, target string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE benchmark_jobs SET status='running',started_at=now(),target_url=$2 WHERE id=$1 AND status='queued'`, id, target)
	return err
}

func (s *Store) CompleteBenchmark(ctx context.Context, id string, m BenchmarkMetrics) error {
	_, err := s.Pool.Exec(ctx, `UPDATE benchmark_jobs SET status='succeeded',requests=$2,winners=$3,conflicts=$4,unexpected_errors=$5,barrier_misses=$6,throughput_rps=$7,p50_ms=$8,p95_ms=$9,p99_ms=$10,database_active_claims=$11,double_bookings=$12,invariant_passed=$13,k6_version=$14,target_url=$15,raw_summary=$16,completed_at=now() WHERE id=$1`, id, m.Requests, m.Winners, m.Conflicts, m.UnexpectedErrors, m.BarrierMisses, m.ThroughputRPS, m.P50MS, m.P95MS, m.P99MS, m.DatabaseActiveClaims, m.DoubleBookings, m.InvariantPassed, m.K6Version, m.TargetURL, m.RawSummary)
	return err
}

func (s *Store) FailBenchmark(ctx context.Context, id, message string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE benchmark_jobs SET status='failed',failure_message=$2,completed_at=now() WHERE id=$1`, id, message)
	return err
}

func (s *Store) FailInterruptedBenchmarks(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `UPDATE benchmark_jobs SET status='failed',failure_message='API restarted before the benchmark completed',completed_at=now() WHERE status IN ('queued','running')`)
	return err
}

func (s *Store) BenchmarkResults(ctx context.Context) (BenchmarkResults, error) {
	out := BenchmarkResults{Recent: []BenchmarkJob{}}
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status<>'discarded'),COALESCE(sum(requests) FILTER (WHERE status<>'discarded'),0),COALESCE(sum(conflicts) FILTER (WHERE status<>'discarded'),0),COALESCE(sum(unexpected_errors) FILTER (WHERE status<>'discarded'),0),count(*) FILTER (WHERE invariant_passed AND status<>'discarded'),COALESCE(sum(double_bookings) FILTER (WHERE status<>'discarded'),0),COALESCE(avg(p99_ms) FILTER (WHERE status='succeeded'),0),COALESCE(max(throughput_rps) FILTER (WHERE status<>'discarded'),0),COALESCE(max(virtual_users) FILTER (WHERE status<>'discarded'),0) FROM benchmark_jobs`).Scan(&out.TotalRuns, &out.TotalRequests, &out.TotalConflicts, &out.TotalErrors, &out.PassedRuns, &out.DoubleBookings, &out.AverageP99MS, &out.PeakThroughputRPS, &out.MaxVirtualUsers)
	if err != nil {
		return out, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+benchmarkColumns+` FROM benchmark_jobs ORDER BY created_at DESC LIMIT 30`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		j, scanErr := scanBenchmark(rows)
		if scanErr != nil {
			return out, scanErr
		}
		out.Recent = append(out.Recent, j)
	}
	return out, rows.Err()
}

func (s *Store) ReleaseBenchmarkHolds(ctx context.Context, benchmarkID string) ([]ReleasedLease, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,event_id,redis_token::text FROM holds WHERE status='active' AND idempotency_key LIKE $1 ORDER BY id FOR UPDATE`, "bench-"+benchmarkID+"-%")
	if err != nil {
		return nil, err
	}
	type hold struct{ id, event, token string }
	var holds []hold
	for rows.Next() {
		var h hold
		if err = rows.Scan(&h.id, &h.event, &h.token); err != nil {
			rows.Close()
			return nil, err
		}
		holds = append(holds, h)
	}
	rows.Close()
	var released []ReleasedLease
	for _, h := range holds {
		seatRows, e := tx.Query(ctx, `UPDATE hold_items SET released_at=now() WHERE hold_id=$1 AND released_at IS NULL RETURNING seat_id`, h.id)
		if e != nil {
			return nil, e
		}
		lease := ReleasedLease{EventID: h.event, Token: h.token}
		for seatRows.Next() {
			var id string
			if e = seatRows.Scan(&id); e != nil {
				seatRows.Close()
				return nil, e
			}
			lease.SeatIDs = append(lease.SeatIDs, id)
		}
		seatRows.Close()
		if _, e = tx.Exec(ctx, `UPDATE holds SET status='released',updated_at=now() WHERE id=$1`, h.id); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO reservation_events(event_id,hold_id,user_id,event_type,metadata) SELECT event_id,id,user_id,'hold.released',jsonb_build_object('source','http_benchmark') FROM holds WHERE id=$1`, h.id); e != nil {
			return nil, e
		}
		released = append(released, lease)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return released, nil
}
