# Changelog

PGDock follows semantic versioning; server, agent, UI, and the install
bundle share one version (spec §11.5).

## Unreleased

- Fixed: the first-run setup code printed by `install.sh` was missing its
  trailing `=`, so the wizard called it wrong. The installer now prints the
  whole code, new codes have no padding, and the server ignores spaces,
  `=` padding and letter case when comparing. (The code still changes every
  time the server restarts, unless `PGDOCK_SETUP_CODE` is set in `.env`.)
- `deploy/compose/preflight.sh <ui-host> <db-host>`: a read-only check to run
  on the server before `install.sh` (OS, resources, Docker, DNS, ports,
  outbound reach, optionally the backup bucket and mail server).
  See docs/install.md.
- docs/install.md: how to get Cloudflare R2 credentials for the backup
  storage step, what to do when the wizard says the setup code is wrong, and
  memory settings and swap for a 4 GB server.

## v2.1.0

UI fixes found by testing the redesign, a SQL filter bar for the Table
Editor, and a way to add and recover platform admins. No database
migrations: upgrade from v2.0.0 with `git checkout v2.1.0` and
`./install.sh` ([upgrades](docs/upgrade.md)).

### Platform admins
- A platform admin can make another account a platform admin, or an
  ordinary user again (Admin → Users → Make admin / Remove admin), with a
  fresh password and code. The account must be active, approved, verified
  and have two-factor set up; its sessions end and it is emailed; the last
  active admin can't be removed. Setup still makes exactly one admin, so
  add a second (docs/install.md, docs/admin-runbook.md).
- Recovery when no admin can sign in: `pgdock-server admin list | promote |
  demote | reset-2fa | reset-password <email>`, run on the server. Changes
  are audited as done on the server. See docs/operations.md.

### SQL filter bar
- The Table Editor has a filter bar above the grid: type a condition as
  you would after `WHERE` (`email = 'a@b.com' or phone like '081%'`) and
  press Enter. Suggestions (Ctrl+Space, or as you type) list the table's
  columns with their type and nullability, then operators and functions.
  Recent filters are remembered per table, errors show the database's own
  message at the character that caused it, and the grid keeps its last
  good view. It combines with the Filter popover, and the count and export
  follow it.
- Safety: the condition is checked (one expression; nothing that can end
  a `WHERE` clause, such as `;`, `UNION`, `ORDER BY`, `LIMIT`) and runs as
  the project's read-only role in a read-only transaction under the
  statement timeout, so it can read what the console can read and write
  nothing. The API takes it as the `where` parameter of the rows, count
  and export endpoints.

### UI fixes from QA
- Text fields show focus with a thin accent border and a soft glow, not the
  heavy 2px outline buttons and links get.
- Phones: the top bar's switchers no longer overlap (names and badges
  show from the `sm` breakpoint up), and the Table Editor and SQL Editor
  show their table or query list and the editor in turns, with a
  Tables/Queries button, instead of squeezing the grid. The grid's footer
  fits.
- Column default values: a bare word on a text column (`free`) now shows
  how to quote it (`'free'`) before the database refuses it.
- SQL Editor completion picks up tables and columns created by the script
  you just ran, without a reload. The shortcuts sheet names Ctrl+Space
  (⌘Space is Spotlight on macOS).
- The grid's row checkboxes are 14px (they were the browser's 20px, nearly
  as tall as the row) and follow the theme, with a tick and a dash for
  partial selection.
- A branch or project that fails no longer shows a "save the password"
  card, and the message says when cleanup was incomplete.
- Row save errors in a side panel show once, inline, rather than also as
  a toast over the Save button.
- The schema change preview wraps long statements; the Plans table
  names its units; the new project hint describes opaque names; the 404
  page for signed-in users no longer uses the sign-in layout.

## v2.0.0

PGDock V2: users and organisations, quotas and usage, API tokens and the
CLI, visual table editing, your own backup storage, database branching,
demotion, webhooks and scheduled jobs, and a redesigned UI.

