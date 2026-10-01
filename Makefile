# PGDock build entry points. See §11.5 of the spec.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

MODULE  := github.com/israel-duff/pgdock
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildDate=$(DATE)

BIN := bin

COMPOSE := docker compose -f deploy/dev/compose.yaml
# Matches deploy/dev/compose.yaml defaults.
DEV_DATABASE_URL ?= postgres://pgdock:pgdock@127.0.0.1:$${PGDOCK_DEV_METADATA_PORT:-5440}/pgdock?sslmode=disable
DEV_MASTER_KEY_FILE := tmp/dev-master.key

.PHONY: all dev dev-up dev-down dev-key test-db generate check-generated build build-ui build-go test test-go test-web lint release-check clean clean-ui

all: build

## dev: Go server with live reload (air) + Vite dev server; Vite proxies /api to Go.
## Requires air (go install github.com/air-verse/air@latest) and `make dev-up`.
dev: dev-key
	@command -v air >/dev/null || { echo "air not found: go install github.com/air-verse/air@latest"; exit 1; }
	@trap 'kill 0' EXIT; \
	PGDOCK_DATABASE_URL="$(DEV_DATABASE_URL)" \
	PGDOCK_MASTER_KEY_FILE="$(DEV_MASTER_KEY_FILE)" \
	PGDOCK_DEV_ENDPOINTS=true \
	PGDOCK_LOG_FORMAT=text \
	air -c .air.toml & \
	(cd web && npm run dev) & \
	wait

## dev-up: start the dev environment (metadata PG, shared PG, PgBouncer x2).
dev-up:
	$(COMPOSE) up -d --wait

## dev-down: stop the dev environment (data volumes are kept).
dev-down:
	$(COMPOSE) down

# A throwaway master key for local development, kept out of git in tmp/.
dev-key: $(DEV_MASTER_KEY_FILE)
$(DEV_MASTER_KEY_FILE):
	@mkdir -p $(@D)
	go run ./cmd/server -gen-master-key > $@
	@chmod 600 $@

## generate: regenerate sqlc queries, Go server interfaces, and TypeScript API types.
generate:
	go generate ./...
	cd web && npm run generate

## check-generated: fail if generated code is out of date (used by CI).
check-generated: generate
	@git diff --exit-code -- internal/api/gen internal/store web/src/api/schema.d.ts \
		|| { echo "generated code is stale; run 'make generate' and commit"; exit 1; }

## build: build the UI, then pgdock-server (UI embedded) and pgdock-agent.
build: build-ui build-go

build-ui: clean-ui
	cd web && npm ci && npm run build

build-go:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/pgdock-server ./cmd/server
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN)/pgdock-agent ./cmd/agent

## release-check: fail if the server binary embeds only the placeholder UI.
release-check:
	$(BIN)/pgdock-server -require-ui

## test: Go unit tests, frontend type-check and unit tests.
test: test-go test-web

# Tests that need Postgres run when PGDOCK_TEST_DATABASE_URL is set and
# skip otherwise; `make test-db` points them at the dev environment.
test-go:
	go test -race ./...

## test-db: Go tests including the Postgres-backed ones, against `make dev-up`.
test-db: dev-up
	PGDOCK_TEST_DATABASE_URL="$(DEV_DATABASE_URL)" go test -race -count=1 ./...

test-web:
	cd web && npm run typecheck && npm test

lint:
	golangci-lint run ./...

## clean-ui: remove UI build output, keeping the committed placeholder.
clean-ui:
	find web/dist -mindepth 1 ! -name placeholder.html -delete

clean: clean-ui
	rm -rf $(BIN) tmp
