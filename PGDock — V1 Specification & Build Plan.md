# PGDock — V1 Specification & Build Plan

*Self-hosted managed PostgreSQL with a web UI. "PGDock" is a working name.*

|  |  |
| --- | --- |
| **Status** | Draft v2 — open questions resolved (see §16) |
| **Scope** | V1: managed Postgres only (no auth, REST, storage, or realtime layers) |
| **Stack** | Go control plane, PostgreSQL 18, PgBouncer, React web UI embedded in the Go binary |
| **Hosting** | Hetzner nodes, Cloudflare R2 for backups |

---

## 1. Problem & Goals

### 1.1 Problem

1. **Hobby friction.** Managed-DB free tiers cap the number of projects. Spinning up a throwaway database means paying or juggling accounts.
2. **Growth cliff.** Projects that become startups inherit managed-DB pricing that doesn't fit early-stage budgets, and leaving means a migration.

### 1.2 Design principle

> **Cheap to create, easy to promote.** A hobby database costs effectively nothing and takes seconds to create. A database that becomes serious moves to dedicated resources **without changing its connection string**.

### 1.3 V1 goals

- Create a production-usable Postgres database from the web UI in **under 10 seconds**.
- Run **100+ hobby databases** on a single modest VPS (4 vCPU / 8 GB / 160 GB SSD).
- **Stable connection strings** that survive promotion from the shared tier to the dedicated tier.
- **Automated, tested backups** to S3-compatible storage, with restore from the UI.
- **Strong tenant isolation** on the shared tier: no project can see or affect another's data.
- Basic **observability**: size, connections, and activity per project, plus node health.
- A **browser SQL console** (read/write, with a per-project read-only toggle) and a read-only table browser.
- **Import from an existing database** (e.g. a Supabase project) by pasting its connection string.

### 1.4 Non-goals (V1)

- Auth, auto-generated REST/GraphQL, storage, realtime, or edge functions.
- Automatic cloud VM provisioning (nodes are registered manually).
- High-availability failover and read replicas.
- Billing, quotas-as-billing, or a public multi-customer SaaS mode.
- Demotion from dedicated back to shared.
- Multiple operators (V1 is single-owner; teammates arrive in V1.1).
- Multiple Postgres major versions (V1 runs PostgreSQL 18 only).
- Per-project backup buckets in the UI (the schema supports them; the UI comes in V1.1).
- Visual table editing (the table browser is read-only in V1).
- Multi-region deployments.

---

## 2. Concepts & Terminology

| Term | Meaning |
| --- | --- |
| **Operator** | A person who logs into PGDock's web UI (you, and later teammates). |
| **Project** | One logical database a user works with. Has a name, slug, tier, and connection strings. |
| **Node** | A Linux server registered with PGDock and running the **agent**. |
| **Shared cluster** | One Postgres instance on a node that hosts many shared-tier projects, each as its own database and role. |
| **Dedicated instance** | A Postgres container on a node that serves exactly one project. |
| **Edge pooler** | The PgBouncer pair every client connects through. It routes by database name to the project's current backend. |
| **Operation** | An async job (create, backup, restore, promote, delete) tracked in the metadata DB. |

---

## 3. Architecture

### 3.1 Topology

```
                         ┌──────────────────────────── Control Node ────────────────────────────┐
  Browser ──HTTPS──▶     │  Caddy (TLS) ──▶ pgdock-server (Go: API + embedded UI + workers)     │
                         │                        │                                              │
                         │                        ├──▶ Metadata Postgres (pgdock's own DB)       │
                         │                        │                                              │
  App clients ──TLS──▶   │  PgBouncer :5432 (session)   ┐   routes by dbname                     │
                         │  PgBouncer :6543 (transaction)┘──────────────┐                        │
                         └───────────────────────────────────────────────┼────────────────────────┘
                                                                         │ private network
                     ┌───────────────────────────────────────────────────┼───────────────┐
                     ▼                                                   ▼               ▼
            ┌── Node A (shared) ──┐                          ┌── Node B ──────────────────────┐
            │ pgdock-agent        │                          │ pgdock-agent                   │
            │ Shared Postgres     │                          │ Dedicated PG container (proj X)│
            │  ├ db: blog_k2f9    │                          │ Dedicated PG container (proj Y)│
            │  ├ db: todo_8xq1    │                          └────────────────────────────────┘
            │  └ … 100s more      │
            └─────────────────────┘
                     │
                     └──────▶ S3-compatible object storage (R2 / B2 / MinIO) for backups
```

**Minimum deployment:** everything (control plane, metadata DB, pooler, shared cluster, agent) runs on **one VPS**. More nodes are optional and added through the UI.

### 3.2 Components

| Component | Tech | Responsibility |
| --- | --- | --- |
| `pgdock-server` | Go single binary | REST API, embedded web UI, session auth, job workers, pooler config management, direct SQL to backends for provisioning. |
| Metadata DB | PostgreSQL | Stores operators, nodes, projects, operations, backups, and the audit log. Backed up like any other project. |
| Edge pooler | PgBouncer ≥ 1.21, two processes | Stable client entry point. `:5432` is session mode (migrations, ORMs that need session features). `:6543` is transaction mode (app traffic, serverless). |
| `pgdock-agent` | Go binary, one per node | Manages Docker containers for dedicated instances, runs `pg_dump`/`pg_restore` and WAL-G locally, reports host metrics, and writes local Postgres config. |
| Shared cluster | PostgreSQL 18 | Hosts shared-tier projects. |
| Dedicated instance | Official `postgres` image plus WAL-G, in Docker | One project per instance, with CPU/memory limits. |
| Object storage | Cloudflare R2 (any S3 API works) | Logical dumps (shared tier) and base backups + WAL (dedicated tier). R2 has no egress fees, which keeps restores and restore tests cheap. |
| Caddy | Caddy 2 | TLS for the web UI via automatic Let's Encrypt. |

### 3.3 Communication

- **Server → backend Postgres:** direct connection over the private network, using a per-node `pgdock_admin` superuser. Used for SQL-level provisioning such as roles, databases, grants, and extensions.
- **Server → agent:** HTTPS with **mutual TLS**. The server acts as a small CA and issues each agent a client certificate at registration. The agent exposes a narrow, fixed command API with no arbitrary shell execution.
- **Clients → pooler:** TLS required (`sslmode=require` minimum). The pooler uses a Let's Encrypt certificate for the configured DB hostname (e.g. `db.yourdomain.com`), obtained via DNS-01 or HTTP-01 and reloaded on renewal.
- **Pooler → backends:** private network, no public exposure. Backends' `pg_hba.conf` accepts connections only from the pooler host and the control plane.

