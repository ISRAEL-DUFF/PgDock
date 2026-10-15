#!/bin/sh
# Writes the Caddy file enable-services.sh makes for acme-dns, in a scratch copy
# of the bundle, and runs plugin_test.go on it (make test-acmedns). Needs Go and
# network access to fetch Caddy and the plugin.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo=$here/../..
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/c" && cp -R "$repo/deploy/compose/." "$work/c/" && rm -f "$work/c/caddy.d/"*.caddy
user=user-1234 sub=9f486b61-aecc-47ac-b6d2-b6999c2f1e44 pass='p4ss/w0rd'
printf '%s\n' XENIBASE_UI_DOMAIN=app.example.test "ACMEDNS_USERNAME=$user" "ACMEDNS_PASSWORD=$pass" "ACMEDNS_SUBDOMAIN=$sub" > "$work/c/.env"
(cd "$work/c" && sh ./enable-services.sh --config-only api.example.test acmedns >/dev/null)
cd "$here"
ACMEDNS_CADDY_FILE="$work/c/caddy.d/api.caddy" ACMEDNS_EXPECT_USER=$user ACMEDNS_EXPECT_SUBDOMAIN=$sub ACMEDNS_PASSWORD=$pass \
	GOFLAGS=-mod=mod go test -count=1 ./...
