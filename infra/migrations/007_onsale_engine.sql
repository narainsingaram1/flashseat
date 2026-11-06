-- On-sale engine: venue geometry, per-event sale configuration, single-round-trip
-- reservation functions, the deliberately unsafe comparison table, and durable
-- simulation runs.

-- Per-event sale configuration. inventory_version namespaces Redis keys so a
-- venue reset never shares state with the previous run.
ALTER TABLE events ADD COLUMN IF NOT EXISTS hold_ttl_seconds integer NOT NULL DEFAULT 600 CHECK (hold_ttl_seconds BETWEEN 3 AND 3600);
ALTER TABLE events ADD COLUMN IF NOT EXISTS allocation_mode text NOT NULL DEFAULT 'safe' CHECK (allocation_mode IN ('safe','naive'));
ALTER TABLE events ADD COLUMN IF NOT EXISTS queue_rate integer NOT NULL DEFAULT 0 CHECK (queue_rate >= 0);
ALTER TABLE events ADD COLUMN IF NOT EXISTS queue_opened_at timestamptz;
ALTER TABLE events ADD COLUMN IF NOT EXISTS queue_base bigint NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN IF NOT EXISTS inventory_version bigint NOT NULL DEFAULT 1;

-- Venue geometry, in seat units, so clients can draw the real map.
ALTER TABLE sections ADD COLUMN IF NOT EXISTS tier integer NOT NULL DEFAULT 1;
ALTER TABLE sections ADD COLUMN IF NOT EXISTS grid_x integer NOT NULL DEFAULT 0;
ALTER TABLE sections ADD COLUMN IF NOT EXISTS grid_y integer NOT NULL DEFAULT 0;

-- seat_index is the seat's bit position in the Redis inventory bitmap.
ALTER TABLE seats ADD COLUMN IF NOT EXISTS seat_index integer;
ALTER TABLE seats ADD COLUMN IF NOT EXISTS row_index integer;
WITH ordered AS (
  SELECT s.id,
         row_number() OVER (PARTITION BY s.event_id ORDER BY sec.sort_order, s.row_label, s.seat_number) - 1 AS idx,
         dense_rank() OVER (PARTITION BY s.section_id ORDER BY s.row_label) - 1 AS row_idx
  FROM seats s JOIN sections sec ON sec.id = s.section_id
)
UPDATE seats s SET seat_index = o.idx, row_index = o.row_idx FROM ordered o WHERE o.id = s.id AND s.seat_index IS NULL;
UPDATE sections SET grid_y = (sort_order - 1) * 6 WHERE event_id = 'aurora-2026';
ALTER TABLE seats ALTER COLUMN seat_index SET NOT NULL;
ALTER TABLE seats ALTER COLUMN row_index SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS seats_event_seat_index ON seats(event_id, seat_index);
CREATE INDEX IF NOT EXISTS hold_items_live_seat ON hold_items(seat_id) WHERE released_at IS NULL;