### 3.4 Key technology choices

| Area | Choice | Why |
| --- | --- | --- |
| HTTP router | `chi` | Minimal and idiomatic. |
| DB driver | `pgx/v5` | Best Go Postgres driver. Supports `SET ROLE` and query cancellation for the SQL console. |
| Queries | `sqlc` | Type-safe SQL without an ORM. |
| API contract | OpenAPI 3 + `oapi-codegen` (Go) + `openapi-typescript` (TS) | One spec generates both server interfaces and client types, so UI and API can't drift. |
| Migrations | `goose` | Simple and embeddable. |
| Docker | Docker Engine SDK for Go | Controls dedicated instances from the agent. |
| Object storage | `aws-sdk-go-v2` (S3) | Works with R2, B2, and MinIO. |
| Continuous backup | WAL-G | Base backups plus WAL archiving and PITR on dedicated instances. |
| Job queue | Postgres table + `FOR UPDATE SKIP LOCKED` | No extra infrastructure. |
| Web UI | React + TypeScript + Vite, TanStack Query, TanStack Router, Tailwind | Built into `/web/dist`, embedded with `go:embed`, shipped as one binary. |
| SQL editor | CodeMirror 6 with the SQL language package | Lightweight and good UX. |
| Charts | Recharts | Enough for time-series panels. |

---

## 4. Tiers

### 4.1 Shared tier (default)

- Each project gets **one database and one owner role** on a shared cluster.
- Creation is pure SQL, so it completes in about 1 second.
- Per-project guards: a connection limit (default 20), a default `statement_timeout` (default 60s), `idle_in_transaction_session_timeout` (default 60s), and a soft disk quota monitored and alerted on (Postgres has no hard per-DB quota).
- Backups: nightly `pg_dump -Fc` to object storage.
- Extensions: enabled from an allow-list by the control plane (see §7.4).

### 4.2 Dedicated tier

- Each project gets **its own Postgres container** with configured CPU and memory limits and a persistent volume.
- The project gets its own `postgresql.conf` tuning and all allow-listed extensions. V1 runs PostgreSQL 18 on both tiers; choosing a major version is a V1.1 feature.
- Backups: WAL-G continuous archiving, with a daily base backup and point-in-time recovery inside the retention window (default 7 days).
- The project is reachable through the **same pooler URL** as before promotion.

### 4.3 Default guardrails

| Setting | Shared | Dedicated |
| --- | --- | --- |
| Max connections (backend) | 20 per project | Instance `max_connections` (default 100) |
| Pooler default pool size | 5 | 20 |
| `statement_timeout` | 60s | unset (configurable) |
| Disk warning | 1 GB | 80% of volume |
| Backup | Nightly logical, 7 daily + 4 weekly | Daily base + WAL, 7-day PITR |
| Extensions | Allow-list, operator-enabled | Allow-list + version choice |

All values are editable per project in the UI.

---

## 5. Connection Model

### 5.1 Stable connection strings

Every project gets a **globally unique database name**, `<slug>_<4-char-random>` (e.g. `blog_k2f9`). The pooler routes on this name, so the backend host is never part of the client's connection string.

```
Pooled (transaction):  postgresql://blog_k2f9_owner:<pw>@db.yourdomain.com:6543/blog_k2f9?sslmode=require
Session (migrations):  postgresql://blog_k2f9_owner:<pw>@db.yourdomain.com:5432/blog_k2f9?sslmode=require
```

On promotion, only the pooler's `[databases]` entry changes. Clients reconnect transparently, and the URLs never change.

### 5.2 Pooler configuration management

- `pgdock-server` owns the pooler config files and regenerates them from the metadata DB (`[databases]` section plus the auth file).
- It writes atomically (temp file + rename), then issues `RELOAD` on the PgBouncer admin console.
- For a promotion cutover it uses `PAUSE <db>` → switch route → `RELOAD` → `RESUME <db>`, so in-flight clients wait rather than error.
- **Auth:** the control plane generates the password, sets it on the backend role, and writes the **SCRAM-SHA-256 verifier** (never the plaintext) to PgBouncer's `auth_file`. SCRAM passthrough lets PgBouncer authenticate to the backend without storing plaintext.
- Transaction mode sets `max_prepared_statements` (e.g. 200) so ORMs that use protocol-level prepared statements work.

### 5.3 Credentials

- The password is **shown once** at creation. Operators can **rotate** it at any time, which updates the backend role and the pooler auth file, then shows the new password once.
- PgDock does **not** store project passwords in plaintext or reversibly. It stores only the SCRAM verifier needed by the pooler.
- The SQL console never needs the project password (see §8.5).

---

## 6. Core Flows

Every flow runs as an **operation** row with states `queued → running → succeeded | failed`, a step log, and the ID of the operator who started it. The UI streams progress through Server-Sent Events.

### 6.1 Create project (shared)

1. Validate the name and generate a slug + random suffix. Pick the shared node with the most free capacity.
2. Generate a 32-byte random password and compute its SCRAM verifier.
3. On the chosen cluster, as `pgdock_admin`:

   ```sql
   CREATE ROLE blog_k2f9_owner LOGIN PASSWORD '<scram-verifier>' CONNECTION LIMIT 20;
   CREATE DATABASE blog_k2f9 OWNER blog_k2f9_owner;
   REVOKE ALL ON DATABASE blog_k2f9 FROM PUBLIC;
   GRANT CONNECT, TEMP ON DATABASE blog_k2f9 TO blog_k2f9_owner;
   ALTER ROLE blog_k2f9_owner SET statement_timeout = '60s';
   ALTER ROLE blog_k2f9_owner SET idle_in_transaction_session_timeout = '60s';
   -- inside blog_k2f9:
   REVOKE CREATE ON SCHEMA public FROM PUBLIC;
   ALTER SCHEMA public OWNER TO blog_k2f9_owner;
   ```
4. Add the pooler route, write the auth entry, and reload the pooler.
5. Run a smoke test: connect **through the pooler** as the new role and run `SELECT 1`.
6. Mark the project `active` and show the connection strings and password once.

