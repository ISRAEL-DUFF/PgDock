# Load test

The spec §13 load test: 150 shared projects, pgbench on 10 of them, measuring
create latency, pooler overhead, and the effect on a quiet neighbour. Run it
with `make test-load` (needs `pgbench`); it prints a report and writes
`tmp/load-report.md`. `PGDOCK_LOAD_PROJECTS`, `PGDOCK_LOAD_BUSY`,
`PGDOCK_LOAD_SECONDS`, and `PGDOCK_LOAD_PARALLEL` change its size.

The test fails if a create fails, the create p95 exceeds 30 s, the quiet
project's p95 exceeds 250 ms during the load, idle backends linger after the
creates, or backends reach the cluster's `max_connections`.

## Results (2026-10-01)

A 4-vCPU, 15 GB container running the dev environment (`make dev-up`):
PostgreSQL 18 with the laptop tuning from `deploy/dev/compose.yaml`
(`shared_buffers=256MB`, `max_connections=500`), PgBouncer 1.25, everything
on one host.

| Measure | Result |
| --- | --- |
| 150 creates, 8 at a time | 14 s; p50 726 ms, p95 791 ms, max 1.0 s |
| Pooler overhead, indexed point query | direct p50 0.2 ms; session and transaction poolers p50 0.3 ms, p95 0.5 ms |
| pgbench, 10 projects × 8 clients, TPC-B-like, scale 2, 30 s | 1,572 tps in total (about 155 each; each project's pool is 5 backends) |
| Quiet neighbour, p50 / p95 / p99 | idle cluster 0.3 / 0.5 / 0.6 ms; during pgbench 4.0 / 8.5 / 14.6 ms |
| Peak client backends during pgbench | 55 of 500 |
| Backends right after the 150 creates / 40 s later | 301 / 1 |

## Tuning

- **`server_idle_timeout = 30` on both poolers** (was PgBouncer's default of
  600 s). Each create smoke-tests the project through both poolers, and each
  leaves an idle server connection behind. The first run had 343 client
  backends at the peak, nearly all idle: about 250 projects would have filled
  `max_connections` with idle connections. With 30 s they close soon after;
  the creates leave 1 backend after 40 s.
- **Per-project pool size 5 (shared tier) holds.** Ten busy projects used 55
  backends, so the busy tenants' pools cap them, and the quiet project's
  p95 stayed under 10 ms. A project that needs more than about 150 tps of
  this kind of work is a promotion candidate (spec §6.6).
- **The create path needs no change.** 150 creates took 14 s; a pooler sync
  re-renders every route (150 here) and reloads both poolers in
  milliseconds.
