# FlashSeat architecture

PostgreSQL is the authority; Redis is an admission-control accelerator. A Redis lease never means a seat is sold. Only a committed PostgreSQL order does.

```mermaid
flowchart LR
  B[Next.js client] -->|REST + idempotency key| A[Go API replicas]
  B <-->|SSE invalidations| A
  A -->|atomic multi-key Lua lease| R[(Redis)]
  A -->|sorted SELECT FOR UPDATE| P[(PostgreSQL)]
  W[Reconciliation workers] -->|SKIP LOCKED expiration batches| P
  W -->|token-safe release + publish| R
```

## Hold sequence

```mermaid
sequenceDiagram
  participant C as Client
  participant A as API
  participant R as Redis
  participant P as PostgreSQL
  C->>A: POST /holds + Idempotency-Key
  A->>R: acquire.lua(all seat keys)
  R-->>A: all acquired or conflict
  A->>P: BEGIN; lock sorted seat rows
  A->>P: verify + insert hold/items + audit
  P-->>A: COMMIT
  A->>R: publish inventory invalidation
  A-->>C: 201 Hold
```

The partial unique index on unreleased `hold_items.seat_id` and unique `order_items.seat_id` are final invariant guards. Sorted row locking prevents lock-order inversions for multi-seat requests. Acquisition is all-or-none.

## Expiration and failure behavior

- Every request compares the PostgreSQL `expires_at` value to server time. TTL is not authoritative.
- Redis keyspace notifications are enabled for operational visibility, but correctness does not depend on delivery.
- Any number of workers can claim expired rows with `FOR UPDATE SKIP LOCKED`; only one transitions each hold.
- Lease release checks the ownership token, preventing a stale request from deleting a newer lease.
- If Redis is unavailable, the MVP fails hold creation closed. Reads and existing PostgreSQL state remain correct.
- This deployment has one Redis node, so it does **not** claim Redlock. A real Redlock deployment needs independent Redis masters and still would not replace database invariants.

## Production gaps

Real identity, PCI-compliant payments, a waiting room, abuse controls, multi-region write ownership, durable event streaming, schema migration orchestration, and autoscaling policies are intentionally outside this portfolio MVP.