**Rollback:** each step has a compensating action (drop DB, drop role, remove route). A failed create leaves nothing behind.

### 6.2 Delete project

1. Require the operator to type the project name, then re-authenticate.
2. Take a **final backup** (on by default, can be skipped).
3. `PAUSE` and remove the pooler route, then terminate backend connections.
4. Drop the database and role (shared) or stop the container and remove its volume (dedicated).
5. Soft-delete the metadata. The final backup is retained for 30 days.

### 6.3 Backup (shared)

1. The scheduler enqueues a nightly `backup` operation per project, with jitter to spread the load.
2. The agent on that node runs `pg_dump -Fc -d <db>` and streams it to object storage with **client-side encryption** (age or AES-256-GCM with a key held by the control plane). The file goes to `s3://<bucket>/projects/<id>/logical/<timestamp>.dump.enc`.
3. Record the size, duration, and checksum in `backups`.
4. Apply the retention policy (7 daily, 4 weekly) and delete expired objects.

### 6.4 Backup (dedicated)

- WAL-G is configured in the container with `archive_mode=on` and `archive_command=wal-g wal-push %p`.
- A daily `wal-g backup-push` job runs through the agent.
- Retention: `wal-g delete retain FULL 7` (or time-based).
- The UI shows the latest base backup, WAL archive lag, and the available PITR window.

### 6.5 Restore

Two modes. **"Restore into a new project" is the default** because it is non-destructive.

- **Into new project:** create a fresh project on the same tier, then `pg_restore` (shared) or run a WAL-G `backup-fetch` plus recovery to a target time (dedicated). The operator can inspect the result before switching over.
- **In place (dangerous):** requires re-authentication and typing the project name. It takes a safety backup first, pauses the pooler route, restores, and resumes.

Restore is also **tested automatically**: a weekly job restores the latest backup of a random project into a scratch database, runs `SELECT count(*)` on its tables, and drops it. Failures surface as alerts.

### 6.6 Promote shared → dedicated

This is the flagship flow. V1 uses dump/restore with a short write freeze. V1.1 adds logical replication for near-zero downtime (see §12).

1. The operator chooses a target node, a CPU/memory profile, and a volume size. The UI shows the estimated downtime from the DB size (roughly dump + restore time).
2. The agent on the target node creates and starts a dedicated Postgres container with WAL-G configured. The control plane creates the same role with the **same SCRAM verifier**, so the password is unchanged.
3. **Freeze:** `ALTER DATABASE ... ALLOW_CONNECTIONS false` on the shared DB, `PAUSE` the pooler route, and terminate existing sessions.
4. `pg_dump -Fc` from shared → `pg_restore` into dedicated, streamed between agents or through the control plane.
5. **Verify:** compare per-table row counts plus the sequence values between source and target.
6. Switch the pooler route to the dedicated instance, `RELOAD`, then `RESUME`.
7. Take an immediate base backup on the dedicated instance.
8. Keep the old shared DB **read-only for 48 hours** as a rollback option, then drop it automatically.

**Rollback:** if anything fails before step 6, re-enable connections on the shared DB, resume, and tear down the new container. The project stays on shared with no data lost.

### 6.7 Enable an extension

1. The operator picks from the allow-list in the UI.
2. The control plane runs `CREATE EXTENSION IF NOT EXISTS <ext>` as `pgdock_admin` inside the project DB, then grants usage as needed.
3. The change is recorded in the audit log.

### 6.8 Import from an existing database

The first use of PGDock is moving existing Supabase projects across, so import is part of V1. It reuses the restore machinery from M3.

1. The operator pastes a source connection string, chooses a project name and tier, and picks which schemas to import. For Supabase sources the UI preselects `public` (and any user-created schemas) and skips Supabase-managed schemas such as `auth`, `storage`, `realtime`, `extensions`, `graphql`, and `vault`.
2. **Preflight** (runs before anything is created): connect to the source, report its Postgres version, database size, schemas, and installed extensions, and flag any extension not on the target tier's allow-list. The preflight also flags references to Supabase-specific roles (`anon`, `authenticated`, `service_role`) in grants and RLS policies.
3. Create the target project as in §6.1 and enable the allow-listed extensions the source uses.
4. The agent runs `pg_dump -Fc --no-owner --no-acl -n <schema>…` against the source and `pg_restore --no-owner --role=<project_owner>` into the target, so every object ends up owned by the project role.
5. **Verify:** compare per-table row counts and sequence values between source and target, and report the result in the operation log.
6. Show the new connection strings and password once. The source is never modified.

**Notes**

- The source connection string is used only for the duration of the operation. It is held in memory, never written to the metadata DB, and redacted from operation logs.
- Older source versions (e.g. 15 or 17) restore cleanly into 18 through `pg_dump`/`pg_restore`.
- RLS policies that reference Supabase roles are kept but will not be enforced as before, because those roles don't exist in PGDock. The preflight lists them so the operator can review them after import.
- Import is a one-time copy with a write freeze up to the operator (stop the app, import, repoint). Live sync from a source is out of scope.

---

## 7. Security

### 7.1 Tenant isolation checklist (shared tier)

Every create enforces, and an automated test verifies:

- [ ] `CONNECT` on each database is revoked from `PUBLIC`, so roles can reach only their own DB.
- [ ] `CREATE` on `public` is revoked from `PUBLIC`.
- [ ] Project roles are `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`.
- [ ] Project roles are not members of `pg_read_all_data`, `pg_write_all_data`, `pg_read_server_files`, `pg_execute_server_program`, or other predefined roles that grant cross-DB or filesystem access.
- [ ] Untrusted languages and escape-prone extensions (`plpython3u`, `plperlu`, `dblink`, `postgres_fdw`, `file_fdw`, `adminpack`) are **never** available on the shared tier.
- [ ] `pg_hba.conf` accepts only the pooler and control plane IPs, using `scram-sha-256` and `hostssl` where applicable.
- [ ] `log_statement` stays off by default, since project SQL may contain secrets. Only `log_min_duration_statement` is set.

**Isolation test suite (CI + weekly on live nodes):** create two throwaway projects A and B, then assert that A cannot connect to B's DB, list B's tables, read B's data through any predefined role, create objects in B, or read server files.

### 7.2 Operator authentication (web UI)

