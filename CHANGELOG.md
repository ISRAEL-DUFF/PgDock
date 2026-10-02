# Changelog

PGDock follows semantic versioning; server, agent, UI, and the install
bundle share one version (spec §11.5).

## Unreleased

### Tenancy hardening, quotas and usage (V2 M9)
- Opaque database and role names for new projects; existing projects are
  renamed behind an alias (same URLs) and can switch to opaque
  credentials with a grace period. `pg_stat_activity` no longer shows
  other tenants.
- Quota plans with per-org overrides, checked when creating projects,
  backups, restores, console queries and connections.
- Storage enforcement on the shared tier: warning at 90%, read-only at
  100%, no app logins at 120%, with the console still working and
  Reclaim space. Long statements (10 min) and idle transactions (5 min)
  are ended; `temp_file_limit` is 2 GB.
- Per-org shared clusters, dedicated allowances and dedicated requests.
- Org suspension, break-glass sessions, and an admin console for
  organisations, plans and requests.
- Hourly usage recording, a Usage & quotas page with CSV export, and
  organisation deletion with a 7-day grace period.
- The isolation check runs nightly and covers metadata leaks, quota
  bypass and suspended orgs.

### Users and organisations (V2 M8)
- Many users: sign-up (invite-only by default, approval, or open with
  email domains), email verification, password reset, recovery codes,
  and versioned terms. SMTP is now a setup step.
- Organisations with owner, admin, and member roles; invitations,
  ownership transfer, leaving, and project transfer between orgs.
- Project roles (admin, developer, read-only) with a personal database
  login per member; removing someone drops their login and ends their
  connections at once.
- Organisation, project, and platform audit logs; an org switcher and
  account pages.
- Upgrading: existing projects move into the platform admin's personal
  organisation unchanged. `/api/v1/audit` is now `/api/v1/admin/audit`.

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
