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

.PHONY: all dev generate check-generated build build-ui build-go test test-go test-web lint release-check clean clean-ui

all: build

## dev: Go server with live reload (air) + Vite dev server; Vite proxies /api to Go.
## Requires air: go install github.com/air-verse/air@latest
dev:
	@command -v air >/dev/null || { echo "air not found: go install github.com/air-verse/air@latest"; exit 1; }
	@trap 'kill 0' EXIT; \
	air -c .air.toml & \
	(cd web && npm run dev) & \
	wait

## generate: regenerate Go server interfaces and TypeScript API types from api/openapi.yaml.
generate:
	go generate ./...
	cd web && npm run generate

## check-generated: fail if generated code is out of date (used by CI).
check-generated: generate
	@git diff --exit-code -- internal/api/gen web/src/api/schema.d.ts \
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

test-go:
	go test -race ./...

test-web:
	cd web && npm run typecheck && npm test

lint:
	golangci-lint run ./...

## clean-ui: remove UI build output, keeping the committed placeholder.
clean-ui:
	find web/dist -mindepth 1 ! -name placeholder.html -delete

clean: clean-ui
	rm -rf $(BIN) tmp