-- One active hold per buyer per event (anti-hoarding).
CREATE UNIQUE INDEX IF NOT EXISTS one_active_hold_per_user ON holds(event_id, user_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS holds_event_status ON holds(event_id, status);

-- The audit log doubles as the change feed for the live seat map and replays.
ALTER TABLE reservation_events ADD COLUMN IF NOT EXISTS seat_indexes integer[] NOT NULL DEFAULT '{}';
ALTER TABLE reservation_events ADD COLUMN IF NOT EXISTS actor text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS reservation_events_event_id ON reservation_events(event_id, id);

-- Waiting-room tickets come from a sequence so the queue survives Redis loss.
CREATE SEQUENCE IF NOT EXISTS queue_ticket_seq;

-- Deliberately unsafe comparison path: the classic check-then-insert design
-- with no row locks, no transaction, and no uniqueness constraint. It exists
-- only so the simulator can show what the safe path prevents.
CREATE TABLE IF NOT EXISTS naive_claims (
  id bigserial PRIMARY KEY,
  event_id text NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  hold_ref uuid NOT NULL,
  user_id uuid NOT NULL,
  seat_index integer NOT NULL,
  price_cents integer NOT NULL,
  status text NOT NULL CHECK (status IN ('held','sold','released','expired')),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS naive_claims_event_seat ON naive_claims(event_id, seat_index);
CREATE INDEX IF NOT EXISTS naive_claims_hold ON naive_claims(hold_ref);

CREATE TABLE IF NOT EXISTS simulation_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  status text NOT NULL CHECK (status IN ('queued','running','succeeded','failed')),
  mode text NOT NULL CHECK (mode IN ('safe','naive')),
  preset text NOT NULL,
  seats integer NOT NULL DEFAULT 0,
  buyers integer NOT NULL CHECK (buyers BETWEEN 10 AND 20000),
  arrival_seconds integer NOT NULL CHECK (arrival_seconds BETWEEN 1 AND 300),
  admit_rate integer NOT NULL CHECK (admit_rate >= 0),
  hold_ttl_seconds integer NOT NULL CHECK (hold_ttl_seconds BETWEEN 3 AND 600),
  metrics jsonb NOT NULL DEFAULT '{}',
  invariants jsonb NOT NULL DEFAULT '{}',
  k6_summary jsonb NOT NULL DEFAULT '{}',
  chaos jsonb NOT NULL DEFAULT '[]',
  timeline bytea,
  failure_message text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS simulation_runs_created ON simulation_runs(created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_simulation ON simulation_runs((true)) WHERE status IN ('queued','running');

-- Expires one hold if it is still active and past due. Shared by the lazy
-- hot-path expiry and the background sweeper.
CREATE OR REPLACE FUNCTION fs_expire_hold(p_hold uuid)
RETURNS TABLE(o_event text, o_token uuid, o_indexes integer[])
LANGUAGE plpgsql AS $$
DECLARE
  v_event text; v_user uuid; v_token uuid; v_indexes integer[];
BEGIN
  UPDATE holds SET status = 'expired', updated_at = now()
   WHERE id = p_hold AND status = 'active' AND expires_at <= now()
  RETURNING event_id, user_id, redis_token INTO v_event, v_user, v_token;
  IF NOT FOUND THEN RETURN; END IF;
  WITH released AS (
    UPDATE hold_items SET released_at = now()
     WHERE hold_id = p_hold AND released_at IS NULL
    RETURNING seat_id
  )
  SELECT coalesce(array_agg(s.seat_index ORDER BY s.seat_index), '{}') INTO v_indexes
    FROM released r JOIN seats s ON s.id = r.seat_id;
  INSERT INTO reservation_events(event_id, hold_id, user_id, event_type, seat_indexes)
  VALUES (v_event, p_hold, v_user, 'hold.expired', v_indexes);
  RETURN QUERY SELECT v_event, v_token, v_indexes;
END $$;

CREATE OR REPLACE FUNCTION fs_expire_due(p_limit integer)
RETURNS TABLE(o_event text, o_token uuid, o_indexes integer[])
LANGUAGE plpgsql AS $$
DECLARE r record;
BEGIN
  FOR r IN SELECT id FROM holds
            WHERE status = 'active' AND expires_at <= now()
            ORDER BY expires_at LIMIT p_limit
            FOR UPDATE SKIP LOCKED
  LOOP
    RETURN QUERY SELECT * FROM fs_expire_hold(r.id);
  END LOOP;
END $$;

-- Creates a hold in one round trip. Errors use custom SQLSTATEs:
-- FS404 unknown event, FS423 sale closed, FS422 unknown seat,
-- FS409 seat taken, FS429 buyer already holds seats.
CREATE OR REPLACE FUNCTION fs_create_hold(p_event text, p_user uuid, p_key text, p_token uuid, p_indexes integer[], p_actor text)
RETURNS TABLE(o_hold uuid, o_replayed boolean, o_expires timestamptz, o_indexes integer[])
LANGUAGE plpgsql AS $$
DECLARE
  v_hold uuid; v_status text; v_ttl integer; v_seats text[]; v_expires timestamptz; v_sorted integer[]; r record;
BEGIN
  SELECT h.id INTO v_hold FROM holds h WHERE h.user_id = p_user AND h.idempotency_key = p_key;
  IF FOUND THEN
    RETURN QUERY
      SELECT h.id, true, h.expires_at,
             (SELECT coalesce(array_agg(s.seat_index ORDER BY s.seat_index), '{}')
                FROM hold_items hi JOIN seats s ON s.id = hi.seat_id WHERE hi.hold_id = h.id)
        FROM holds h WHERE h.id = v_hold;
    RETURN;
  END IF;

  SELECT e.sale_status, e.hold_ttl_seconds INTO v_status, v_ttl FROM events e WHERE e.id = p_event;
  IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE = 'FS404', MESSAGE = 'event not found'; END IF;
  IF v_status <> 'open' THEN RAISE EXCEPTION USING ERRCODE = 'FS423', MESSAGE = 'sale is not open'; END IF;

  SELECT array_agg(DISTINCT i ORDER BY i) INTO v_sorted FROM unnest(p_indexes) i;
  -- Lock seat rows in bitmap order; every writer uses the same order, so
  -- overlapping multi-seat requests cannot deadlock.
  SELECT array_agg(x.id) INTO v_seats FROM (
    SELECT s.id FROM seats s
     WHERE s.event_id = p_event AND s.seat_index = ANY(v_sorted)
     ORDER BY s.seat_index FOR UPDATE
  ) x;
  IF coalesce(array_length(v_seats, 1), 0) <> coalesce(array_length(v_sorted, 1), 0) OR v_sorted IS NULL THEN
    RAISE EXCEPTION USING ERRCODE = 'FS422', MESSAGE = 'unknown seat in request';
  END IF;

  -- Lazy expiry: release past-due holds that block these seats or this buyer,
  -- so a seat is sellable the instant its hold lapses, not at the next sweep.
  FOR r IN
    SELECT DISTINCT hi.hold_id AS id FROM hold_items hi JOIN holds h ON h.id = hi.hold_id
     WHERE hi.seat_id = ANY(v_seats) AND hi.released_at IS NULL AND h.status = 'active' AND h.expires_at <= now()
    UNION
    SELECT h.id FROM holds h
     WHERE h.event_id = p_event AND h.user_id = p_user AND h.status = 'active' AND h.expires_at <= now()
    ORDER BY 1
  LOOP
    PERFORM fs_expire_hold(r.id);
  END LOOP;

  IF EXISTS (SELECT 1 FROM order_items oi WHERE oi.seat_id = ANY(v_seats))
     OR EXISTS (SELECT 1 FROM hold_items hi WHERE hi.seat_id = ANY(v_seats) AND hi.released_at IS NULL) THEN
    RAISE EXCEPTION USING ERRCODE = 'FS409', MESSAGE = 'seat unavailable';
  END IF;
  IF EXISTS (SELECT 1 FROM holds h WHERE h.event_id = p_event AND h.user_id = p_user AND h.status = 'active') THEN
    RAISE EXCEPTION USING ERRCODE = 'FS429', MESSAGE = 'buyer already holds seats for this event';
  END IF;

  v_expires := now() + make_interval(secs => v_ttl);
  INSERT INTO holds(event_id, user_id, status, expires_at, idempotency_key, redis_token)
  VALUES (p_event, p_user, 'active', v_expires, p_key, p_token) RETURNING id INTO v_hold;
  INSERT INTO hold_items(hold_id, seat_id, price_cents)
  SELECT v_hold, s.id, s.price_cents FROM seats s WHERE s.id = ANY(v_seats);
  INSERT INTO reservation_events(event_id, hold_id, user_id, event_type, seat_indexes, actor)
  VALUES (p_event, v_hold, p_user, 'hold.created', v_sorted, p_actor);
  RETURN QUERY SELECT v_hold, false, v_expires, v_sorted;
END $$;

-- Converts a hold to an order in one round trip.
-- FS404 not found, FS403 not owner, FS409 not active, FS410 expired, FS402 declined.
CREATE OR REPLACE FUNCTION fs_checkout(p_hold uuid, p_user uuid, p_key text, p_actor text)
RETURNS TABLE(o_order uuid, o_reference text, o_total integer, o_created timestamptz, o_replayed boolean,
              o_event text, o_token uuid, o_indexes integer[])
LANGUAGE plpgsql AS $$
DECLARE
  v_order uuid; v_event text; v_owner uuid; v_status text; v_expires timestamptz; v_token uuid;
  v_total integer; v_indexes integer[]; v_reference text;
BEGIN
  SELECT o.id INTO v_order FROM orders o WHERE o.user_id = p_user AND o.idempotency_key = p_key;
  IF FOUND THEN
    RETURN QUERY
      SELECT o.id, o.reference, o.total_cents, o.created_at, true, h.event_id, h.redis_token,
             (SELECT coalesce(array_agg(s.seat_index ORDER BY s.seat_index), '{}')
                FROM order_items oi JOIN seats s ON s.id = oi.seat_id WHERE oi.order_id = o.id)
        FROM orders o JOIN holds h ON h.id = o.hold_id WHERE o.id = v_order;
    RETURN;
  END IF;

  SELECT h.event_id, h.user_id, h.status, h.expires_at, h.redis_token
    INTO v_event, v_owner, v_status, v_expires, v_token
    FROM holds h WHERE h.id = p_hold FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE = 'FS404', MESSAGE = 'hold not found'; END IF;
  IF v_owner <> p_user THEN RAISE EXCEPTION USING ERRCODE = 'FS403', MESSAGE = 'hold belongs to another session'; END IF;
  IF v_status <> 'active' THEN
    -- A concurrent request with the same key may have committed while this one
    -- waited for the row lock; return its order instead of an error.
    SELECT o.id INTO v_order FROM orders o WHERE o.user_id = p_user AND o.idempotency_key = p_key AND o.hold_id = p_hold;
    IF FOUND THEN
      RETURN QUERY
        SELECT o.id, o.reference, o.total_cents, o.created_at, true, v_event, v_token,
               (SELECT coalesce(array_agg(s.seat_index ORDER BY s.seat_index), '{}')
                  FROM order_items oi JOIN seats s ON s.id = oi.seat_id WHERE oi.order_id = o.id)
          FROM orders o WHERE o.id = v_order;
      RETURN;
    END IF;
    IF v_status = 'expired' THEN RAISE EXCEPTION USING ERRCODE = 'FS410', MESSAGE = 'hold has expired'; END IF;
    RAISE EXCEPTION USING ERRCODE = 'FS409', MESSAGE = 'hold is no longer active';
  END IF;
  IF v_expires <= now() THEN RAISE EXCEPTION USING ERRCODE = 'FS410', MESSAGE = 'hold has expired'; END IF;
  IF p_key = 'decline-demo' THEN RAISE EXCEPTION USING ERRCODE = 'FS402', MESSAGE = 'simulated payment declined'; END IF;

  SELECT coalesce(sum(hi.price_cents), 0), coalesce(array_agg(s.seat_index ORDER BY s.seat_index), '{}')
    INTO v_total, v_indexes
    FROM hold_items hi JOIN seats s ON s.id = hi.seat_id
   WHERE hi.hold_id = p_hold AND hi.released_at IS NULL;

  v_order := gen_random_uuid();
  v_reference := 'FS-' || to_char(now() AT TIME ZONE 'UTC', 'YYMMDD') || '-' || left(replace(v_order::text, '-', ''), 8);
  INSERT INTO orders(id, reference, hold_id, user_id, idempotency_key, total_cents)
  VALUES (v_order, v_reference, p_hold, p_user, p_key, v_total);
  INSERT INTO order_items(order_id, seat_id, section_name, row_label, seat_number, price_cents)
  SELECT v_order, s.id, sec.name, s.row_label, s.seat_number, hi.price_cents
    FROM hold_items hi JOIN seats s ON s.id = hi.seat_id JOIN sections sec ON sec.id = s.section_id
   WHERE hi.hold_id = p_hold AND hi.released_at IS NULL;
  UPDATE hold_items SET released_at = now() WHERE hold_id = p_hold AND released_at IS NULL;
  UPDATE holds SET status = 'purchased', updated_at = now() WHERE id = p_hold;
  INSERT INTO reservation_events(event_id, hold_id, order_id, user_id, event_type, seat_indexes, actor)
  VALUES (v_event, p_hold, v_order, p_user, 'order.created', v_indexes, p_actor);
  RETURN QUERY SELECT v_order, v_reference, v_total, now(), false, v_event, v_token, v_indexes;
END $$;

-- Buyer-initiated release. FS404 / FS403 / FS409 as above.
CREATE OR REPLACE FUNCTION fs_release(p_hold uuid, p_user uuid, p_actor text)
RETURNS TABLE(o_event text, o_token uuid, o_indexes integer[])
LANGUAGE plpgsql AS $$
DECLARE v_event text; v_owner uuid; v_status text; v_token uuid; v_indexes integer[];
BEGIN
  SELECT h.event_id, h.user_id, h.status, h.redis_token INTO v_event, v_owner, v_status, v_token
    FROM holds h WHERE h.id = p_hold FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE = 'FS404', MESSAGE = 'hold not found'; END IF;
  IF v_owner <> p_user THEN RAISE EXCEPTION USING ERRCODE = 'FS403', MESSAGE = 'hold belongs to another session'; END IF;
  IF v_status <> 'active' THEN RAISE EXCEPTION USING ERRCODE = 'FS409', MESSAGE = 'hold is no longer active'; END IF;
  WITH released AS (
    UPDATE hold_items SET released_at = now() WHERE hold_id = p_hold AND released_at IS NULL RETURNING seat_id
  )
  SELECT coalesce(array_agg(s.seat_index ORDER BY s.seat_index), '{}') INTO v_indexes
    FROM released r JOIN seats s ON s.id = r.seat_id;
  UPDATE holds SET status = 'released', updated_at = now() WHERE id = p_hold;
  INSERT INTO reservation_events(event_id, hold_id, user_id, event_type, seat_indexes, actor)
  VALUES (v_event, p_hold, p_user, 'hold.released', v_indexes, p_actor);
  RETURN QUERY SELECT v_event, v_token, v_indexes;
END $$;
