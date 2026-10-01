# Changelog

PGDock follows semantic versioning; server, agent, UI, and the install
bundle share one version (spec §11.5).

## v1.0.0

The first release: self-hosted managed PostgreSQL 18 with a web UI, on one
VPS or several nodes.

**Install:** [docs/install.md](docs/install.md). **Run it:**
[docs/operations.md](docs/operations.md). **Upgrade:**
[docs/upgrade.md](docs/upgrade.md). **Recover:**
[docs/disaster-recovery.md](docs/disaster-recovery.md).

### Projects
- Shared-tier projects in seconds: a database and role on the shared
  cluster, reached through PgBouncer in session (`:5432`) and transaction
  (`:6543`) mode with TLS required, guardrails per project, password
  rotation, and deletion with a final backup.
- Dedicated projects: their own PostgreSQL 18 container on a node (sizes
  small, medium, large), with continuous WAL archiving and point-in-time
  recovery for 7 days.
- Promotion from shared to dedicated with the same URL and password and a
  short write freeze, verified row by row; the shared copy is kept
  read-only for 48 hours.
- Import from any PostgreSQL 15+ database, Supabase included.

### Backups
- Nightly logical backups (7 daily, 4 weekly), encrypted before upload to
  any S3-compatible bucket; on-demand backups; restore into a new project or
  in place with a safety backup; weekly automatic restore tests; nightly
  self-backups of the metadata database.

### Console, browser, metrics
- SQL console scoped to the project's role (timeout, cancel, 1,000-row cap,
  CSV export, read-only mode), read-only table browser, extension
  allow-list.
- Metrics for projects (size, connections, TPS, cache hit ratio, top
  queries) and nodes (CPU, memory, disk, I/O), 1 h / 24 h / 7 d charts, and
  a Prometheus endpoint.

### Operations and security
- Alerts by signed webhook and SMTP email: backup failed or overdue,
  restore test failed, node disk over 85%, node unreachable, project over
  its disk warning, pooler down, and a failed isolation check.
- The tenant-isolation checklist (spec §7.1) checked in CI and weekly
  against every live shared cluster; `pg_hba.conf` limited to the control
  plane and the poolers.
- One owner account with argon2id passwords and TOTP, server-side
  sessions, CSRF protection, re-authentication for destructive actions,
  rate limiting and lockout, an append-only audit log.
- Secrets encrypted with a master key, rotatable with
  `pgdock-server -rotate-master-key`; agents on mTLS with pinned
  certificates and a major-version check.
- Multi-node: add nodes from the UI, per-node roles, placement by load.

### Tested
- Unit, integration, isolation, failure-injection (agent killed mid-dump,
  pooler killed, disk full, S3 lost), promotion with live writers, a
  150-project load test ([results](docs/load-test.md)), a browser journey
  from a fresh install to every feature, and the install guide itself run
  on a clean machine.
