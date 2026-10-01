#!/bin/sh
# Installs PGDock by following docs/install.md on this machine, then drives
# a browser from the fresh install to a working database (M7 done-when:
# "a clean VPS can be installed from the docs").
#
# It clones the current commit into a scratch directory, runs every shell
# block the guide marks with <!-- docs-test -->, in order, in one shell,
# from the clone's root (as a reader would after "Get PGDock"), and checks
# the installer's output. The only differences from a real VPS: Pebble and
# a DNS stub stand in for Let's Encrypt and public DNS (the e2e overlay,
# through COMPOSE_FILE), host ports are offset, and a fake S3 is the bucket.
#
#   make test-docs [DOCKER_BUILD_FLAGS=...]
set -eu
repo=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'cd "$work/deploy/compose" 2>/dev/null && docker compose down -v --remove-orphans >/dev/null 2>&1; rm -rf "$work"' EXIT

git clone -q "$repo" "$work"
git -C "$work" checkout -q "$(git -C "$repo" rev-parse HEAD)"

# The commands, exactly as the guide shows them.
steps="$work/docs-steps.sh"
awk '/<!-- docs-test -->/ {mark=1; next}
     mark && /^```sh$/ {inblock=1; next}
     inblock && /^```$/ {inblock=0; mark=0; next}
     inblock {print}' "$repo/docs/install.md" > "$steps"
echo "--- running from docs/install.md:"
cat "$steps"
[ -s "$steps" ] || { echo "no docs-test blocks found" >&2; exit 1; }

# Test-only images for the overlay (Pebble, fake S3); the guide builds the rest.
# shellcheck disable=SC2086
docker build ${PGDOCK_BUILD_FLAGS:-} -q -t pgdock-pebble:local -f "$repo/test/e2e/bundle/Dockerfile.pebble" "$repo/test/e2e/bundle" >/dev/null
# shellcheck disable=SC2086
docker build ${PGDOCK_BUILD_FLAGS:-} -q -t pgdock-fakes3:local -f "$repo/test/e2e/bundle/Dockerfile.fakes3" "$repo" >/dev/null

export PGDOCK_UI_DOMAIN=pgdock.test PGDOCK_ACME_EMAIL=ops@pgdock.test PGDOCK_PUBLIC_IPS=127.0.0.1
export COMPOSE_PROJECT_NAME=pgdock-docs COMPOSE_FILE=compose.yaml:../../test/e2e/bundle/compose.e2e.yaml
export PGDOCK_NETWORK=pgdock-docs PGDOCK_DB_SESSION_PORT=15432 PGDOCK_DB_POOLED_PORT=16543
export SHARED_PG_SHARED_BUFFERS=128MB SHARED_PG_EFFECTIVE_CACHE_SIZE=512MB
out="$work/install.log"
(cd "$work" && sh -eux "$steps") 2>&1 | tee "$out"
grep -q "First-run setup code: e2e-setup-code" "$out" || { echo "the installer did not print the setup code" >&2; exit 1; }
grep -q '^PGDOCK_MASTER_KEY=' "$work/deploy/compose/.env" || { echo "no .env written" >&2; exit 1; }
[ "$(stat -c %a "$work/deploy/compose/.env")" = 600 ] || { echo ".env is not mode 600" >&2; exit 1; }

# The setup wizard and a first database, in a browser.
mkdir -p "$repo/tmp"
curl -fsSk --noproxy '*' https://127.0.0.1:15000/roots/0 > "$repo/tmp/docs-pebble-root.pem"
cd "$repo/test/e2e"
npm ci --silent
PGDOCK_E2E_URL=https://pgdock.test:18443 \
PGDOCK_E2E_HOST_RULES="MAP pgdock.test 127.0.0.1" \
PGDOCK_E2E_IGNORE_HTTPS_ERRORS=1 \
PGDOCK_E2E_SETUP_CODE=e2e-setup-code \
PGDOCK_E2E_DB_HOST=db.pgdock.test \
PGDOCK_E2E_DB_ADDR=127.0.0.1 \
PGDOCK_E2E_DB_CA="$repo/tmp/docs-pebble-root.pem" \
PGDOCK_E2E_EXPECT_ISSUER=Pebble \
PGDOCK_E2E_S3_ENDPOINT=http://fakes3:9000 \
PGDOCK_E2E_S3_BUCKET=pgdock-e2e \
npx playwright test --grep "fresh install to a working database" || {
	cd "$work/deploy/compose" && docker compose logs --no-color --tail 80 pgdock-server caddy pgdock-agent
	exit 1
}
echo "installed from docs/install.md and reached a working database"
