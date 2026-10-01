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
POOLER_DIR := tmp/pooler
TEST_POOLER_DIR := tmp/pooler-test
DEV_ENV := deploy/dev/server.env
# Loads $(DEV_ENV); PGDOCK_* variables already set by the caller win.
LOAD_DEV_ENV := saved="$$(export -p | grep ' PGDOCK_' || true)"; set -a; . ./$(DEV_ENV); set +a; eval "$$saved"

.PHONY: all dev run-dev dev-up dev-down dev-key pooler-seed test-db test-integration test-agent-bin pg-image test-acme test-e2e test-docs test-load e2e-images generate check-generated build build-ui build-go test test-go test-web lint release-check release clean clean-ui

all: build

## dev: Go server with live reload (air) + Vite dev server; Vite proxies /api to Go.
## Requires air (go install github.com/air-verse/air@latest) and `make dev-up`.
dev: dev-key
	@command -v air >/dev/null || { echo "air not found: go install github.com/air-verse/air@latest"; exit 1; }
	@trap 'kill 0' EXIT; \
	$(LOAD_DEV_ENV); \
	air -c .air.toml & \
	(cd web && npm run dev) & \
	wait

## run-dev: run the built bin/pgdock-server against the dev environment.
run-dev: dev-key
	@$(LOAD_DEV_ENV); exec $(BIN)/pgdock-server

## dev-up: start the dev environment (metadata PG, shared PG, PgBouncer x2).
dev-up: pooler-seed
	$(COMPOSE) up -d --wait

# The poolers read pgdock-server's generated files from $(POOLER_DIR) (and
# the test poolers from $(TEST_POOLER_DIR)). Seed missing ones so PgBouncer
# can start before the server has run; never overwrite what it wrote.
pooler-seed:
	@for d in $(POOLER_DIR) $(TEST_POOLER_DIR); do \
		mkdir -p $$d; \
		for f in userlist.txt databases.ini; do \
			[ -f $$d/$$f ] || install -m 644 deploy/dev/pgbouncer/bootstrap/$$f $$d/$$f; \
		done; \
		[ -f $$d/server.crt ] || openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
			-days 3650 -subj /CN=localhost -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
			-keyout $$d/server.key -out $$d/server.crt 2>/dev/null; \
		chmod 644 $$d/server.crt $$d/server.key; \
	done

## dev-down: stop the dev environment (data volumes are kept).
dev-down:
	$(COMPOSE) --profile test down

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

## release: linux/amd64 and linux/arm64 binaries (UI embedded), the install
## bundle (source at this commit), and SHA256SUMS, in dist/.
DIST := dist
release: build-ui
	rm -rf $(DIST) && mkdir -p $(DIST)
	for arch in amd64 arm64; do \
		for cmd in server agent; do \
			CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
				-o $(DIST)/pgdock-$$cmd-$(VERSION)-linux-$$arch ./cmd/$$cmd; \
		done; \
	done
	git archive --format=tar.gz --prefix=pgdock-$(VERSION)/ -o $(DIST)/pgdock-$(VERSION).tar.gz HEAD
	cd $(DIST) && sha256sum * > SHA256SUMS
	@ls -l $(DIST)

## test: Go unit tests, frontend type-check and unit tests.
test: test-go test-web

# Tests that need Postgres run when PGDOCK_TEST_DATABASE_URL is set and
# skip otherwise; `make test-db` points them at the dev environment.
test-go:
	go test -race ./...

## test-db: Go tests including the Postgres-backed ones, against `make dev-up`.
test-db: dev-up
	PGDOCK_TEST_DATABASE_URL="$(DEV_DATABASE_URL)" go test -race -count=1 ./...

# Pebble (Let's Encrypt's test CA) for the ACME test, built from source.
PEBBLE_VERSION := v2.10.1
PEBBLE_DIR := tmp/pebble

$(PEBBLE_DIR)/pebble $(PEBBLE_DIR)/pebble-challtestsrv:
	GOBIN=$(CURDIR)/$(PEBBLE_DIR) go install github.com/letsencrypt/pebble/v2/cmd/pebble@$(PEBBLE_VERSION) \
		github.com/letsencrypt/pebble/v2/cmd/pebble-challtestsrv@$(PEBBLE_VERSION)

## test-acme: obtain a real certificate over HTTP-01 from Pebble.
test-acme: $(PEBBLE_DIR)/pebble $(PEBBLE_DIR)/pebble-challtestsrv
	PGDOCK_TEST_PEBBLE_DIR=$(CURDIR)/$(PEBBLE_DIR) go test -count=1 -run TestACMEWithPebble -v ./internal/tlscert/

# ---- End-to-end: the install bundle, driven through a browser --------------
E2E_COMPOSE := docker compose -p pgdock-e2e --env-file $(CURDIR)/test/e2e/bundle/e2e.env \
	-f $(CURDIR)/deploy/compose/compose.yaml -f $(CURDIR)/test/e2e/bundle/compose.e2e.yaml
