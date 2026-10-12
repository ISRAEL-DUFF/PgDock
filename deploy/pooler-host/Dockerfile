# A PGDock edge pooler host (V3 §2.1): pgdock-agent in pooler mode and the
# session and transaction PgBouncers, in one container. keepalived runs
# beside it (keepalived.Dockerfile), the only part that needs NET_ADMIN; this
# container renders its configuration into the shared /etc/pgdock-edge.
# Production runs both with host networking on each of the two pooler hosts
# (compose.yaml here); TestPoolerHostFailover runs two pairs on the dev
# Docker network.
#
#   docker build -f deploy/pooler-host/Dockerfile -t pgdock-pooler-host .
#
# Behind a TLS-intercepting proxy, pass its CA as a build secret
# (--secret id=ca_bundle,src=ca.pem); base images can come from a mirror
# (--build-arg REGISTRY=mirror.gcr.io/library/).

ARG REGISTRY=
FROM ${REGISTRY}golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -f /run/secrets/ca_bundle ]; then export SSL_CERT_FILE=/run/secrets/ca_bundle; fi; \
    go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/israel-duff/pgdock/internal/version.Version=${VERSION} -X github.com/israel-duff/pgdock/internal/version.Commit=${COMMIT}" \
      -o /out/pgdock-agent ./cmd/agent

FROM edoburu/pgbouncer:v1.25.1-p0
# Everything runs as the image's postgres user (uid 70): the agent writes
# the PgBouncer files and signals the PgBouncers, so they share a user.
USER root
COPY --from=build /out/pgdock-agent /usr/local/bin/pgdock-agent
COPY deploy/pgbouncer/session.ini /etc/pgbouncer/session.ini
COPY deploy/pgbouncer/transaction.ini /etc/pgbouncer/transaction.ini
COPY deploy/pooler-host/entrypoint.sh /usr/local/bin/pooler-host
RUN chmod 755 /usr/local/bin/pooler-host \
 && install -d -o 70 -g 70 -m 700 /var/lib/pgdock-agent \
 && install -d -o 70 -g 70 -m 750 /etc/pgbouncer/pgdock \
 && install -d -o 70 -g 70 -m 755 /run/pgdock /etc/pgdock-edge
USER 70:70
ENV PGDOCK_AGENT_STATE_DIR=/var/lib/pgdock-agent \
    PGDOCK_AGENT_POOLER_DIR=/etc/pgbouncer/pgdock \
    PGDOCK_AGENT_POOLER_PIDFILES=/run/pgdock/session.pid,/run/pgdock/transaction.pid \
    PGDOCK_EDGE_KEEPALIVED_DIR=/etc/pgdock-edge \
    PGDOCK_AGENT_LOG_FORMAT=json
VOLUME ["/var/lib/pgdock-agent", "/etc/pgdock-edge"]
EXPOSE 5432 6543 7070
ENTRYPOINT ["/usr/local/bin/pooler-host"]
