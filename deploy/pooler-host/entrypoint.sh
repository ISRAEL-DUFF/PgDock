#!/bin/sh
# pooler-host: runs one PGDock edge pooler host (V3 §2.1): both PgBouncers
# and pgdock-agent in pooler mode. keepalived runs in its own container and
# reads the configuration this script renders. If any process here exits,
# the rest are stopped and the container exits: keepalived's check then
# fails, the other pooler host takes the floating IP, and the restart policy
# brings this one back.
#
# Configuration (environment):
#   PGDOCK_AGENT_SERVER, PGDOCK_AGENT_TOKEN   registration (first start)
#   PGDOCK_AGENT_ADVERTISE                    host:port pgdock-server dials
#   PGDOCK_EDGE_SELF_IP, PGDOCK_EDGE_PEER_IP  the two hosts' private IPs
#   PGDOCK_EDGE_VRRP_PASSWORD                 shared, at most 8 characters
#   PGDOCK_EDGE_INTERFACE (eth0), PGDOCK_EDGE_VIP, PGDOCK_EDGE_ROUTER_ID (51),
#   PGDOCK_EDGE_PRIORITY (100)
#   PGDOCK_AGENT_SERVER_ID, PGDOCK_AGENT_HETZNER_TOKEN,
#   PGDOCK_AGENT_FLOATING_IP_ID, PGDOCK_AGENT_HETZNER_API   the floating IP
# Without PGDOCK_EDGE_SELF_IP no keepalived configuration is written.
set -eu

dir=${PGDOCK_AGENT_POOLER_DIR:-/etc/pgbouncer/pgdock}
edge=${PGDOCK_EDGE_KEEPALIVED_DIR:-/etc/pgdock-edge}
run=/run/pgdock

# Placeholder routes, users and certificate, so the PgBouncers start before
# pgdock-server's first push (the host is not ready until it arrives).
pgdock-agent pooler-seed --dir "$dir"
if [ -n "${PGDOCK_EDGE_SELF_IP:-}" ]; then
  pgdock-agent keepalived-config > "$edge/keepalived.conf.tmp"
  mv "$edge/keepalived.conf.tmp" "$edge/keepalived.conf"
fi

pgbouncer /etc/pgbouncer/session.ini &
echo $! > "$run/session.pid"
pgbouncer /etc/pgbouncer/transaction.ini &
echo $! > "$run/transaction.pid"
pgdock-agent run &
agent=$!

pids="$(cat "$run/session.pid") $(cat "$run/transaction.pid") $agent"
stop() {
  # The agent first: keepalived's check fails at once and it gives up the
  # address while the PgBouncers drain.
  kill "$agent" 2>/dev/null || true
  sleep 1
  # shellcheck disable=SC2086
  kill $pids 2>/dev/null || true
}
trap 'stop; exit 0' TERM INT

while :; do
  for p in $pids; do
    if ! kill -0 "$p" 2>/dev/null; then
      echo "pooler-host: process $p exited; stopping the pooler host" >&2
      stop
      exit 1
    fi
  done
  sleep 1
done
