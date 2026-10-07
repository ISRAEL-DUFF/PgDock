# Status page

`pgdock-status` is PGDock's public status page (V3 §2.6). Run it **away from
PGDock**, on another provider, so an outage can't take down the page that
announces it.

It does four things:
- **Probes PGDock from outside every minute**: the dashboard, the edge
  pooler on both ports, and the shared tier.
- **Takes signed heartbeats** from pgdock-server for what it can't probe:
  dedicated instances, backups, and webhooks & jobs. A component whose
  heartbeat stops shows **No data**, never Operational.
- **Opens an incident by itself** after 3 failing checks in a row, and
  resolves it after 2 good ones. The incidents you post in Admin →
  Incidents appear there too.
- Keeps **90 days of history** per component, emails **subscribers**
  (double opt-in), and serves the page as plain HTML with no JavaScript, a
  JSON API (`/api/v1/status`, `/api/v1/incidents`) and an RSS feed
  (`/feed.rss`).

Its state is one SQLite file. It never connects to PGDock's database.

## Deploy

On a small server outside PGDock's provider (1 vCPU, 512 MB is enough):

1. Build the image in a PGDock checkout and copy it over:

   ```sh
   make status-image
   docker save pgdock-status:local | ssh status-host docker load
   ```

2. Copy `deploy/status/compose.yaml` and
   `deploy/status/status.example.toml` (as `status.toml`) to the server. Put
   the secrets in a `secrets/` directory next to them:

   ```sh
   mkdir -p secrets
   openssl rand -hex 32 > secrets/pgdock-status-push   # shared with pgdock-server
   ```

3. Edit `status.toml`: set `public_url`, and the dashboard URL in the
   `dashboard` probe. If you want email subscriptions, fill in `[smtp]`;
   remove the section otherwise. Then check the file:

   ```sh
   docker run --rm -v "$PWD/status.toml:/s.toml:ro" -v "$PWD/secrets:/run/secrets:ro" \
     pgdock-status:local check-config --config /s.toml
   ```

4. Point the status domain's DNS at the server and start it:

   ```sh
   STATUS_DOMAIN=status.example.com docker compose up -d
   ```

   Caddy gets the certificate and forwards to pgdock-status.

## The probe database

The edge pooler and shared tier probes log in through the pooler like a
customer does. They need a database of their own with a low-privilege
role:

1. In PGDock, create a project named `status-probe`, in your own
   organisation, on the shared tier.
2. In Connect, copy its session and transaction connection strings. Replace
   the host with the floating IP's hostname: the public database host
   clients use.
3. Save them as `secrets/pgdock-status-probe-session` and
   `secrets/pgdock-status-probe-transaction`.

The transaction-port probe runs `SELECT 1`. The shared tier probe runs
`SELECT txid_current()`, which needs a primary that accepts writes.

## Connect PGDock

On pgdock-server, set these and restart it:

```sh
PGDOCK_STATUS_URL=https://status.example.com
PGDOCK_STATUS_PUSH_SECRET_FILE=/run/secrets/pgdock-status-push   # the same secret
PGDOCK_STATUS_REGION=eu-central                                  # optional default
# PGDOCK_STATUS_COMPONENTS=dashboard,edge-pooler,...  only if you renamed components
```

pgdock-server then:
- sends a heartbeat every minute;
- pushes each incident within seconds of a change. Admin → Incidents shows
  "on status page", or the error if a push fails; failed pushes are retried
  every 10 seconds.

Both directions are signed with the `PGDock-Signature` HMAC scheme webhooks
use, and refused if they are more than 5 minutes old.

## Incidents

- **Automatic:** a probed component that fails 3 checks in a row opens an
  incident on the status page by itself. A component that answers on one
  pooler port but not the other is degraded, and opens a minor incident.
  The incident resolves itself after 2 good checks. PGDock can't edit
  these.
- **Posted in PGDock:** Admin → Incidents → New incident. Pick the affected
  components, the severity, the status, and the first update:
  - minor and maintenance show the components as degraded;
  - major and critical show them as an outage.

  Post updates as you learn more. Posting one with status **resolved**
  closes the incident; posting any other status reopens it.

Confirmed subscribers get an email for every new update, automatic or
posted.

## Operate

- **Back up** `/var/lib/pgdock-status/status.db` (the `data` volume) if you
  want to keep the history and subscribers. If it is lost, the page starts
  over with no history.
- `pgdock-status` logs JSON to stdout, including every component state
  change and every incident it opens or resolves.
- The subscribe form allows 5 requests an hour per address. Behind Caddy,
  `trust_proxy = true` makes that per client, not per proxy.

## Limits

The page probes from one vantage point. The SLA's two-vantage-point
measurement, availability minutes per HA project, arrives with HA dedicated
instances (M19). Paying organisations are subscribed automatically once
billing exists (M20).
