#!/bin/sh
# Turn on backend services (docs/backend-services.md) on this install: run
# xenibase-edge, and serve every project's API at https://<ref>.<api-domain>
# under one wildcard certificate that Caddy gets and renews with a DNS
# challenge (the only way to get a wildcard).
#
#   cd deploy/compose
#   ./enable-services.sh api.example.com [cloudflare|desec|acmedns]
#
# Before it, in .env (put them there by hand; this script never asks for a
# token): the DNS provider's API token as CLOUDFLARE_API_TOKEN or DESEC_TOKEN,
# or for acme-dns (a free service that answers only the challenge, for DNS
# hosts with no API) the three values its /register call returned, as
# ACMEDNS_USERNAME, ACMEDNS_PASSWORD and ACMEDNS_SUBDOMAIN (and
# ACMEDNS_SERVER_URL if it isn't https://auth.acme-dns.io). With acme-dns, add a
# CNAME at your DNS host:  _acme-challenge.<api-domain>  ->  <its fulldomain>. If the domain's DNS can't be on that provider, point the
# challenge at a zone that is: add a CNAME
#   _acme-challenge.<api-domain>  ->  _acme-challenge.<zone you control there>
# and set ACME_CHALLENGE_DOMAIN=<that zone name> in .env. DNS also needs
# `*.<api-domain>` as an A record to this server. Safe to run again.
set -eu
cd "$(dirname "$0")"

die() { echo "enable-services: $*" >&2; exit 1; }
rand_url() { head -c "$1" /dev/urandom | base64 | tr -d '\n' | tr '+/' '-_' | tr -d '='; }
env_get() { grep "^$1=" .env | head -1 | cut -d= -f2- || true; }

[ -f .env ] || die "no .env here: run ./install.sh first"
api=${1:-$(env_get XENIBASE_API_DOMAIN)}
[ -n "$api" ] || die "usage: ./enable-services.sh <api-domain> [cloudflare|desec|acmedns]"
case "$api" in
*[!A-Za-z0-9.-]* | .* | *. | *..*) die "invalid domain: '$api'" ;;
esac
ui=$(env_get XENIBASE_UI_DOMAIN)
[ "$api" != "$ui" ] || die "the API domain can't be the web UI's name ($ui): projects are at <ref>.$api"

provider=${2:-$(env_get DNS_PROVIDER)}
provider=${provider:-cloudflare}
case "$provider" in
cloudflare) plugin=github.com/caddy-dns/cloudflare tokenvar=CLOUDFLARE_API_TOKEN ;;
desec) plugin=github.com/caddy-dns/desec tokenvar=DESEC_TOKEN ;;
acmedns) plugin=github.com/caddy-dns/acmedns tokenvar=ACMEDNS_USERNAME ;;
*) die "unknown DNS provider '$provider': use cloudflare, desec or acmedns" ;;
esac
if [ "$provider" = acmedns ]; then
	for v in ACMEDNS_USERNAME ACMEDNS_PASSWORD ACMEDNS_SUBDOMAIN; do
		[ -n "$(env_get "$v")" ] || die "set $v in .env first (from acme-dns's /register call)"
	done
else
	[ -n "$(env_get "$tokenvar")" ] || die "set $tokenvar in .env first (the $provider API token, limited to the zone)"
fi

challenge=$(env_get ACME_CHALLENGE_DOMAIN)
case "$challenge" in
*[!A-Za-z0-9.-]*) die "invalid ACME_CHALLENGE_DOMAIN: '$challenge'" ;;
esac

# Settings: add what's missing, keep what the operator set.
set_env() { grep -q "^$1=" .env || echo "$1=$2" >> .env; }
set_env XENIBASE_API_DOMAIN "$api"
set_env XENIBASE_EDGE_SECRET "$(rand_url 40)"
set_env DNS_PROVIDER "$provider"
[ "$provider" != acmedns ] || set_env ACMEDNS_SERVER_URL https://auth.acme-dns.io
set_env CADDY_IMAGE "xenibase-caddy-$provider:local"
set_env COMPOSE_PROFILES services
secret=$(env_get XENIBASE_EDGE_SECRET)
[ "${#secret}" -ge 32 ] || die "XENIBASE_EDGE_SECRET in .env must be at least 32 characters"

echo "Building Caddy with the $provider DNS plugin..."
docker build -q --build-arg "DNS_PLUGIN=$plugin" -t "$(env_get CADDY_IMAGE)" caddy-dns >/dev/null

# The wildcard site: Caddy terminates TLS and hands plain HTTP to the edge.
{
	echo "# Written by enable-services.sh: backend services' API domain."
	echo '*.{$XENIBASE_API_DOMAIN} {'
	echo '	tls {'
	# Each plugin has its own Caddyfile syntax.
	case "$provider" in
	desec)
		echo "		dns desec {"
		echo "			token {env.$tokenvar}"
		echo "		}"
		;;
	acmedns)
		echo "		dns acmedns {"
		echo "			username {env.ACMEDNS_USERNAME}"
		echo "			password {env.ACMEDNS_PASSWORD}"
		echo "			subdomain {env.ACMEDNS_SUBDOMAIN}"
		echo "			server_url {env.ACMEDNS_SERVER_URL}"
		echo "		}"
		;;
	*) echo "		dns $provider {env.$tokenvar}" ;;
	esac
	[ -z "$challenge" ] || echo "		dns_challenge_override_domain $challenge"
	echo '	}'
	echo '	# No compression or buffering: realtime and large downloads stream.'
	echo '	reverse_proxy xenibase-edge:8443 {'
	echo '		flush_interval -1'
	echo '	}'
	echo '}'
} > caddy.d/api.caddy

echo "Starting xenibase-edge and reloading Caddy..."
docker compose up -d --build
echo
echo "Backend services are on for *.$api. The first certificate takes a minute."
echo "Check:   docker compose logs caddy | grep -i 'certificate obtained'"
echo "Then:    Project -> Settings -> API -> Enable backend services"