- Local accounts with email + password hashed with **argon2id**.
- **TOTP 2FA** is required for all operators (enforced at first login).
- Server-side sessions in the metadata DB: `HttpOnly`, `Secure`, `SameSite=Strict` cookies with a 12-hour idle timeout.
- CSRF protection through double-submit tokens on all mutating requests.
- **Re-authentication** (password + TOTP) is required for destructive actions: delete, in-place restore, rotating the master key, and removing a node.
- Login rate limiting and lockout after repeated failures.
- **Single operator in V1.** The setup wizard creates one `owner` account. The `operators.role` column exists so that teammates, invitations, and a `member` role can be added in V1.1 without a schema change.

### 7.3 Secrets

- Secret material (the agent CA key, backup encryption key, S3 credentials, per-node `pgdock_admin` passwords) is encrypted at rest in the metadata DB with a **master key** supplied through an environment variable or file (`PGDOCK_MASTER_KEY`). It is never stored in the database.
- The master key can be rotated with a CLI command that re-encrypts the stored secrets.
- Backups are encrypted **before** upload. Losing the backup key means losing the backups, so the UI prompts the operator to export and store it offline at setup.

### 7.4 Extension allow-list (initial)

| Shared tier | Dedicated tier (adds) |
| --- | --- |
| `pgcrypto`, `uuid-ossp`, `citext`, `pg_trgm`, `hstore`, `unaccent`, `btree_gin`, `btree_gist`, `pg_stat_statements` (read via control plane), `vector` (pgvector) | `postgis`, `pg_partman`, `timescaledb` (licence check), `postgres_fdw`, `pg_cron` |

Extensions that need `shared_preload_libraries` are enabled cluster-wide on the shared tier only if they are safe to share (e.g. `pg_stat_statements`). Otherwise they are dedicated-only.

### 7.5 Audit log

Every mutating API call writes an audit row with the operator, action, target, IP, user agent, timestamp, and outcome. The log is append-only and viewable and filterable in the UI.

---

## 8. Web UI

A single-page app served by `pgdock-server` at the root path. All data comes from `/api/v1`.

### 8.1 Screen map

```
/login, /setup (first-run wizard)
/projects                     — list
/projects/new                 — create
/projects/import              — import from an existing database
/projects/:id                 — overview
/projects/:id/connect         — connection strings & snippets
/projects/:id/sql             — SQL console
/projects/:id/tables          — table browser (read-only)
/projects/:id/backups         — backups & restore
/projects/:id/metrics         — charts
/projects/:id/settings        — guardrails, extensions, rotate password, promote, delete
/nodes, /nodes/:id            — node list & health, register node
/operations                   — job history with live logs
/audit                        — audit log
/settings                     — storage, domain/TLS, backup key, operators
```

### 8.2 First-run setup wizard

1. Create the owner account and enrol TOTP.
2. Set the DB hostname (e.g. `db.yourdomain.com`) and verify DNS points at the control node.
3. Configure S3 storage (endpoint, bucket, keys). The wizard runs a live write/read/delete test.
4. Generate the backup encryption key, which must be downloaded and confirmed before continuing.
5. Register the local node, running agent auto-registration on the same host.
6. Done. Offers "Create your first project".

### 8.3 Projects list

Table columns: name, tier badge (Shared/Dedicated), status, size, active connections, last backup (with a warning if older than 26 hours), and created date. Includes search, filter by tier and status, and a prominent **New project** button.

### 8.4 Create project

The form has a name, an optional description, and the tier (shared by default; dedicated needs a node and profile). Submitting shows live operation progress. On success, a **one-time credential panel** shows the password and both connection strings, with copy buttons and an explicit "I've saved this" confirmation.

### 8.5 SQL console

- CodeMirror editor with SQL highlighting, run-selection, and `Ctrl/Cmd+Enter`.
- The server opens an admin connection to the project DB and runs `SET ROLE <project_owner>` so queries have exactly the project's permissions and no more.
- Statement timeout of 30s by default. A **cancel** button sends `pg_cancel_backend`.
- Results are capped at 1,000 rows in the grid, with CSV export of the displayed rows.
- A per-browser query history is kept in local state. Nothing is stored server-side except an audit entry saying the console was used, without the query text.
- A banner warns that queries run against the live database.
- **Read-only toggle per project** (in project settings). When on, every console query runs inside `BEGIN READ ONLY … ROLLBACK/COMMIT`, so writes and DDL fail with a clear message. Defaults: **off** for shared-tier projects (quick fixes on hobby databases), **on** for dedicated projects, since those are usually production. Promotion switches the toggle on; the operator can turn it off again. Changing the toggle is audited.

### 8.6 Table browser (read-only)

A schema tree (schemas → tables/views), plus a table view with columns, types, indexes, row estimate, and size. Data is shown in a paginated grid (50 rows per page, keyset pagination on the primary key where available). No editing in V1.

### 8.7 Metrics

Per project, over 1h/24h/7d windows:

- DB size over time (sampled every 5 min).
- Active and idle connections, read from `pg_stat_activity` and PgBouncer `SHOW POOLS`.
- Transactions per second, from `xact_commit` + `xact_rollback` deltas.
- Cache hit ratio.
- Top 10 queries by total time (from `pg_stat_statements`, when enabled).

Per node: CPU, memory, disk usage, and disk I/O, reported by the agent every 30s.

Metrics are stored in the metadata DB as a simple time-series table with downsampling (1-minute points for 24 hours, then 1-hour points for 30 days). No Prometheus dependency in V1. A `/metrics` Prometheus endpoint is exposed for anyone who wants it.

### 8.8 Alerts (minimal)

Configurable **webhook** plus optional **SMTP email**. Alerts fire for: backup failed or overdue, restore test failed, node disk above 85%, node unreachable for 2+ minutes, project over its disk warning, and pooler down.

### 8.9 Design notes

- Clean, dense, developer-oriented, with dark and light themes.
- Every destructive action uses a typed-confirmation modal.
- Long operations never block the UI. They appear as progress toasts that link to `/operations/:id`.
- Responsive enough for phone use (check status, copy a connection string), but desktop-first.

---

## 9. Data Model (metadata DB)

