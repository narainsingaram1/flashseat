-- Backstop hits: times a seat-safety unique index rejected a write that the
-- reservation functions' own checks should already have refused. The indexes
-- keep the data correct either way, so "seats sold twice" stays 0 by
-- construction; this table is what shows whether the engine's locking and
-- checks did the work, or the last line of defense did. A safe run fails if
-- any row exists for its event.
CREATE TABLE IF NOT EXISTS backstop_hits (
  id bigserial PRIMARY KEY,
  event_id text NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  operation text NOT NULL CHECK (operation IN ('hold','checkout')),
  constraint_name text NOT NULL,
  actor text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS backstop_hits_event ON backstop_hits(event_id);
