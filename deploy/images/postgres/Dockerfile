# PostgreSQL with WAL-G, the image for dedicated instances (spec §3.2,
# §6.4) and agent-run shared clusters, one per supported major (V3 §2.4).
#   docker build -t pgdock-postgres:18-walg3.0.9 deploy/images/postgres
#   docker build --build-arg PG_MAJOR=17 -t pgdock-postgres:17-walg3.0.9 deploy/images/postgres
#
# WAL-G is built from a pinned commit through the Go module proxy, without
# cgo: no system packages, and encryption via OpenPGP (pure Go).
ARG REGISTRY=
ARG PG_MAJOR=18

FROM ${REGISTRY}golang:1.25-alpine AS walg
# wal-g v3.0.9
ARG WALG_COMMIT=3e493188db28335fb93a45bd680c6ff8fc3028c1
ENV CGO_ENABLED=0 GOEXPERIMENT=jsonv2 GOTOOLCHAIN=go1.25.13
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -f /run/secrets/ca_bundle ]; then export SSL_CERT_FILE=/run/secrets/ca_bundle; fi; \
    go install -trimpath -ldflags "-s -w" github.com/wal-g/wal-g/main/pg@${WALG_COMMIT} \
 && install -m 755 /go/bin/pg /out-wal-g

FROM ${REGISTRY}postgres:${PG_MAJOR}
ARG PG_MAJOR
COPY --from=walg /out-wal-g /usr/local/bin/wal-g
COPY --chmod=755 pgdock-hba.sh /docker-entrypoint-initdb.d/10-pgdock-hba.sh
LABEL org.opencontainers.image.title="pgdock-postgres" \
      org.opencontainers.image.description="PostgreSQL ${PG_MAJOR} with WAL-G v3.0.9 for PGDock instances"