**Upgrading from v1.0.0:** read [docs/upgrade.md](docs/upgrade.md) first,
and upgrade agents before the server.

### A redesigned UI
- The whole UI follows Supabase Studio's layout: a top bar with
  organisation, project and branch switchers, an icon rail with section
  menus, and dark and light themes (dark by default).
- **Table Editor:** a spreadsheet grid with filters, multi-column sort,
  immediate cell edits (conflicts reported), rows and columns edited in
  side panels, CSV/JSON/SQL export, and every schema change shown as SQL
  before it runs. Columns can be created with UNIQUE, CHECK and foreign
  keys; tables can be duplicated and commented.
- **SQL Editor:** a code editor with schema-aware completion and
  formatting, saved queries (private or shared with the project, with
  favourites), history and templates.
- Project pages: Database (branches, backups, webhooks, jobs, extensions,
  migrations), Reports, Logs, and settings split into General, Database,
  Compute and tier, and Backup storage.
- Organisation, account and admin pages in the same style; projects shown
  as cards with their branches.
- Keyboard shortcuts (press `?`), loading skeletons, a navigation drawer
  on phones, and colours that meet WCAG AA in both themes.

### Fixes from QA
- Request bodies with unknown fields are refused (`400`) instead of
  ignored; a misspelled field could clear an organisation's outbound
  allow-list.
- `GET /me` with an API token returns the token's real `expires_at`.
- Approving or rejecting a dedicated request, and demoting, accept an
  empty body (every field is optional).
- A project-restricted API token can no longer list organisation members.
- Signing out forgets the organisation remembered in the browser.
- S3-compatible stores that are unreachable or stall now fail within
  seconds (connect, TLS and reply timeouts) instead of holding an
  operation, and its slot in the operations-in-flight quota, for 30 minutes.
- Starting a stopped dedicated instance refreshes its recorded published
  port (dev mode, where Docker picks a new one).
- Switching to opaque credentials: the UI and docs now say members'
  personal logins are renamed at once. Per-project plan defaults are
  512 MB (Personal) and 8 GB (Team); the spec now says so.
- Demoting a project whose instance is down returns `503` with a reason,
  not a bare `500`.
- Upgrade: shared projects created before V2 get `temp_file_limit` (a
  migration queues `apply_settings` for each); `install.sh` recreates the
  poolers so they read the new configuration; an agent on an older minor
  version than the server is refused work (upgrade agents first).

### Hardening (V2 M16)
- **Security:** scheduled SQL jobs no longer run on a superuser
  connection, and copying a tenant's data (branches, restores, imports,
  the restore test, promotion, demotion) no longer runs the tenant's
  functions as the superuser. NAT64 and 6to4 addresses can't reach
  internal or metadata addresses. Upgrade before inviting anyone else.
- Organisation owners can download any backup as a `pg_dump` file
  (Backups → Download, `pgdock backup download`).
- Background work survives being stopped half-way: storage locks, the
  reaper, webhook delivery, the scheduler (runs now end with a stopping
  server), a member removed mid-session, an org suspended mid-backup.
- Faster webhook delivery: kept-alive connections, one destination check
  per batch, batched outbound counters.
- A V2 load check (300 projects on two nodes, webhooks, jobs, branches),
  a user guide, a platform admin runbook, an incident process and a terms
  template.

### Webhooks and scheduled jobs (V2 M15)
- Database webhooks: inserts, updates and deletes of chosen tables POSTed
  to a URL, recorded in the same transaction (a rolled-back change never
  sends anything), in commit order per webhook, signed with HMAC-SHA256,
  retried with backoff for 24 hours, then kept as dead letters you can
  replay. Column filters for updates, static headers, test events, a
  7-day delivery log, auto-pause after 50 failures, and broken-trigger
  detection.
- Scheduled jobs: SQL as the project owner, or a signed HTTP call, on a
  cron schedule in your time zone, with timeouts, skip-or-queue overlap,
  history, run now, and an email after three failures in a row.
