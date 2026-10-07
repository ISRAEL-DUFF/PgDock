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

## V2 load check

`TestLoadV2` (also in `make test-load`, which starts the `load` Compose
profile's second shared node, `shared-pg-2`, and a second agent) is V2
§14 M16's check: 30 organisations, 300 projects across two shared nodes,
20 projects with webhooks at 50 events/s each for 30 s (30,000 events),
100 jobs a minute for 3 minutes, 50 branches created at once, and a quiet
project probed through the transaction pooler throughout. Plan limits are
overridden, so it measures the platform rather than the quotas.
`PGDOCK_LOAD_ORGS`, `PGDOCK_LOAD_V2_PROJECTS`,
`PGDOCK_LOAD_WEBHOOK_PROJECTS`, `PGDOCK_LOAD_EVENTS_PER_SEC`,
`PGDOCK_LOAD_EVENT_SECONDS`, `PGDOCK_LOAD_JOBS`, `PGDOCK_LOAD_JOB_MINUTES`
and `PGDOCK_LOAD_BRANCHES` change its size; it writes
`tmp/load-report-v2.md`.

It fails if a create or branch fails, the create p95 exceeds 30 s, either
node holds less than a fifth of the projects, any event is lost, out of
order, or past a 5 s p95, fewer than 95% of the due job runs happen or any
fails, the job start delay p95 exceeds 10 s, the branch p95 exceeds 3
minutes, the quiet project's p95 exceeds 250 ms, or backends reach
`max_connections`.

### Results (2026-10-03)

The same 4-vCPU container, both shared nodes on one host:

| Measure | Result |
| --- | --- |
| 300 creates, 8 at a time | 25 s; p50 610 ms, p95 840 ms, max 2.0 s; 150 on each node |
| Webhooks, 20 × 50 events/s for 30 s | 30,000 of 30,000 delivered in order, no failed attempts; commit → delivery p50 98 ms, p95 351 ms, p99 485 ms |
| Jobs, 100 a minute for 3 minutes | 300 of 300 runs, all succeeded; start delay p50 0.4 s, p95 4.6 s |
| 50 branches at once (live copies) | 8 s for all; p50 4.1 s, p95 7.7 s |
| Quiet neighbour p50 / p95 / p99 | idle 0.2 / 0.3 / 0.4 ms; during webhooks and jobs 0.7 / 1.8 / 5.5 ms; during the branches 1.2 / 26 / 36 ms |
| Peak client backends | 313 of 500 and 309 of 500 |

### Tuning

- **Webhook delivery.** The first run delivered everything, in order, but
  late: p50 7.6 s, p95 9.3 s, as each delivery loop fell behind 50
  events/s. Every event was checking the organisation, the allow-list and
  DNS, opening a new connection, upserting a counter row, and reading the
  outbox for one row. Now a batch of up to 100 events is checked once and
  read in one query, connections to a checked address are kept alive, and
  the per-host counters are written about once a second: p95 351 ms.
- **Job start delay** is the scheduler starting 100 runs in the same
  second through the console logins; the p95 of 4.6 s is under the 10 s
  budget and well inside the minute.
- **Backends** peak near 310 per node with 150 projects each, mostly the
  idle server connections creates and branches leave until the poolers'
  `server_idle_timeout`: about 2 per project. A node at 500
  `max_connections` should take another node before about 200 projects.

## V3 rating load test

`TestRatingLoad` (in `internal/billing`, also run by `make test-load`) is
V3 M27's load test of rating: 1,000 organisations (60% Free, 30% Pro, 8%
Team, 2% Team with a dedicated HA project), one to three shared projects
each, and a month of usage as the recorder writes it (hourly storage and
transfer per project, daily backup storage, the dedicated project's
compute, HA and synchronous replication hours). It rates every
organisation, then runs the month end (draft every invoice, issue them)
and the ledger audit over the result. `PGDOCK_LOAD_RATING_ORGS` changes
its size; it writes `tmp/load-report-rating.md`.

It fails if an invoice is missing for a paying organisation or isn't
issued, the audit finds a problem, rating's p95 exceeds 500 ms, the
month end takes over 10 minutes, or the audit over a minute.

### Results (2026-10-06)

The same 4-vCPU container, one Postgres for everything:

| Measure | 1,000 orgs | 5,000 orgs |
| --- | --- | --- |
| Usage rows for the month | 3.1 million | 15.7 million |
| Rate each org | 5.9 s in all; p50 5 ms, p95 8 ms, max 24 ms | 30 s; p50 5 ms, p95 9 ms, max 41 ms |
| Draft every invoice | 410 in 6.1 s | 2,050 in 35 s |
| Issue them | 410 in 2.0 s | 2,050 in 13 s |
| Ledger audit | 10 ms | 26 ms |

Rating is per organisation and stays at about 5 ms however many there
are; the month end grows linearly, about 25 ms an organisation.

### Tuning

- **Ledger audit.** The first run took 152 ms at 1,000 organisations and
  4.9 s at 5,000: each invoice, credit note and payment summed its own
  ledger entries with a correlated subquery over the whole ledger, so the
  audit grew with invoices × entries. The ledger is now summed once per
  transaction and joined: 10 ms and 26 ms.
