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
| `make lint` | `golangci-lint`. |
| `make release-check` | Fails if the server binary embeds only the placeholder UI. |

```sh
make dev-up build dev-key
PGDOCK_DATABASE_URL='postgres://pgdock:pgdock@127.0.0.1:5440/pgdock?sslmode=disable' \
PGDOCK_MASTER_KEY_FILE=tmp/dev-master.key \
PGDOCK_DEV_ENDPOINTS=true \
  ./bin/pgdock-server

curl http://127.0.0.1:8080/api/v1/version

# Enqueue a dummy operation and stream its progress (Server-Sent Events).
ID=$(curl -s -X POST localhost:8080/api/v1/dev/operations \
  -d '{"steps": 5, "delay_ms": 500, "fail_attempts": 1}' | jq -r .id)
curl -N localhost:8080/api/v1/operations/$ID/stream
```

Migrations run automatically on start.

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

> Until auth lands in M2, the API is unauthenticated. Keep the default
> loopback listen address.
