# PGDock

Self-hosted managed PostgreSQL with a web UI. See
[the V1 specification & build plan](./PGDock%20—%20V1%20Specification%20&%20Build%20Plan.md).

## Requirements

- Go 1.25+
- Node.js 22+ (only for building the UI; backend-only work compiles without it)
- [`golangci-lint`](https://golangci-lint.run/) v2 for `make lint`
- [`air`](https://github.com/air-verse/air) for `make dev`
- Docker with Compose for the dev environment

## Common tasks

| Command | What it does |
| --- | --- |
| `make build` | Builds the UI, then `bin/pgdock-server` (UI embedded) and `bin/pgdock-agent`. |
| `make dev-up` / `make dev-down` | Start/stop the dev environment: metadata PG (`:5440`), shared PG 18 (`:5441`), PgBouncer session (`:6432`) and transaction (`:6543`) poolers. |
| `make dev` | Go server with live reload plus the Vite dev server (proxies `/api` to Go), wired to the dev environment. |
| `make generate` | Regenerates Go server interfaces and TypeScript types from `api/openapi.yaml`. |
| `make test` | Go unit tests, frontend type-check and unit tests. Postgres-backed tests skip unless `PGDOCK_TEST_DATABASE_URL` is set. |
| `make test-db` | Go tests including the Postgres-backed ones, against the dev environment. |
| `make test-integration` | Provisioning end to end and the tenant-isolation suite, against real Postgres 18 and PgBouncer. |
| `make run-dev` | Runs the built `bin/pgdock-server` against the dev environment (`deploy/dev/server.env`). |
| `make lint` | `golangci-lint`. |
| `make release-check` | Fails if the server binary embeds only the placeholder UI. |

```sh
make dev-up build
make run-dev        # bin/pgdock-server with deploy/dev/server.env

curl http://127.0.0.1:8080/api/v1/version

# Create a shared-tier project. The response carries the password and
# connection URLs exactly once; they work when the operation succeeds.
curl -s -X POST localhost:8080/api/v1/projects -d '{"name": "My Blog"}' | jq
psql 'postgresql://my_blog_k2f9_owner:<password>@127.0.0.1:6543/my_blog_k2f9?sslmode=disable'

# Enqueue a dummy operation and stream its progress (Server-Sent Events).
ID=$(curl -s -X POST localhost:8080/api/v1/dev/operations \
  -d '{"steps": 5, "delay_ms": 500, "fail_attempts": 1}' | jq -r .id)
curl -N localhost:8080/api/v1/operations/$ID/stream
```

Migrations run automatically on start.

## Projects (shared tier)

`POST /api/v1/projects` records the project and queues a `create` operation
(spec §6.1). It creates the owner role and database on the shared cluster,
hardens them (§7.1), adds the pooler route and SCRAM auth entry, reloads
both PgBouncers, and smoke-tests a login through each before marking the
project active. Each step is idempotent; if the operation finally fails, a
rollback drops everything it created. `rotate-password` and `DELETE` work the
same way. PGDock stores only SCRAM verifiers: the pooler authenticates to
the backend with SCRAM passthrough, so no plaintext password is kept.

The isolation suite (`test/isolation`) creates two projects and asserts that
neither can reach the other's database, objects, sessions, or the server's
files. Decisions that depart from the spec are in
[docs/decisions.md](docs/decisions.md).

## Operations

Long actions run as rows in the `operations` table (spec §6). Workers claim
them with `FOR UPDATE SKIP LOCKED`, so any number of servers can share the
queue. Each attempt heartbeats its lease; an operation whose worker dies is
requeued after the lease expires (30s) and resumes elsewhere. Failures retry
with exponential backoff up to the kind's attempt limit; handlers return
`jobs.Permanent(err)` to fail immediately. A table trigger publishes changes
with `NOTIFY`, which wakes idle workers and drives the SSE stream at
`/api/v1/operations/{id}/stream`.

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `PGDOCK_LISTEN_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `PGDOCK_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `PGDOCK_LOG_FORMAT` | `json` | `json` or `text` |
| `PGDOCK_SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown limit |
| `PGDOCK_DATABASE_URL` | *required* | Metadata DB connection string |
| `PGDOCK_MASTER_KEY` / `PGDOCK_MASTER_KEY_FILE` | *required (one)* | Base64 32-byte key encrypting secrets at rest. Generate with `pgdock-server -gen-master-key`. |
| `PGDOCK_MASTER_KEY_PREVIOUS` | | Comma-separated old keys that stay readable during a rotation |
| `PGDOCK_WORKERS` | `4` | Operations run concurrently |
| `PGDOCK_DEV_ENDPOINTS` | `false` | Enables `/api/v1/dev/*`. Never in production. |
| `PGDOCK_SHARED_ADMIN_URL` | | Shared cluster superuser (`pgdock_admin`) URL; registers the cluster at startup |
| `PGDOCK_SHARED_NODE_NAME` | `local` | Name of that node |
| `PGDOCK_SHARED_POOLER_HOST` / `_PORT` | admin URL's | How the poolers reach the cluster |
| `PGDOCK_POOLER_CONFIG_DIR` | | Where `databases.ini` and `userlist.txt` are written; enables provisioning |
| `PGDOCK_POOLER_FILE_MODE` | `0640` | Mode of the generated files |
| `PGDOCK_POOLER_ADMIN_USER` | `pgdock` | PgBouncer admin console user |
| `PGDOCK_POOLER_ADMIN_PASSWORD` / `_FILE` | *required with the dir* | Its password |
| `PGDOCK_POOLER_SESSION_ADDR` / `_POOLED_ADDR` | public host and ports | The two poolers as pgdock-server reaches them |
| `PGDOCK_POOLER_SSLMODE` | `prefer` | For admin and smoke-test connections |
| `PGDOCK_DB_HOST` | `localhost` | Host in client connection strings, e.g. `db.example.com` |
| `PGDOCK_DB_SESSION_PORT` / `_POOLED_PORT` | `5432` / `6543` | Ports in client connection strings |
| `PGDOCK_DB_SSLMODE` | `require` | `sslmode` in client connection strings |

> Until auth lands in M2, the API is unauthenticated. Keep the default
> loopback listen address.