```sql
-- Operators & sessions
CREATE TABLE operators (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email         citext UNIQUE NOT NULL,
  password_hash text NOT NULL,              -- argon2id
  totp_secret   bytea,                      -- encrypted with master key
  role          text NOT NULL CHECK (role IN ('owner','member')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  disabled_at   timestamptz
);

CREATE TABLE sessions (
  id            text PRIMARY KEY,           -- random 256-bit, hashed at rest
  operator_id   uuid NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_seen_at  timestamptz NOT NULL DEFAULT now(),
  reauth_at     timestamptz,                -- last step-up auth
  ip            inet, user_agent text
);

-- Infrastructure
CREATE TABLE nodes (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name            text UNIQUE NOT NULL,
  private_addr    inet NOT NULL,
  agent_port      int  NOT NULL DEFAULT 7070,
  role            text NOT NULL CHECK (role IN ('shared','dedicated','both')),
  agent_cert_fp   text NOT NULL,            -- pinned client cert fingerprint
  pg_admin_secret bytea,                    -- encrypted; for shared cluster
  capacity        jsonb NOT NULL,           -- cpu, mem, disk reported by agent
  status          text NOT NULL DEFAULT 'healthy',
  last_heartbeat  timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE instances (                    -- a Postgres server: shared cluster or dedicated
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  node_id       uuid NOT NULL REFERENCES nodes(id),
  kind          text NOT NULL CHECK (kind IN ('shared','dedicated')),
  pg_version    int  NOT NULL,
  port          int  NOT NULL,
  container_id  text,                       -- dedicated only
  cpu_limit     numeric, mem_limit_mb int, volume_gb int,
  status        text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);

-- Backup storage (V1: one default target; per-project targets in V1.1)
CREATE TABLE storage_targets (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text UNIQUE NOT NULL,
  endpoint    text NOT NULL,
  bucket      text NOT NULL,
  prefix      text NOT NULL DEFAULT '',
  credentials bytea NOT NULL,              -- encrypted with master key
  is_default  boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ON storage_targets (is_default) WHERE is_default;

-- Projects
CREATE TABLE projects (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name            text NOT NULL,
  slug            text NOT NULL,
  db_name         text UNIQUE NOT NULL,     -- e.g. blog_k2f9
  owner_role      text NOT NULL,            -- e.g. blog_k2f9_owner
  scram_verifier  text NOT NULL,            -- for pooler auth_file
  tier            text NOT NULL CHECK (tier IN ('shared','dedicated')),
  instance_id     uuid NOT NULL REFERENCES instances(id),
  status          text NOT NULL,            -- provisioning|active|promoting|restoring|deleting|deleted|error
  settings        jsonb NOT NULL DEFAULT '{}', -- conn limit, timeouts, pool size, disk warn, console_read_only
  storage_target_id uuid NOT NULL REFERENCES storage_targets(id),
  extensions      text[] NOT NULL DEFAULT '{}',
  description     text,
  created_by      uuid REFERENCES operators(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  deleted_at      timestamptz
);

-- Jobs
CREATE TABLE operations (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind         text NOT NULL,               -- create|import|delete|backup|restore|promote|rotate|enable_ext|restore_test
  project_id   uuid REFERENCES projects(id),
  params       jsonb NOT NULL DEFAULT '{}',
  status       text NOT NULL DEFAULT 'queued',
  attempts     int  NOT NULL DEFAULT 0,
  run_after    timestamptz NOT NULL DEFAULT now(),
  locked_by    text, locked_at timestamptz,
  log          jsonb NOT NULL DEFAULT '[]', -- [{ts, step, level, msg}]
  error        text,
  created_by   uuid REFERENCES operators(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  finished_at  timestamptz
);
CREATE INDEX ON operations (status, run_after);

-- Backups
CREATE TABLE backups (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid NOT NULL REFERENCES projects(id),
  kind         text NOT NULL CHECK (kind IN ('logical','base','final','safety')),
  object_key   text NOT NULL,
  size_bytes   bigint, checksum text,
  started_at   timestamptz NOT NULL, finished_at timestamptz,
  status       text NOT NULL,
  expires_at   timestamptz
);

-- Metrics
CREATE TABLE metric_points (
  scope      text NOT NULL,                 -- 'project' | 'node'
  scope_id   uuid NOT NULL,
  metric     text NOT NULL,
  ts         timestamptz NOT NULL,
  resolution text NOT NULL,                 -- '1m' | '1h'
  value      double precision NOT NULL,
  PRIMARY KEY (scope, scope_id, metric, resolution, ts)
);

-- Audit & settings
CREATE TABLE audit_log (
  id          bigserial PRIMARY KEY,
  operator_id uuid REFERENCES operators(id),
  action      text NOT NULL,
  target_type text, target_id text,
  detail      jsonb NOT NULL DEFAULT '{}',
  ip          inet, user_agent text,
  outcome     text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settings (
  key   text PRIMARY KEY,
  value jsonb NOT NULL,                     -- secrets inside are encrypted blobs
  updated_at timestamptz NOT NULL DEFAULT now()
);
```

---

## 10. API (v1)

JSON over HTTPS, with session cookie auth and CSRF headers on mutations. Long actions return `202 Accepted` with an `operation_id`.

