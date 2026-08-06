# FlashSeat

**A high-concurrency assigned-seat reservation engine built for the exact moment everyone clicks “buy.”**

FlashSeat is a portfolio-grade concurrency lab with a focused Next.js simulation experience and a Go reservation engine backed by PostgreSQL and Redis. It demonstrates atomic seat contention, deterministic row locking, persistent proof history, and reproducible K6 load tests.

> Performance numbers in this repository are targets until a benchmark run is recorded in [docs/BENCHMARKS.md](docs/BENCHMARKS.md). Correctness claims are enforced by the database design and test scenarios; they are not fabricated throughput claims.

## What works

- Run the concurrency simulator directly from the home page.
- Choose 1,000, 5,000, or 10,000 simultaneous buyers targeting one live seat.
- See the one winner, rejected conflicts, database claims, errors, and timing clearly.
- Save every run in PostgreSQL and showcase aggregate proof on a dedicated results page.
- Run multiple API and worker replicas without double-transitioning a hold.
- Observe Prometheus HTTP request, conflict, and latency metrics.
- Exercise contention, journey, expiration, replay, soak, and 10K-VU spike profiles with K6.
- Launch up to 10,000 simultaneous buyers from the browser and inspect the one-winner invariant immediately.

## Quick start

Prerequisites: Docker Desktop with Compose. K6 is needed only for load testing.

```bash
cp .env.example .env
docker compose up --build
```

Open:

- Simulation Lab: http://localhost:3000
- Saved Results: http://localhost:3000/results
- API readiness: http://localhost:8080/health/ready
- Prometheus metrics: http://localhost:8080/metrics

For a fast expiration demonstration:

```bash
HOLD_TTL_SECONDS=10 docker compose up --build
```

Observability profile:

```bash
docker compose --profile observability up --build
```

This adds Prometheus at port 9090 and Grafana at port 3001 (`admin` / `flashseat`).

## Correctness design

The critical path intentionally uses two layers with different responsibilities:

1. An atomic Redis Lua script leases every requested seat key together. If one key is occupied, it writes nothing.
2. PostgreSQL starts a transaction and locks seat rows in sorted ID order using `SELECT ... FOR UPDATE`.
3. The transaction verifies sales and live holds, inserts the hold and immutable price snapshots, and records an audit event.
4. Partial unique indexes prevent two unreleased hold items for one seat. A unique order-item constraint makes overselling impossible even if application logic regresses.
5. Checkout locks the hold, verifies ownership and authoritative database time, creates exactly one order, and releases the Redis leases after commit.

Redis TTL notifications are deliberately not a correctness dependency. A worker continuously claims overdue holds in bounded `FOR UPDATE SKIP LOCKED` batches, transitions them exactly once, and releases token-owned lease keys. See [the architecture notes](docs/ARCHITECTURE.md).

## Interactive demonstration

Open the home page, choose a 1,000, 5,000, or 10,000-buyer preset, and select **Launch simulation**. The Go API releases every goroutine through one synchronization barrier, reports the Redis conflicts, verifies the active PostgreSQL claims, and automatically releases the test hold afterward. Each run is saved to PostgreSQL and appears on the dedicated `/results` proof page with aggregate totals and resume-safe wording. `LAB_ENABLED` is disabled by default outside Docker so these diagnostic endpoints are not accidentally exposed.

## API

All product routes are under `/api/v1`:

| Method | Route | Purpose |
|---|---|---|
| POST | `/sessions/demo` | Create or restore a demo-user cookie |
| GET | `/events` | List events |
| GET | `/events/{eventId}` | Event details |
| GET | `/events/{eventId}/seats` | Availability snapshot and server time |
| GET | `/events/{eventId}/stream` | SSE inventory invalidations |
| POST | `/holds` | Atomic all-or-none hold |
| GET / DELETE | `/holds/{holdId}` | Read or release an owned hold |
| POST | `/holds/{holdId}/checkout` | Idempotent simulated checkout |
| GET | `/orders/{orderId}` | Read an owned order |

Mutating requests require `Idempotency-Key`. The browser demo uses an HTTP-only session cookie. Stable JSON errors use `code`, `message`, and `status`. The contract is in [docs/openapi.yaml](docs/openapi.yaml).

## Testing

```bash
# Go unit tests
cd apps/api && go test ./...

# Database concurrency test (stack must be running)
cd apps/api && go test -tags=integration ./internal/store

# Web production build and rendered-route tests
npm test

# Atomic same-seat collision; exactly one winner threshold
k6 run load-tests/scenarios/same-seat-contention.js

# Realistic browse → hold → checkout flow
k6 run load-tests/scenarios/journey.js

# Hardware-intensive 10K virtual-user profile
k6 run load-tests/scenarios/spike-10k.js
```

Other profiles are in `load-tests/scenarios`: hot section, expiration churn, idempotency replay, and soak. Configure `BASE_URL`, `VUS`, `DURATION`, or `SEAT_ID` through K6 environment variables.

## Repository map

```text
app/                  Next.js simulation and saved-results routes
apps/api/             Go API, reconciliation worker, and tests
infra/migrations/     PostgreSQL schema, constraints, and seed event
infra/redis/          Auditable Lua lock/release scripts
infra/prometheus/     Metrics scrape configuration
load-tests/           K6 workloads
docs/                 Architecture, API contract, and benchmark record
```

The web workspace stays at the repository root because it is also deployable as a Cloudflare-compatible Sites application. The service and infrastructure remain separated as monorepo workspaces.

## Failure semantics

- Competing request: `409 seat_unavailable`; no partial hold is created.
- Invalid selection: `422 invalid_seats`.
- Expired hold: `410 hold_expired`.
- Redis unavailable during admission: fail closed; no database hold is attempted.
- Missed Redis notification: reconciliation worker releases the database hold.
- Duplicate idempotency key: return the original hold or order.
- SSE disconnect: the UI keeps its last snapshot and refreshes via REST when reconnecting.

## Honest resume framing

Before benchmarking:

> Built a horizontally safe seat-reservation MVP using Go, PostgreSQL row locks and constraints, Redis atomic Lua leases, idempotent checkout, SSE inventory updates, and `SKIP LOCKED` expiration workers.

After running and recording K6 results, replace placeholders only with measured values:

> Validated `[N]` concurrent checkout attempts with zero oversubscriptions; sustained `[RPS]` at `[P99] ms` P99 on `[hardware profile]` using reproducible K6 contention and soak workloads.

## Intentional production gaps

This is a focused systems MVP, not a payment processor. Production adoption would still require real authentication, PCI-compliant payments, fraud and abuse controls, a virtual waiting room, multi-region write ownership, durable event delivery, managed migrations, secret management, backups, and autoscaling policy.
