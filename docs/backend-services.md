# Backend services

Backend services (V4) let an app talk to its project over HTTPS, straight
from a browser or a phone, without a server of its own: the data API, auth,
storage and realtime. This page covers what is in place now (V4-M28): the
edge gateway, project API hostnames, API keys, and the per-request roles
and claims everything else builds on. The data API, auth, storage and
realtime arrive in the milestones after it.

## How a request is served

```
app ──HTTPS──▶ <ref>.<domain> ──▶ pgdock-edge (per region)
                                    │ key, token, CORS, rate limit
                                    ▼
                        transaction pooler ──▶ the project's database
                        BEGIN; SET LOCAL ROLE <db>_anon|_user|_service;
                        set_config('pgd.claims', …, true); …; COMMIT
```

- Each project with backend services has a **reference**, eight letters and
  digits starting with a letter (`k7f3m2q9`), and is served at
  `https://<ref>.<domain>`, where the domain is `PGDOCK_API_DOMAIN`.
- **pgdock-edge** resolves exactly one project per request from the
  hostname, checks the API key against that project's keys only, and runs
  the request in one transaction through the transaction pooler. It logs
  in as the project's `<db>_edge` role, which owns nothing and can only
  `SET ROLE` to the three request roles.
- The claims of the request are set for that transaction only, so nothing
  carries over to the next request on the same pooled connection.

## Keys

| Key | Prefix | Used from | Requests run as |
| --- | --- | --- | --- |
| Publishable | `pgd_pub_` | Browsers and mobile apps; meant to be embedded | `<db>_anon`, or `<db>_user` with a signed-in user's token |
| Secret | `pgd_sec_` | Servers only | `<db>_service` (bypasses row-level security) |

- Send the key in the `apikey` header. A signed-in user's access token goes
  in `Authorization: Bearer …`; it is an ES256 JWT for this project
  (`aud` is the reference), verified with the project's public keys.
- Keys are stored hashed. The publishable key stays visible in the
  dashboard; a **secret key is shown once**, when it is made.
- A secret key sent from a page (a request with an `Origin` header) is
  refused unless the project allows it.
- Make a second key and move your apps to it before revoking the first:
  revoking takes effect at the edge within seconds.

## Turning it on

Project → Settings → **API** → **Enable backend services**, or
`pgdock services enable <project>`. Enabling:

1. creates `<db>_edge`, `<db>_anon`, `<db>_user` and `<db>_service` and lets
   the pooler accept the edge login;
2. adds the `pgd_auth`, `pgd_storage` and `pgd_realtime` schemas, owned by
   the platform, with `pgd_auth.uid()`, `role()`, `claims()` and
   `claim(text)` for your row-level security policies;
3. grants the three request roles use of what the owner creates in
   `public` (row-level security decides which rows);
4. makes the first publishable and secret keys and the project's signing
   key.

```sql
ALTER TABLE todos ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_todos ON todos FOR ALL TO "<db>_user"
  USING (owner_id = pgd_auth.uid()) WITH CHECK (owner_id = pgd_auth.uid());
```

Turning it off revokes the keys and switches the edge login off. The
`pgd_*` schemas and their data stay, so turning it on again restores
everything except the keys, which are new.

Check that it works:

```sh
curl -H "apikey: pgd_pub_…" https://k7f3m2q9.api.pgdock.ng/data/v1/health
# {"status":"ok","project":"k7f3m2q9","role":"anon","region":"ng-lagos"}
```

## Settings

- **Allowed origins**: the pages that may call the API. Empty allows any
  origin, which is fine for the publishable key; list your app's origins
  once you go live.
- **Statement timeout**: each request's limit, 8 seconds by default and 15
  at most.
- **Rate limits**: requests per minute per IP address (600) and per key
  (12,000). Over the limit, the edge answers `429` with `Retry-After`.

A paused Free project answers `503 project_resuming` with `Retry-After` and
is woken, as a database connection would wake it. A suspended
organisation's projects answer `403 project_suspended`.

## Usage and logs

The edge counts each request that passed the key check (`api_requests`)
and the bytes it returned (`api_egress_gb`), per project and hour, and
sends them to pgdock-server every few seconds with the request log. A
report is recorded once even if it is retried. The request log (method,
path, status, latency, role, user, key, IP) is on the API page and at
`GET /api/v1/projects/{id}/services/logs`, kept 7 days.

## Running pgdock-edge

Run one pgdock-edge per region, on the region's nodes. It keeps no state.

| Setting | |
| --- | --- |
| `PGDOCK_API_DOMAIN` (server) | The API domain: projects are at `<ref>.<domain>`. |
| `PGDOCK_EDGE_SECRET` (server and edges) | At least 32 characters, the same everywhere. Without it pgdock-server serves no edge. |
| `PGDOCK_EDGE_CONTROL_URL` | pgdock-server's URL as the edge reaches it. |
| `PGDOCK_EDGE_DOMAIN` | The same as `PGDOCK_API_DOMAIN`. |
| `PGDOCK_EDGE_REGION` | The region it serves (empty for all). |
| `PGDOCK_EDGE_TLS_CERT`, `PGDOCK_EDGE_TLS_KEY` | A wildcard certificate for `*.<domain>`. |
| `PGDOCK_EDGE_LISTEN` | Default `:8443`. |
| `PGDOCK_EDGE_POOLER_ADDR` | Optional: the transaction pooler as the edge reaches it. |
| `PGDOCK_EDGE_TRUSTED_PROXIES` | Optional: CIDRs allowed to set `X-Forwarded-For`. |

`deploy/edge/Dockerfile` builds the image, and `deploy/edge/edge.env.example`
lists the settings.

- **DNS:** a wildcard record `*.<domain>` pointing at the region's edge.
- **TLS:** get the wildcard certificate with a DNS-01 client for your DNS
  provider (certbot, lego, acme.sh) and point the two settings at its
  files. The edge re-reads them when they change. Behind a TLS terminator,
  leave them empty and the edge serves plain HTTP.

The edge follows pgdock-server's configuration feed. A change, such as a
key revoked, an origin added, a project paused or an organisation
suspended, reaches it within a second or two, and it re-reads everything
every minute in case a change was missed. While pgdock-server is down, the
edge keeps serving from what it has and holds its reports, retrying them
later. `GET /healthz` on any host that isn't a project answers once the
first configuration has loaded.

## Not yet

These come in the next milestones (V4 §14): the data API's reads (M29) and
writes (M30), auth (M31–M32), storage (M33), realtime (M34) and read
replicas (M35). Rating the new usage on invoices and per-plan limits come
with billing (M37).
