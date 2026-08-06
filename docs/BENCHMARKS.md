# Benchmark report

Targets are engineering goals. Measured values remain blank until the matching K6 run completes on documented hardware.

| Metric | Target | Measured |
|---|---:|---:|
| Same-seat winners | exactly 1 | Not measured |
| Sold-seat oversubscriptions | 0 | Not measured |
| Partial multi-seat holds | 0 | Not measured |
| Unexpected HTTP failure rate | <1% | Not measured |
| API P95 | <100 ms | Not measured |
| API P99 | <250 ms | Not measured |
| Expiration recovery lag | <10 s | Not measured |

## Reproducible run record

- Date/time: pending
- Git revision: pending
- Machine/CPU/memory: pending
- Docker resource limits: pending
- Scenario and environment variables: pending
- Virtual users / request rate / duration: pending
- Requests and throughput: pending
- Expected contention responses: pending
- PostgreSQL/Redis observations: pending
- Correctness verification: pending

Run a scenario with `k6 run --summary-export results/summary.json load-tests/scenarios/same-seat-contention.js`. Save raw output and fill this table before turning any number into a resume claim.

## Interactive lab smoke result — 2026-08-06

This is a measured correctness smoke test, not an HTTP throughput benchmark. On an Apple M4 with 16 GB RAM, 250 synchronized Go buyers targeted `balcony-32` through the containerized Redis and PostgreSQL services:

| Buyers | Winners | Redis conflicts | Active DB claims | Internal errors | Wall time |
|---:|---:|---:|---:|---:|---:|
| 250 | 1 | 249 | 1 | 0 | 46 ms |

The test passed the zero-double-booking invariant and released its winning hold afterward. K6 latency and throughput fields above remain intentionally unfilled until the separate network load scenarios run.
