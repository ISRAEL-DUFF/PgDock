# PostgreSQL with WAL-G, the image for dedicated instances (spec §3.2,
# §6.4) and agent-run shared clusters, one per supported major (V3 §2.4).
#   docker build -t pgdock-postgres:18-walg3.0.9 deploy/images/postgres
#   docker build --build-arg PG_MAJOR=17 -t pgdock-postgres:17-walg3.0.9 deploy/images/postgres
#
# WAL-G is built from a pinned commit through the Go module proxy, without
# cgo: no system packages, and encryption via OpenPGP (pure Go).
#
# Patroni (HA instances, V3 §2.2) runs on the Python of the official
# python image of the same Debian release, copied in with a virtualenv of
# pinned wheels from PyPI: again no system packages.
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

FROM ${REGISTRY}python:3.13-slim-trixie AS patroni
ARG PATRONI_VERSION=4.1.5
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -f /run/secrets/ca_bundle ]; then export PIP_CERT=/run/secrets/ca_bundle; fi; \
    python -m venv /opt/patroni \
 && /opt/patroni/bin/pip install --no-cache-dir --disable-pip-version-check \
      "patroni[etcd3,psycopg3]==${PATRONI_VERSION}" \
 && /opt/patroni/bin/patroni --version

FROM ${REGISTRY}postgres:${PG_MAJOR}
ARG PG_MAJOR
COPY --from=walg /out-wal-g /usr/local/bin/wal-g
COPY --from=patroni /usr/local/lib/python3.13 /usr/local/lib/python3.13
COPY --from=patroni /usr/local/lib/libpython3.13.so.1.0 /usr/local/lib/
COPY --from=patroni /usr/local/bin/python3.13 /usr/local/bin/
COPY --from=patroni /opt/patroni /opt/patroni
RUN ln -s python3.13 /usr/local/bin/python3 && ln -s python3 /usr/local/bin/python \
 && ldconfig && /opt/patroni/bin/patroni --version
COPY --chmod=755 pgdock-patroni.sh /usr/local/bin/pgdock-patroni
COPY --chmod=755 pgdock-hba.sh /docker-entrypoint-initdb.d/10-pgdock-hba.sh
LABEL org.opencontainers.image.title="pgdock-postgres" \
      org.opencontainers.image.description="PostgreSQL ${PG_MAJOR} with WAL-G v3.0.9 for PGDock instances"
