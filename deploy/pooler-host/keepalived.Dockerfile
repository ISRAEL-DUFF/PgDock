# keepalived for a PGDock edge pooler host (V3 §2.1). It runs beside the
# pooler-host container, reads the keepalived.conf that container renders
# into the shared /etc/pgdock-edge, and calls the agent's loopback API with
# wget (busybox) for its check and notify scripts.
#
#   docker build -f deploy/pooler-host/keepalived.Dockerfile -t pgdock-keepalived .

ARG REGISTRY=
FROM ${REGISTRY}alpine:3.22
RUN --mount=type=secret,id=ca_bundle,required=false \
    set -e; \
    if [ -f /run/secrets/ca_bundle ]; then cp /etc/ssl/certs/ca-certificates.crt /tmp/ca.orig; cat /run/secrets/ca_bundle >> /etc/ssl/certs/ca-certificates.crt; fi; \
    apk add --no-cache keepalived; \
    if [ -f /tmp/ca.orig ]; then mv /tmp/ca.orig /etc/ssl/certs/ca-certificates.crt; fi
COPY deploy/pooler-host/keepalived.sh /usr/local/bin/pgdock-keepalived
RUN chmod 755 /usr/local/bin/pgdock-keepalived
ENTRYPOINT ["/usr/local/bin/pgdock-keepalived"]
