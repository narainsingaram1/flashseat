# Benchmark report

Targets are engineering goals. Measured values below come only from accepted, barrier-verified K6 runs on the documented hardware.

| Metric | Target | Measured |
|---|---:|---:|
| Same-seat winners | exactly 1 | 1 in two 100-VU runs, plus 500 and 1,000 VUs |
| Sold-seat oversubscriptions | 0 | 0 |
| Partial multi-seat holds | 0 | Not measured |
| Unexpected HTTP failure rate | <1% | 0% |
| API P95 | <100 ms | 59.7 ms worst accepted run |
| API P99 | <250 ms | 64.0 ms worst accepted run |
| Expiration recovery lag | <10 s | Not measured |

## Reproducible run record

- Date/time: 2026-08-06, America/New_York
- Git revision: `1b3c864` base plus the working-tree HTTP benchmark implementation
- Machine/CPU/memory: Apple M4, 16 GB RAM
- Docker resource limits: Docker Desktop local defaults; not separately pinned
- Scenario: `same-seat-http`, one iteration per VU, shared timestamp barrier
- Virtual users: 100, 500, and 1,000
- Requests: 1,700 accepted `POST /holds` attempts
- Expected contention responses: 1,696 total `409 Conflict`
- PostgreSQL/Redis observations: exactly one live database claim per run; winner token-safely released after evidence persistence
- Correctness verification: 0 barrier misses, 0 unexpected errors, 0 double-bookings in every accepted run

Run the browser-controlled benchmark at `http://localhost:3000`. Each result and its raw K6 summary are saved in `benchmark_jobs` and shown at `/results`. For standalone scenarios, use `k6 run load-tests/scenarios/same-seat-contention.js`.

## Legacy in-process correctness smoke — 2026-08-06

This is a measured correctness smoke test, not an HTTP throughput benchmark. On an Apple M4 with 16 GB RAM, 250 synchronized Go buyers targeted `balcony-32` through the containerized Redis and PostgreSQL services:

| Buyers | Winners | Redis conflicts | Active DB claims | Internal errors | Wall time |
|---:|---:|---:|---:|---:|---:|
| 250 | 1 | 249 | 1 | 0 | 46 ms |
| 10,000 | 1 | 9,999 | 1 | 0 | 152 ms |

Both tests passed the zero-double-booking invariant and released their winning holds afterward. These figures came from goroutines inside the API process, not external HTTP clients, so they are deliberately excluded from the product's Results page and must not be presented as HTTP load results.

## Browser-controlled external HTTP runs — 2026-08-06

Measured on an Apple M4 with 16 GB RAM using Docker Desktop and the pinned `k6 v2.0.0` runner. The accepted evidence path was: separate K6 container → REST API → Redis/PostgreSQL → database invariant query → persisted raw summary.

| HTTP buyers | 201 winners | 409 conflicts | Barrier misses | Unexpected errors | DB claims | Duplicates | P50 hold | P95 hold | P99 hold | Burst completion rate |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 100 | 1 | 99 | 0 | 0 | 1 | 0 | 30.7 ms | 45.0 ms | 46.5 ms | 2,144 attempts/s |
| 100 repeat | 1 | 99 | 0 | 0 | 1 | 0 | 19.6 ms | 28.2 ms | 31.6 ms | 2,848 attempts/s |
| 500 | 1 | 499 | 0 | 0 | 1 | 0 | 26.8 ms | 38.0 ms | 40.3 ms | 11,804 attempts/s |
| 1,000 | 1 | 999 | 0 | 0 | 1 | 0 | 35.6 ms | 59.7 ms | 64.0 ms | 14,433 attempts/s |

“Burst completion rate” is `seat attempts / slowest observed hold-request duration`; it describes the synchronized contention window, not sustained system throughput. Every accepted run recorded zero barrier misses, proving all VUs completed session setup before the shared release timestamp. Do not claim sustained 14K RPS or 10,000 tested VUs from these measurements.

Earlier development runs are retained as `discarded` because they predated persisted barrier-miss verification or used an incomplete summary configuration. Discarded runs remain auditable but are excluded from aggregates.
