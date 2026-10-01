# syntax=docker/dockerfile:1
# pgdock-server and pgdock-agent, with the web UI embedded.
#   docker build -t pgdock .
#
# Behind a TLS-intercepting proxy, pass its CA as a build secret:
#   docker build --secret id=ca_bundle,src=/path/to/ca.pem -t pgdock .

FROM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -f /run/secrets/ca_bundle ]; then export NODE_EXTRA_CA_CERTS=/run/secrets/ca_bundle; fi; \
    npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -f /run/secrets/ca_bundle ]; then export SSL_CERT_FILE=/run/secrets/ca_bundle; fi; \
    go mod download
COPY . .
COPY --from=ui /src/web/dist/ ./web/dist/
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/israel-duff/pgdock/internal/version.Version=${VERSION} -X github.com/israel-duff/pgdock/internal/version.Commit=${COMMIT} -X github.com/israel-duff/pgdock/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o /out/usr/local/bin/pgdock-server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/usr/local/bin/pgdock-agent ./cmd/agent \
 && /out/usr/local/bin/pgdock-server -require-ui \
 && mkdir -p /out/var/lib/pgdock/pooler

FROM gcr.io/distroless/static-debian12
# uid 70 matches the PgBouncer image, so the poolers can read the 0640
# config, auth file, and TLS key pgdock-server writes.
COPY --from=build --chown=70:70 /out/ /
USER 70:70
ENV PGDOCK_LISTEN_ADDR=0.0.0.0:8080 \
    PGDOCK_DATA_DIR=/var/lib/pgdock \
    PGDOCK_POOLER_CONFIG_DIR=/var/lib/pgdock/pooler
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pgdock-server"]