| Method | Path | Description |
| --- | --- | --- |
| POST | `/api/v1/auth/login` | Email + password, which returns a TOTP challenge |
| POST | `/api/v1/auth/totp` | Complete login |
| POST | `/api/v1/auth/reauth` | Step-up auth for destructive actions |
| POST | `/api/v1/auth/logout` |  |
| GET | `/api/v1/me` | Current operator |
| GET | `/api/v1/projects` | List (filter by tier, status, q) |
| POST | `/api/v1/projects` | Create → `202` + operation |
| POST | `/api/v1/projects/import/preflight` | `{source_url}` → version, size, schemas, extensions, warnings |
| POST | `/api/v1/projects/import` | `{source_url, name, tier, schemas}` → `202` + operation |
| GET | `/api/v1/projects/:id` | Detail + connection info (no password) |
| PATCH | `/api/v1/projects/:id` | Update name, description, settings (including `console_read_only`) |
| DELETE | `/api/v1/projects/:id` | Delete (reauth) → `202` |
| POST | `/api/v1/projects/:id/rotate-password` | Returns the new password once |
| POST | `/api/v1/projects/:id/extensions` | `{name}` → enable |
| POST | `/api/v1/projects/:id/promote` | `{node_id, profile}` → `202` |
| GET | `/api/v1/projects/:id/backups` | List |
| POST | `/api/v1/projects/:id/backups` | On-demand backup → `202` |
| POST | `/api/v1/projects/:id/restore` | `{backup_id \| target_time, mode}` → `202` |
| POST | `/api/v1/projects/:id/sql` | `{query}` → result rows (capped) |
| POST | `/api/v1/projects/:id/sql/cancel` | Cancel the running console query |
| GET | `/api/v1/projects/:id/schema` | Schemas, tables, columns |
| GET | `/api/v1/projects/:id/tables/:schema/:table/rows` | Paginated rows |
| GET | `/api/v1/projects/:id/metrics` | `?metric=&range=` |
| GET | `/api/v1/nodes` · POST · GET `/:id` · DELETE `/:id` | Node management; POST returns a one-time registration token |
| GET | `/api/v1/operations` · GET `/:id` · GET `/:id/stream` (SSE) | Jobs + live logs |
| GET | `/api/v1/audit` | Audit log |
| GET/PUT | `/api/v1/settings/*` | Storage, domain, alerts, operators |
| GET | `/metrics` | Prometheus exposition (optionally protected by token) |
| GET | `/healthz`, `/readyz` | Liveness and readiness |

A thin **CLI** (`pgdock`) calling the same API is a stretch goal for V1 and can use personal API tokens (see §12).

### Agent API (internal, mTLS only)

`POST /v1/instances` (create container) · `DELETE /v1/instances/:id` · `POST /v1/instances/:id/restart` · `POST /v1/dump` · `POST /v1/restore` · `POST /v1/walg/backup` · `POST /v1/walg/fetch` · `GET /v1/host/metrics` · `GET /v1/health`

Registration: `pgdock-agent register --server https://… --token <one-time>` creates a keypair, sends a CSR, receives a signed client cert, and pins the server CA.

---

## 11. Operations & Deployment

### 11.1 Install (single VPS)

```
curl -fsSL https://…/install.sh | sh     # or docker compose up
```

The install provides: `pgdock-server` (systemd), metadata Postgres, the shared cluster Postgres, two PgBouncer units, Caddy, and `pgdock-agent`, all on one host. Ship both a **Docker Compose** bundle and a **bare-metal systemd** path. Compose is the recommended default.

### 11.2 Shared cluster tuning defaults (8 GB host)

`shared_buffers=2GB`, `effective_cache_size=6GB`, `work_mem=8MB`, `maintenance_work_mem=256MB`, `max_connections=500`, `wal_compression=on`, `checkpoint_timeout=15min`, `autovacuum_max_workers=5`. The pooler keeps actual backend connections far below `max_connections`.

### 11.3 Upgrades

- **pgdock-server/agent:** a single binary swap. Metadata migrations run on start with `goose`, and the server refuses to start if the agent version is incompatible.
- **Postgres minor versions:** a rolling restart per instance from the UI (dedicated) or a scheduled window (shared).
- **Postgres major versions:** V1 runs PostgreSQL 18 everywhere. Major upgrades and multi-version support arrive in V1.1, which will reuse the promotion machinery to move a project into an instance on a newer version.

### 11.4 Hosting

- **Nodes on Hetzner.** Best price per spec for the shared cluster and dedicated instances. App servers that use PGDock databases should run in the **same Hetzner region**, because app-to-database latency is what users feel.
- **Backups on Cloudflare R2.** No egress fees, so restores, imports from backups, and the weekly restore tests cost nothing beyond storage.
- **Data residency.** A project that becomes a regulated Nigerian fintech may need its data hosted in Nigeria; check the current CBN and NDPA requirements at that point. The design already supports it: register a Nigeria-hosted node and promote the project onto it. A per-project R2 bucket in a suitable jurisdiction (V1.1) covers its backups.

### 11.5 Repository layout & build

PGDock is a **monorepo with one Go module**. The server, agent, and web UI are versioned and released together, and the UI is embedded into `pgdock-server` with `go:embed`, so each release is one consistent server + agent + UI set.

```
pgdock/
├── go.mod                  # one Go module for server + agent
├── Makefile                # dev, generate, build, test, release
├── api/
│   └── openapi.yaml        # single source of truth for the HTTP API
├── cmd/
│   ├── server/main.go      # pgdock-server
│   └── agent/main.go       # pgdock-agent
├── internal/
│   ├── api/                # chi handlers implementing generated interfaces
│   ├── auth/               # argon2id, TOTP, sessions, CSRF, reauth
│   ├── jobs/               # operation queue, workers, SSE streaming
│   ├── provision/          # create / delete / rotate / import / promote flows
│   ├── pooler/             # PgBouncer config rendering + admin console client
│   ├── backup/             # logical dumps, WAL-G orchestration, retention
│   ├── agentclient/        # mTLS client used by the server
│   ├── agentsvc/           # the agent's HTTP service
│   ├── crypto/             # master-key encryption, SCRAM verifiers, agent CA
│   ├── metrics/            # collectors, downsampling, /metrics
│   ├── config/
│   └── store/              # sqlc-generated code
│       ├── migrations/     # goose
│       └── queries/
├── web/
│   ├── package.json
│   ├── vite.config.ts
│   ├── src/                # React app; API types generated from api/openapi.yaml
│   ├── dist/               # build output (gitignored except a placeholder)
│   └── embed.go            # package web — //go:embed all:dist
├── deploy/
│   ├── compose/            # Docker Compose bundle (recommended install)
│   ├── images/postgres/    # PostgreSQL 18 + WAL-G image
│   ├── systemd/            # bare-metal units
│   └── install.sh
├── test/
│   ├── integration/        # testcontainers-go
│   ├── isolation/          # tenant-escape suite
│   ├── fixtures/           # incl. Supabase-shaped database for import tests
│   └── e2e/                # Playwright
└── docs/
```

**Embedding.** `go:embed` cannot reference parent directories, so `cmd/server` cannot embed `web/dist` directly. Instead, `web/embed.go` makes `web` a Go package that embeds its own `dist` folder and exports it as an `fs.FS`. `pgdock-server` imports it and serves it at `/` with an SPA fallback: any non-`/api` path that doesn't match a file returns `index.html`, so client-side routes survive a page refresh. Static assets with hashed filenames get long-lived cache headers, and `index.html` gets `no-cache`.

