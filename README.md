# PGDock

Self-hosted managed PostgreSQL with a web UI. See
[the V1 specification & build plan](./PGDock%20—%20V1%20Specification%20&%20Build%20Plan.md).

## Requirements

- Go 1.25+
- Node.js 22+ (only for building the UI; backend-only work compiles without it)
- [`golangci-lint`](https://golangci-lint.run/) v2 for `make lint`
- [`air`](https://github.com/air-verse/air) for `make dev`

## Common tasks

| Command | What it does |
| --- | --- |
| `make build` | Builds the UI, then `bin/pgdock-server` (UI embedded) and `bin/pgdock-agent`. |
| `make dev` | Go server with live reload plus the Vite dev server (proxies `/api` to Go). |
| `make generate` | Regenerates Go server interfaces and TypeScript types from `api/openapi.yaml`. |
| `make test` | Go unit tests, frontend type-check and unit tests. |
| `make lint` | `golangci-lint`. |
| `make release-check` | Fails if the server binary embeds only the placeholder UI. |

```sh
make build
PGDOCK_LISTEN_ADDR=127.0.0.1:8080 ./bin/pgdock-server
curl http://127.0.0.1:8080/api/v1/version
```

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `PGDOCK_LISTEN_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `PGDOCK_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `PGDOCK_LOG_FORMAT` | `json` | `json` or `text` |
| `PGDOCK_SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown limit |
