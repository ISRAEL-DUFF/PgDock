#!/bin/sh
# Turn on backend services (docs/backend-services.md) on this install: run
# xenibase-edge, and serve every project's API at https://<ref>.<api-domain>
# under one wildcard certificate that Caddy gets and renews with a Cloudflare
# DNS challenge.
#
#   cd deploy/compose
#   ./enable-services.sh api.example.com
#
# Before it: the domain's DNS is on Cloudflare, with a record `*.api` (an A
# record to this server, DNS only), and a Cloudflare API token limited to the
# zone with "Edit zone DNS" is in .env as CLOUDFLARE_API_TOKEN (put it there by
# hand; this script never asks for it). It is safe to run again.
set -eu
cd "$(dirname "$0")"

die() { echo "enable-services: $*" >&2; exit 1; }
rand_url() { head -c "$1" /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_' | tr -d '='; }

[ -f .env ] || die "no .env here: run ./install.sh first"
api=${1:-$(grep '^XENIBASE_API_DOMAIN=' .env | cut -d= -f2- || true)}
[ -n "$api" ] || die "usage: ./enable-services.sh <api-domain>   (for example api.example.com)"
case "$api" in
*[!A-Za-z0-9.-]* | .* | *. | *..*) die "invalid domain: '$api'" ;;
esac
ui=$(grep '^XENIBASE_UI_DOMAIN=' .env | cut -d= -f2-)
[ "$api" != "$ui" ] || die "the API domain can't be the web UI's name ($ui): projects are at <ref>.$api"

token=$(grep '^CLOUDFLARE_API_TOKEN=' .env | cut -d= -f2- || true)
[ -n "$token" ] || die "set CLOUDFLARE_API_TOKEN in .env first (a token limited to this zone, 'Edit zone DNS')"

# Settings: add what's missing, keep what the operator set.
set_env() { grep -q "^$1=" .env || echo "$1=$2" >> .env; }
set_env XENIBASE_API_DOMAIN "$api"
set_env XENIBASE_EDGE_SECRET "$(rand_url 40)"
set_env CADDY_IMAGE xenibase-caddy-cf:local
set_env COMPOSE_PROFILES services
secret=$(grep '^XENIBASE_EDGE_SECRET=' .env | cut -d= -f2-)
[ "${#secret}" -ge 32 ] || die "XENIBASE_EDGE_SECRET in .env must be at least 32 characters"

echo "Building Caddy with the Cloudflare DNS plugin..."
docker build -q -t "$(grep '^CADDY_IMAGE=' .env | cut -d= -f2-)" caddy-cf >/dev/null

# The wildcard site: Caddy terminates TLS and hands plain HTTP to the edge.
cat > caddy.d/api.caddy <<'CADDY'
# Written by enable-services.sh: backend services' API domain.
*.{$XENIBASE_API_DOMAIN} {
	tls {
		dns cloudflare {env.CLOUDFLARE_API_TOKEN}
	}
	# No compression or buffering: realtime and large downloads stream.
	reverse_proxy xenibase-edge:8443 {
		flush_interval -1
	}
}
CADDY

echo "Starting xenibase-edge and reloading Caddy..."
docker compose up -d --build
echo
echo "Backend services are on for *.$api. The first certificate takes a minute."
echo "Check:   docker compose logs caddy | grep -i 'certificate obtained'"
echo "Then:    Project -> Settings -> API -> Enable backend services"
