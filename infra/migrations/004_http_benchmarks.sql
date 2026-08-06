CREATE TABLE IF NOT EXISTS benchmark_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  status text NOT NULL CHECK (status IN ('queued','running','succeeded','failed','discarded')),
  scenario text NOT NULL CHECK (scenario IN ('same-seat-http')),
  event_id text NOT NULL REFERENCES events(id),
  seat_id text NOT NULL REFERENCES seats(id),
  virtual_users integer NOT NULL CHECK (virtual_users BETWEEN 2 AND 10000),
  requests bigint NOT NULL DEFAULT 0 CHECK (requests >= 0),
  winners bigint NOT NULL DEFAULT 0 CHECK (winners >= 0),
  conflicts bigint NOT NULL DEFAULT 0 CHECK (conflicts >= 0),
  unexpected_errors bigint NOT NULL DEFAULT 0 CHECK (unexpected_errors >= 0),
  barrier_misses bigint NOT NULL DEFAULT 0 CHECK (barrier_misses >= 0),
  throughput_rps double precision NOT NULL DEFAULT 0,
  p50_ms double precision NOT NULL DEFAULT 0,
  p95_ms double precision NOT NULL DEFAULT 0,
  p99_ms double precision NOT NULL DEFAULT 0,
  database_active_claims integer NOT NULL DEFAULT 0,
  double_bookings integer NOT NULL DEFAULT 0,
  invariant_passed boolean NOT NULL DEFAULT false,
  k6_version text NOT NULL DEFAULT '',
  target_url text NOT NULL DEFAULT '',
  raw_summary jsonb NOT NULL DEFAULT '{}',
  failure_message text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz
);

CREATE INDEX IF NOT EXISTS benchmark_jobs_created_at_idx ON benchmark_jobs(created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS one_running_benchmark
  ON benchmark_jobs ((true)) WHERE status IN ('queued','running');
