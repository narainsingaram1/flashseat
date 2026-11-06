# Benchmarks

All runs: Apple M4, 16 GB RAM, Docker Desktop with an 8 GB VM. The load generator, both API replicas, PostgreSQL 17, Redis 8, nginx, and Toxiproxy all share that VM. Every run is stored in `simulation_runs` with its k6 summary, server metrics, final invariant check, and replay. Open `/runs` to see them.

## FlashSeat vs. naive, same crowd

Arena preset (9,000 seats), 6,000 buyers over 15 s, no waiting room, 10 s holds.

| | FlashSeat | Naive |
|---|---:|---:|
| Distinct seats sold (PostgreSQL) | 8,581 | 4,182 |
| Seats paid for by buyers (k6) | 8,581 | 10,659 |
| Seats sold to 2+ buyers | 0 | 2,131 |
| Holds created | 5,198 | 5,979 |
| Holds expired, seats resold | 955 expired, 1,456 seats resold | 1,086 expired, 1,256 resold |
| Lost races (server re-picked up to 4×, then 409) | 21 | n/a (never detected) |
| Peak server requests/s | 1,314 | 1,226 |

On FlashSeat, k6's client-side count and PostgreSQL agree to the seat. On naive, buyers were charged for 6,477 more seats than were actually sold.

## Contention is what breaks the naive engine

Club preset (1,170 seats), 600 buyers over 8 s, waiting room admitting 100/s: the naive engine sold **0** seats twice. Holds arrived about 10 ms apart and each took about 1 ms, so the read-then-write races almost never overlapped. The waiting room was protecting the unsafe code. With the queue off and 2,000 buyers in 2 s, the naive engine sold 108 seats twice; FlashSeat sold 0.

## Faults during a live sale

Arena, 6,000 buyers over 20 s, waiting room at 250/s. Faults injected in order: API replica killed (t = 4 s), Redis cut for 6 s (t = 11 s), PostgreSQL slowed by 80 ms ± 20 ms for 8 s (t = 18 s), Redis wiped (t = 26 s).

- 0 seats sold twice, 0 overlapping claims, 0 drift after recovery.
- 3,843 holds failed closed with `503 redis_unavailable`, and 2,018 with `503 inventory_rebuilding` while the bitmap was restored from PostgreSQL.
- Holds and checkouts that hit the 2 s PostgreSQL budget returned `503 timeout`. Their Redis claims were left to expire rather than released, because the outcome was unknown. Clients retry with the same idempotency key.

## Where latency goes, and what is not fixed

- **Low load.** Club venue, queue at 100/s: client hold P99 7 ms, checkout P99 11 ms.
- **Rush.** Arena, 6,000 buyers in 15 s, no queue: server hold P50 15 ms, but P99 is around 1 s.
  - Per-phase logging (`slow hold` lines in the API logs) shows slow requests are slow in every phase, including in-memory work, which means CPU starvation. During the rush, k6 uses 3–4 cores and PostgreSQL 2–4.5 cores of the 10.
  - In isolation, commit `fsync` on Docker Desktop's virtual disk dominates: with `synchronous_commit=off`, P99 falls from about 175 ms to about 17 ms. Durability stays on.
  - **Open follow-up:** sampling `pg_stat_activity` during a rush shows `fs_checkout` doing most of PostgreSQL's CPU work, with about a third of samples waiting on `Lock:transactionid`. The checkout path needs profiling (`EXPLAIN ANALYZE` inside the function, and lock-order analysis against lazy expiry) before the rush numbers are worth quoting.
- Absolute numbers describe one laptop with the load generator on the same machine. The comparison between engines is the meaningful result.
