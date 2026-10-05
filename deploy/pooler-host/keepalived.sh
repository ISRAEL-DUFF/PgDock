#!/bin/sh
# pgdock-keepalived: waits for the pooler-host container to render
# /etc/pgdock-edge/keepalived.conf, then runs keepalived in the foreground.
# Any Alpine-based keepalived image works the same way (the scripts run
# /bin/busybox wget):
#   docker run --entrypoint sh <image> -c "$(cat keepalived.sh)"
set -eu
conf=${PGDOCK_EDGE_KEEPALIVED_CONF:-/etc/pgdock-edge/keepalived.conf}
until [ -s "$conf" ]; do sleep 1; done
exec keepalived --dont-fork --log-console --use-file "$conf"
