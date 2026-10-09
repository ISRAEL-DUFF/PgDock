# pgdock-server (with the web UI embedded) and, as the agent target,
# pgdock-agent.
#   docker build -t pgdock .
#
# Behind a TLS-intercepting proxy, pass its CA as a build secret:
#   docker build --secret id=ca_bundle,src=/path/to/ca.pem -t pgdock .
# Base images can come from a mirror, e.g.
#   --build-arg REGISTRY=mirror.gcr.io/library/

ARG REGISTRY=
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12

FROM ${REGISTRY}node:22-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -f /run/secrets/ca_bundle ]; then export NODE_EXTRA_CA_CERTS=/run/secrets/ca_bundle; fi; \
    npm ci
COPY web/ ./
RUN npm run build

FROM ${REGISTRY}golang:1.26-alpine AS build
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
      -tags nodynamic \
      -o /out/usr/local/bin/pgdock-server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/usr/local/bin/pgdock-agent ./cmd/agent \
 && /out/usr/local/bin/pgdock-server -require-ui \
 && mkdir -p /out/var/lib/pgdock/pooler

# pgdock-agent runs next to Postgres and needs its client tools (spec §3.2):
#   docker build --target agent -t pgdock-agent .
FROM ${REGISTRY}postgres:18 AS agent
COPY --from=build /out/usr/local/bin/pgdock-agent /usr/local/bin/pgdock-agent
RUN install -d -o postgres -g postgres -m 700 /var/lib/pgdock-agent
USER postgres
ENV PGDOCK_AGENT_STATE_DIR=/var/lib/pgdock-agent \
    PGDOCK_AGENT_DISK_PATH=/var/lib/pgdock-agent
EXPOSE 7070
ENTRYPOINT ["/usr/local/bin/pgdock-agent"]
CMD ["run"]

FROM ${RUNTIME_IMAGE}
# uid 70 matches the PgBouncer image, so the poolers can read the 0640
# config, auth file, and TLS key pgdock-server writes.
COPY --from=build --chown=70:70 /out/ /
USER 70:70
ENV PGDOCK_LISTEN_ADDR=0.0.0.0:8080 \
    PGDOCK_DATA_DIR=/var/lib/pgdock \
    PGDOCK_POOLER_CONFIG_DIR=/var/lib/pgdock/pooler
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pgdock-server"]
