#!/bin/sh
# Runs once, after initdb (docker-entrypoint-initdb.d): network logins
# only from PGDOCK_HBA_ALLOW (comma-separated CIDRs, set by the agent)
# and only with SCRAM (spec §7.1). Unset keeps the image's default.
set -eu
[ -n "${PGDOCK_HBA_ALLOW:-}" ] || exit 0
{
	echo "# Written by pgdock-hba.sh: control plane and poolers only (spec §7.1)."
	echo "local all all trust"
	echo "$PGDOCK_HBA_ALLOW" | tr ',' '\n' | while read -r cidr; do
		[ -n "$cidr" ] && printf 'host all all %s scram-sha-256\n' "$cidr"
	done
} > "$PGDATA/pg_hba.conf"