**Placeholder build.** A committed `web/dist/index.html` placeholder lets backend-only work compile without Node installed. Release builds always overwrite it with the real UI, and the release pipeline fails if the placeholder is still present in the built binary.

**Makefile targets**

| Target | What it does |
| --- | --- |
| `make dev` | Runs the Go server with live reload (`air`) and the Vite dev server together. Vite proxies `/api` to Go, so the UI hot-reloads without rebuilding Go. |
| `make generate` | Regenerates `sqlc` code, Go server interfaces (`oapi-codegen`), and TypeScript API types (`openapi-typescript`). |
| `make build` | `npm ci && npm run build` in `web/`, then `go build` for `pgdock-server` (UI embedded) and `pgdock-agent`. Version, commit, and build date injected via `-ldflags`. |
| `make test` | Go unit tests, frontend type-check and unit tests. |
| `make test-integration` | Integration and isolation suites against real Postgres + PgBouncer via testcontainers. |
| `make release` | Cross-compiles `linux/amd64` and `linux/arm64`, builds the Postgres image, produces the Compose bundle and checksums. |

**CI pipeline (single workflow):** Go lint (`golangci-lint`) and tests → frontend lint, type-check, and tests → **generated-code check** (run `make generate` and fail on any diff) → UI build → binary build → integration and isolation suites → Playwright smoke tests on tagged builds → release artifacts on tags.

**Versioning.** One semantic version for the whole repo. Server and agent report their versions to each other, and the server refuses to schedule work on an agent with an incompatible major version (§11.3).

### 11.6 Self-backup

The metadata DB is backed up nightly to the same object store with the same encryption. The docs include a disaster-recovery runbook: rebuild the control node, restore metadata, re-register agents, and regenerate the pooler config.

---

## 12. Future (post-V1, noted to avoid painting into corners)

- **V1.1:** logical-replication promotion (seconds of downtime), `pgdock` CLI with personal API tokens, per-project extra roles (read-only role), teammates (invitations + `member` role), multiple Postgres major versions and in-place major upgrades, per-project backup buckets in the UI.
- **V1.2:** database branching for dev/test (clone from a backup), scheduled restore to staging, HA for dedicated instances (Patroni or streaming standby).
- **V2:** optional per-project PostgREST, automatic VM provisioning (Hetzner/DigitalOcean APIs), usage-based quotas, team/org separation.

Design choices in V1 that keep these open: stable pooler URLs, the `instances` abstraction separate from `projects`, and all long actions as resumable operations.

---

## 13. Testing Strategy

| Layer | Approach |
| --- | --- |
| Unit | Go tests for slug generation, SCRAM verifier generation, pooler config rendering, retention policy maths, and state machines. |
| Integration | `testcontainers-go` spins up real Postgres + PgBouncer to test create → connect via pooler → rotate → delete end-to-end. |
| Isolation suite | The tenant-escape tests from §7.1, run in CI and weekly against live nodes. |
| Backup/restore | CI round trip: seed data → backup → restore to new project → checksum match. Plus the weekly live restore test. |
| Import | CI imports from Postgres 15, 17, and 18 sources, including a fixture shaped like a Supabase database (managed schemas, `anon`/`authenticated` grants, RLS policies). Asserts the skipped schemas, preflight warnings, ownership, and row counts. |
| Promotion | CI with a live writer: run continuous inserts during promotion, then assert zero lost committed rows and that the client's URL still works afterwards. |
| Failure injection | Kill the agent mid-dump, kill the pooler, fill the disk, lose S3 connectivity. Assert operations fail cleanly and roll back or resume. |
| UI | Playwright smoke tests for setup wizard, create project, SQL console, restore, and promote. |
| Load | 150 shared projects with `pgbench` on 10 of them. Measure create latency, pooler overhead, and noisy-neighbour impact. |

---

## 14. Build Plan

Estimates assume **one experienced engineer working roughly full time**. The whole plan is about 10 weeks. Each milestone ends with something usable.

### M0 — Foundations (Week 1)

- **Repo setup (§11.5):** monorepo skeleton, single `go.mod`, Makefile with `dev`, `generate`, `build`, and `test` targets, `golangci-lint` config.
- **Web scaffold:** Vite + React + TypeScript project in `web/`, `web/embed.go` with the committed placeholder, SPA fallback handler in the server, Vite proxy to the Go API for `make dev`.
- **API contract:** initial `api/openapi.yaml` (health, version, and auth endpoints), wired to `oapi-codegen` and `openapi-typescript` through `make generate`.
- **CI:** single workflow running lint, tests, the generated-code check, the UI build, and the binary build on every push.
- Config loading, structured logging (`slog`), metadata DB with `goose` migrations, `sqlc` setup.
- Master-key encryption helpers and tests.
- Operation queue with a worker pool (`SKIP LOCKED`), retries, step logging, and SSE streaming.
- Docker Compose dev environment: metadata PG, shared PG, PgBouncer ×2.

**Done when:** `make build` produces a `pgdock-server` binary that serves the placeholder React app at `/` and `GET /api/v1/version` from the same origin; CI is green; and a dummy operation can be enqueued, run, and streamed to `curl`.

### M1 — Shared-tier provisioning via API (Week 2)

- Create, delete, and rotate flows (§6.1, §6.2) with compensating rollback.
- Pooler config rendering + atomic write + `RELOAD`/`PAUSE`/`RESUME`.
- SCRAM passthrough auth working end-to-end.
- Isolation hardening on create, plus the first version of the isolation test suite.

**Done when:** `POST /projects` returns a URL that works in `psql`, and the isolation tests pass.

### M2 — Auth, TLS & the web UI shell (Weeks 3–4)

- Operator auth for a single owner: argon2id, TOTP, sessions, CSRF, step-up reauth, rate limiting.
- First-run setup wizard (without S3 for now).
- Pooler TLS with a Let's Encrypt certificate. Caddy in front of the UI.
- React app scaffold embedded via `go:embed`, design system basics, routing, API client.
- Screens: login, setup, projects list, create project (with one-time credential panel), project overview, connect page, settings (guardrails, rotate, delete), operations list/detail with live logs.
- Audit log writes and viewer.

**Done when:** you can go from a fresh VPS to a working database entirely through the browser. **Start dogfooding here** with your real hobby projects.

### M3 — Backups & restore (Week 5)

