CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE events (
  id text PRIMARY KEY,
  name text NOT NULL,
  venue text NOT NULL,
  starts_at timestamptz NOT NULL,
  sale_status text NOT NULL CHECK (sale_status IN ('scheduled','open','closed')),
  accent_color text NOT NULL DEFAULT '#e24a32'
);
CREATE TABLE sections (
  id text PRIMARY KEY,
  event_id text NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  name text NOT NULL,
  sort_order integer NOT NULL
);
CREATE TABLE seats (
  id text PRIMARY KEY,
  event_id text NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  section_id text NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
  row_label text NOT NULL,
  seat_number integer NOT NULL,
  price_cents integer NOT NULL CHECK (price_cents > 0),
  accessible boolean NOT NULL DEFAULT false,
  UNIQUE(section_id,row_label,seat_number)
);
CREATE TABLE holds (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  event_id text NOT NULL REFERENCES events(id),
  user_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('active','released','expired','purchased')),
  expires_at timestamptz NOT NULL,
  idempotency_key text NOT NULL,
  redis_token uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id,idempotency_key)
);
CREATE TABLE hold_items (
  hold_id uuid NOT NULL REFERENCES holds(id) ON DELETE CASCADE,
  seat_id text NOT NULL REFERENCES seats(id),
  price_cents integer NOT NULL,
  released_at timestamptz,
  PRIMARY KEY(hold_id,seat_id)
);
CREATE UNIQUE INDEX one_live_hold_per_seat ON hold_items(seat_id) WHERE released_at IS NULL;
CREATE TABLE orders (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  reference text NOT NULL UNIQUE,
  hold_id uuid NOT NULL UNIQUE REFERENCES holds(id),
  user_id uuid NOT NULL,
  idempotency_key text NOT NULL,
  total_cents integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id,idempotency_key)
);
CREATE TABLE order_items (
  order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  seat_id text NOT NULL UNIQUE REFERENCES seats(id),
  section_name text NOT NULL,
  row_label text NOT NULL,
  seat_number integer NOT NULL,
  price_cents integer NOT NULL,
  PRIMARY KEY(order_id,seat_id)
);
CREATE TABLE reservation_events (
  id bigserial PRIMARY KEY,
  event_id text NOT NULL,
  hold_id uuid,
  order_id uuid,
  user_id uuid NOT NULL,
  event_type text NOT NULL,
  metadata jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX holds_expiration_idx ON holds(expires_at) WHERE status='active';
CREATE INDEX seats_event_idx ON seats(event_id,section_id,row_label,seat_number);

INSERT INTO events(id,name,venue,starts_at,sale_status) VALUES ('aurora-2026','Aurora After Dark','The Halcyon · Brooklyn','2026-10-18T00:00:00Z','open');
INSERT INTO sections(id,event_id,name,sort_order) VALUES
  ('orchestra','aurora-2026','ORCHESTRA',1),('mezzanine','aurora-2026','MEZZANINE',2),('balcony','aurora-2026','BALCONY',3);
INSERT INTO seats(id,event_id,section_id,row_label,seat_number,price_cents,accessible)
SELECT section_id || '-' || n, 'aurora-2026', section_id, chr(65 + ((n-1)/8)::int), ((n-1)%8)+1,
  CASE section_id WHEN 'orchestra' THEN 18500 WHEN 'mezzanine' THEN 12500 ELSE 7500 END,
  n IN (8,25)
FROM (VALUES ('orchestra'),('mezzanine'),('balcony')) s(section_id), generate_series(1,32) n;
