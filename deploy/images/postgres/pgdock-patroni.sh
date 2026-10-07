#!/bin/sh
# Entry point of an HA member (V3 §2.2): the agent passes Patroni's
# configuration and the etcd client certificate in the environment; this
# writes them where only postgres can read them and runs Patroni as
# postgres, which then runs PostgreSQL.
#
#   pgdock-patroni             run Patroni
#   pgdock-patroni replica …   create_replica_methods: restore the newest
#                              WAL-G base backup into --datadir
set -eu

if [ "${1:-}" = replica ]; then
	datadir=
	for a in "$@"; do
		case "$a" in --datadir=*) datadir="${a#--datadir=}" ;; esac
	done
	[ -n "$datadir" ] || { echo "pgdock-patroni replica: no --datadir" >&2; exit 1; }
	[ -n "${WALG_S3_PREFIX:-}" ] || { echo "pgdock-patroni replica: no WAL-G archive" >&2; exit 1; }
	mkdir -p "$datadir" && chmod 700 "$datadir"
	exec wal-g backup-fetch "$datadir" LATEST
fi

conf=/etc/patroni
mkdir -p "$conf"
umask 077
printf '%s\n' "${PGDOCK_PATRONI_CONFIG:?no Patroni configuration}" > "$conf/patroni.yml"
for f in ca cert key; do
	eval "v=\${PGDOCK_ETCD_$(echo $f | tr a-z A-Z):-}"
	[ -n "$v" ] && printf '%s\n' "$v" > "$conf/etcd-$f.pem"
done
chown -R postgres:postgres "$conf"
unset PGDOCK_PATRONI_CONFIG PGDOCK_ETCD_CA PGDOCK_ETCD_CERT PGDOCK_ETCD_KEY

# The data directory lives in the instance's volume (mounted at
# /var/lib/postgresql); Patroni creates it on bootstrap.
mkdir -p "$(dirname "$PGDATA")"
chown postgres:postgres /var/lib/postgresql "$(dirname "$PGDATA")"
if [ -d "$PGDATA" ]; then
	chown -R postgres:postgres "$PGDATA"
	chmod 700 "$PGDATA"
fi
exec gosu postgres /opt/patroni/bin/patroni "$conf/patroni.yml"