- Agent v0: mTLS registration, health, host metrics, `dump`/`restore` commands.
- S3 settings in the wizard with a live test. Backup key generation and download.
- Nightly scheduled logical backups with jitter, encryption, and retention.
- Restore into new project and restore in place.
- **Import from an existing database** (§6.8): preflight, schema selection with Supabase defaults, dump/restore with ownership remap, verification. About one extra day, since it reuses the restore path.
- Weekly automated restore test and metadata self-backup.
- UI: backups tab, restore dialogs, and the last-backup indicator in the list.

**Done when:** you can delete data, restore to a new project, and verify it, and one of your real Supabase projects has been imported and is serving its app.

### M4 — Nodes & dedicated instances (Week 6)

- Agent Docker management: create, start, stop, and destroy a dedicated container with limits and a volume. Build and pin a custom image that includes WAL-G.
- Node registration UI, node health page, and multi-node shared placement.
- Create a project directly on the dedicated tier.
- WAL-G archiving, daily base backups, PITR restore into a new project.

**Done when:** a dedicated project works through the same pooler URL format and PITR restore succeeds.

### M5 — Promotion (Week 7)

- Promotion flow (§6.6) with freeze, dump/restore, verification, route swap, and rollback.
- Keep the source read-only for 48 hours, then auto-drop.
- Downtime estimate in the UI. Promotion wizard.
- CI promotion test with a live writer.

**Done when:** a real hobby project is promoted with its URL unchanged and no lost commits.

### M6 — Console, browser & metrics (Week 8)

- SQL console: `SET ROLE` execution, timeout, cancel, result cap, CSV export, per-project read-only toggle.
- Read-only schema tree and table browser with keyset pagination.
- Metrics collector (project and node), downsampling job, charts, and the `/metrics` endpoint.
- Extension allow-list UI.

**Done when:** you can inspect and query any project and see its size and connection trends without leaving the UI.

### M7 — Alerts, hardening & release (Weeks 9–10)

- Alerts via webhook and SMTP for every trigger in §8.8.
- Failure-injection tests and a fix-up pass.
- Load test with 150 projects and a tuning pass.
- Playwright smoke tests.
- Security review against §7 (checklist sign-off). Dependency audit.
- Install script, Compose bundle, docs: install, operate, disaster-recovery runbook, upgrade guide.
- Tag **v1.0.0**.

**Done when:** a clean VPS can be installed from the docs by someone other than you, and every checklist item in §7.1 is automated.

### Timeline summary

| Week | Milestone | Usable outcome |
| --- | --- | --- |
| 1 | M0 Foundations | Job engine + dev env |
| 2 | M1 Shared provisioning | Databases via API |
| 3–4 | M2 Auth + UI | **Dogfood-ready via browser** |
| 5 | M3 Backups + import | Safe to trust with real data; Supabase projects moved across |
| 6 | M4 Dedicated | Serious projects supported |
| 7 | M5 Promotion | Hobby → startup path works |
| 8 | M6 Console + metrics | Supabase-like convenience |
| 9–10 | M7 Hardening | v1.0.0 |

**Critical path:** M0 → M1 → M3 (agent) → M4 → M5. The UI (M2, M6) can run in parallel if a second person, or a frontend-focused block of time, is available, which would compress the plan to about 7–8 weeks.

---

## 15. Risks & Mitigations

| Risk | Impact | Mitigation |
| --- | --- | --- |
| **Edge pooler is a single point of failure** | All projects unreachable | systemd auto-restart and health alerts in V1. Document a second pooler behind DNS/keepalived for V1.2. PgBouncer is very stable in practice. |
| **Noisy neighbour on the shared tier** | One project slows the rest | Connection limits, statement timeouts, and pool sizes per project. Metrics make offenders visible. Promotion is the escape hatch. |
| **Disk full on the shared node** | Cluster-wide outage | Disk alerts at 85%, per-project size warnings, and WAL/temp file monitoring. Keep 20% headroom on node sizing. |
| **Untested backups** | Data loss at the worst moment | Automated weekly restore tests with alerting. The backup key export is forced at setup. |
| **Lost master or backup key** | Unrecoverable secrets/backups | Forced download and confirmation at setup, a warning banner if never re-confirmed, and a documented offline storage practice. |
| **SQL console abuse / session hijack** | Direct data access | 2FA, strict cookies, `SET ROLE` scoping, audit entries, short timeouts. The console can be disabled globally. |
| **Promotion downtime too long for large DBs** | Visible outage | Downtime estimate shown upfront. Large DBs wait for the V1.1 logical-replication path. |
| **PgBouncer transaction mode breaks some apps** | Confusing client errors | The session-mode port is always available. The connect page explains when to use which, with ORM-specific snippets (Prisma, Drizzle, GORM, sqlc/pgx, SQLAlchemy). |
| **Scope creep toward "full Supabase"** | V1 never ships | Non-goals in §1.4 are explicit. New ideas go to §12. |

---

## 16. Decisions Log

| # | Question | Decision | Rationale |
| --- | --- | --- | --- |
| 1 | Product name | Keep **PGDock** as the internal codename; choose the public name before v1.0. | A name only matters once others see the tool. |
| 2 | UI stack | **React + TypeScript + Vite**, embedded via `go:embed`. | The SQL console, charts, and live logs benefit most from a rich client; single-binary deploy is kept. |
| 3 | Hosting | **Hetzner** nodes, **Cloudflare R2** backups. | Best price per spec; no egress fees on restores. Nigeria-hosted node available later for regulated projects (§11.4). |
| 4 | Operators | **Single owner** in V1; `role` column kept for V1.1 teammates. | Saves 2–3 days in M2. |
| 5 | Postgres version | **PostgreSQL 18 only**, both tiers. | One image, one extension build set, one tuning profile. Older sources import cleanly. |
| 6 | SQL console writes | **Writes allowed**, per-project read-only toggle. Default off for shared, on for dedicated (and switched on at promotion). | Quick fixes on hobby DBs; safer defaults for production-like projects. |
| 7 | Backup storage | **One default bucket** in V1; `storage_targets` table and per-project reference in the schema now. | Lets a project take ownership of its backups later without a migration. |
| — | Added scope | **Import from an existing database** (§6.8), in M3. | Moving existing Supabase projects is the first real use; reuses restore code (\~1 day). |