# Extra flags for image builds, e.g. behind a TLS-intercepting proxy:
#   DOCKER_BUILD_FLAGS='--network host --secret id=ca_bundle,src=/path/ca.pem'
DOCKER_BUILD_FLAGS ?=

## test-e2e: install the compose bundle from scratch and go from a fresh
## install to a working database entirely through the browser (M2 done-when).
# Dedicated instances are created by the agent, outside Compose: remove the
# bundle's (containers on its network, and their volumes).
E2E_INSTANCES := ids=$$(docker ps -aq --filter network=pgdock-e2e --filter label=pgdock.instance); \
	[ -z "$$ids" ] || { vols=$$(docker inspect -f '{{.Name}}' $$ids | tr -d /); docker rm -f $$ids >/dev/null; docker volume rm -f $$vols >/dev/null; }

test-e2e: e2e-images
	@$(E2E_INSTANCES)
	$(E2E_COMPOSE) down -v --remove-orphans >/dev/null 2>&1 || true
	$(E2E_COMPOSE) up -d --no-build --wait
	@mkdir -p tmp && curl -fsSk --noproxy '*' https://127.0.0.1:15000/roots/0 > tmp/e2e-pebble-root.pem
	cd test/e2e && npm ci --silent && \
	PGDOCK_E2E_URL=https://pgdock.test:18443 \
	PGDOCK_E2E_HOST_RULES="MAP pgdock.test 127.0.0.1" \
	PGDOCK_E2E_IGNORE_HTTPS_ERRORS=1 \
	PGDOCK_E2E_SETUP_CODE=e2e-setup-code \
	PGDOCK_E2E_DB_HOST=db.pgdock.test \
	PGDOCK_E2E_DB_ADDR=127.0.0.1 \
	PGDOCK_E2E_DB_CA=$(CURDIR)/tmp/e2e-pebble-root.pem \
	PGDOCK_E2E_EXPECT_ISSUER=Pebble \
	PGDOCK_E2E_S3_ENDPOINT=http://fakes3:9000 \
	PGDOCK_E2E_S3_BUCKET=pgdock-e2e \
	PGDOCK_E2E_SUPABASE_SEED_URL=postgres://postgres:supabase-source@127.0.0.1:15450/postgres \
	PGDOCK_E2E_SUPABASE_URL=postgres://postgres:supabase-source@src-supabase:5432/postgres?sslmode=disable \
	npx playwright test || { $(E2E_COMPOSE) logs --no-color --tail 100 pgdock-server pgdock-agent caddy pebble; $(E2E_INSTANCES); exit 1; }
	@$(E2E_INSTANCES)
	$(E2E_COMPOSE) down -v --remove-orphans

## test-docs: install PGDock by running docs/install.md's commands in a
## scratch clone, then reach a working database in a browser (M7 done-when).
test-docs:
	PGDOCK_BUILD_FLAGS='$(DOCKER_BUILD_FLAGS)' test/docs/install-from-docs.sh

e2e-images:
	docker build $(DOCKER_BUILD_FLAGS) -t pgdock:local .
	docker build $(DOCKER_BUILD_FLAGS) --target agent -t pgdock-agent:local .
	docker build $(DOCKER_BUILD_FLAGS) -t pgdock-pebble:local -f test/e2e/bundle/Dockerfile.pebble test/e2e/bundle
	docker build $(DOCKER_BUILD_FLAGS) -t pgdock-fakes3:local -f test/e2e/bundle/Dockerfile.fakes3 .
	docker build $(DOCKER_BUILD_FLAGS) -t $(PG_IMAGE) deploy/images/postgres

## test-integration: provisioning end to end and the tenant-isolation suite,
## against real Postgres 18 and PgBouncer (the dev env plus test poolers).
test-integration: pooler-seed test-agent-bin pg-image
	$(COMPOSE) --profile test up -d --wait
	@set -a; . ./deploy/dev/test.env; set +a; go test -race -count=1 -p 1 ./test/...

## test-load: 150 shared projects, pgbench on 10 (spec §13); writes
## tmp/load-report.md. Needs pgbench.
test-load: pooler-seed test-agent-bin
	$(COMPOSE) --profile test up -d --wait
	@set -a; . ./deploy/dev/test.env; set +a; PGDOCK_TEST_LOAD=1 go test -count=1 -timeout 30m -v -run TestLoad ./test/load/

## pg-image: the Postgres 18 + WAL-G image dedicated instances run.
PG_IMAGE := pgdock-postgres:18-walg3.0.9
pg-image:
	docker build $(DOCKER_BUILD_FLAGS) -t $(PG_IMAGE) deploy/images/postgres

# The agent the integration tests run inside the agent-test container.
test-agent-bin:
	@mkdir -p tmp/bin
	CGO_ENABLED=0 go build -trimpath -o tmp/bin/pgdock-agent ./cmd/agent

test-web:
	cd web && npm run typecheck && npm test

lint:
	golangci-lint run ./...

## clean-ui: remove UI build output, keeping the committed placeholder.
clean-ui:
	find web/dist -mindepth 1 ! -name placeholder.html -delete

clean: clean-ui
	rm -rf $(BIN) tmp
