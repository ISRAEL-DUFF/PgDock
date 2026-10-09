#!/usr/bin/env bash
# Times an edge pooler failover from a client's point of view (V4.1 §13,
# docs/edge-poolers.md "Rehearse a failover").
#
#   DATABASE_URL='postgresql://user:pass@db.eu.example.ng:6543/db?sslmode=require' \
#   KILL_CMD='ssh edge-a docker kill pgdock-edge-pooler-host-1' \
#   scripts/rehearse-pooler-failover.sh
#
# It runs `SELECT 1` through the floating address every INTERVAL seconds,
# runs KILL_CMD once the client has had WARMUP good queries (or waits for you
# to kill the active host by hand when KILL_CMD is empty), and reports the
# gap between the last success before the failure and the first success
# after it. It exits 1 if that gap is over TARGET seconds (10, V3 §2.1) or
# queries never come back within GIVEUP seconds.
set -euo pipefail

: "${DATABASE_URL:?set DATABASE_URL to a pooled project URL through the floating address}"
INTERVAL="${INTERVAL:-0.2}"
WARMUP="${WARMUP:-25}"
TARGET="${TARGET:-10}"
GIVEUP="${GIVEUP:-120}"
KILL_CMD="${KILL_CMD:-}"
command -v psql >/dev/null || { echo "psql is needed" >&2; exit 2; }

now() { date +%s.%N; }
probe() { PGCONNECT_TIMEOUT=2 psql "$DATABASE_URL" -XAtqc 'SELECT 1' >/dev/null 2>&1; }

echo "warming up: $WARMUP good queries"
good=0
while [ "$good" -lt "$WARMUP" ]; do
	if probe; then good=$((good + 1)); else good=0; fi
	sleep "$INTERVAL"
done

if [ -n "$KILL_CMD" ]; then
	echo "killing the active host: $KILL_CMD"
	bash -c "$KILL_CMD" &
else
	echo "kill the host holding the floating IP now (docker kill, power off, or pull its network)"
fi

last_ok=$(now)
failed_at=""
start=$(now)
fails=0
while :; do
	t=$(now)
	if probe; then
		if [ -n "$failed_at" ]; then
			back=$(now)
			gap=$(awk -v a="$last_ok" -v b="$back" 'BEGIN { printf "%.1f", b - a }')
			echo "queries back after a ${gap}s gap ($fails failed attempts)"
			echo "date=$(date -u +%FT%TZ) gap_s=$gap failed_attempts=$fails target_s=$TARGET"
			awk -v g="$gap" -v t="$TARGET" 'BEGIN{exit !(g<=t)}' && { echo "PASS: within ${TARGET}s"; exit 0; }
			echo "FAIL: over ${TARGET}s"; exit 1
		fi
		last_ok=$t
	else
		fails=$((fails + 1))
		[ -n "$failed_at" ] || { failed_at=$t; echo "first failure at $(date -u +%T)"; }
	fi
	if awk -v s="$start" -v n="$t" -v g="$GIVEUP" 'BEGIN{exit !(n-s>g)}'; then
		if [ -z "$failed_at" ]; then
			echo "no failure seen in ${GIVEUP}s: did the kill reach the active host?"; exit 1
		fi
		echo "FAIL: queries didn't come back within ${GIVEUP}s"; exit 1
	fi
	sleep "$INTERVAL"
done