- Outbound safety: requests only to public addresses (resolved, checked,
  connected to the checked address, no redirects); the platform admin can
  allow-list internal hosts per organisation, turn its outbound traffic
  off, and see its requests by host. Per-organisation rate limits queue
  webhook deliveries rather than dropping them.
- `pgdock webhooks …` and `pgdock jobs …`; Project → Webhooks and Jobs.

### Demotion (V2 M14)
- Move a dedicated project back to the shared tier with its URL, app
  password and every member's personal login unchanged, after a write
  freeze while the data is copied and verified. A failure before the
  switch leaves it on its dedicated instance.
- A preflight checklist (also `pgdock demote --check`): size against the
  organisation's shared storage limits, extensions, custom roles, peak
  connections, database settings that reset, and a shared cluster with
  room, the organisation's own when it has one.
- Guardrails go back to the shared defaults; point-in-time recovery ends,
  with a logical backup at once, and the old base backups stay
  restorable for 7 days. The stopped dedicated instance is kept for 48
  hours, then destroyed, which releases it from the dedicated allowance.
- `pgdock demote` and Settings → Move back to shared.

### Database branching (V2 M13)
- Branches: throwaway copies of a project on the shared tier, from its
  latest backup or live, schema only or with data, that delete themselves
  after a TTL (7 days by default) with an email a day before.
- Reset a branch from its parent without changing its URL, password or
  members' logins; detach it to keep it as a standalone project.
- Organisation branch quotas (10 on Personal, 25 on Team), branch-hours
  and branch GB-hours usage, and "contains sensitive data" projects whose
  branches copy only the schema by default.
- `pgdock branch list|create|reset|extend|detach|delete`, with `--env`
  for `$GITHUB_ENV`, and an example GitHub Actions workflow that gives
  every pull request its own database.

### Backup storage targets (V2 M12)
- Platform storage targets (one is the default) and organisation targets:
  buckets an organisation brings itself, invisible to everyone else and
  not counted against its backup quota. Every target is live-tested
  (write, read, list, delete) before it's saved; credentials are never
  shown again.
- A project chooses where its new backups go; existing ones stay
  restorable where they are, or are copied over, verified by checksum.
  Dedicated projects move their WAL-G archive and take a fresh base
  backup at once.
- Per-project backup keys: backups become standard OpenPGP messages, and
  the downloaded key file (re-authentication, audited) restores them with
  gpg and pg_restore alone.
- Backup storage on platform targets counts toward the `backup_storage_mb`
  quota.
- Point-in-time recovery reads WAL with the source archive's own
  credentials and key.

### Visual table editing (V2 M11)
- The table browser filters (equals, contains, ranges, null, lists),
  sorts by any column, opens foreign-key rows in a side panel, and
  exports up to 100,000 rows as CSV or JSON.
- Row editing for tables with a primary key: staged inline edits, new
  rows and deletes saved in one transaction, with type-aware inputs.
  If someone changed a row since you loaded it, the save stops with a
  conflict showing their version instead of overwriting it.
- A schema editor for tables, columns, constraints, foreign keys,
  indexes, schemas and enums. Every change previews its SQL with risk
  notes (table rewrites, NOT NULL scans, volatile defaults), runs with a
  5-second lock timeout (indexes concurrently), is audited with its SQL,
  and exports as a plain SQL, goose or dbmate migration.

### API tokens and the CLI (V2 M10)
- API tokens for scripts and CI: one organisation each, read/write/admin
  scopes, an optional project restriction, and a required expiry (90
  days by default, at most a year). Shown once; revocable by their owner
  and by the organisation's owners and admins; disabled when the
  organisation is suspended and revoked when their user leaves it.
- The `pgdock` CLI for linux, macOS and Windows: device login in the
  browser, contexts for several servers and organisations,
  `PGDOCK_TOKEN` for CI, `--json` everywhere, and commands for
  organisations, projects, SQL, connecting, backups, promotion, members,
  tokens and operations. See [docs/cli.md](docs/cli.md).

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
