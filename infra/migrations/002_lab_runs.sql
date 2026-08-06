CREATE TABLE IF NOT EXISTS lab_runs (
  id bigserial PRIMARY KEY,
  event_id text NOT NULL REFERENCES events(id),
  seat_id text NOT NULL REFERENCES seats(id),
  simulated_users integer NOT NULL CHECK (simulated_users BETWEEN 2 AND 1000),
  winners integer NOT NULL CHECK (winners >= 0),
  conflicts integer NOT NULL CHECK (conflicts >= 0),
  errors integer NOT NULL CHECK (errors >= 0),
  database_active_claims integer NOT NULL CHECK (database_active_claims >= 0),
  duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
  invariant_passed boolean NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS lab_runs_created_at_idx ON lab_runs(created_at DESC);
