# Backend services

Backend services (V4) let an app talk to its project over HTTPS, straight
from a browser or a phone, without a server of its own: the data API, auth,
storage and realtime. This page covers what is in place now: the edge gateway, project API
hostnames, API keys and the per-request roles and claims (V4-M28), and the
data API's reads (V4-M29). Writes, auth, storage and realtime arrive in the
milestones after them.

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

The `pgd_*` schemas are part of the database: backups, restores and
branches carry them. After a restore or a move, PGDock takes them back from
the project's owner (a restore recreates them as the owner's) and re-applies
the request roles' grants, within seconds. A promotion or demotion creates
the request roles on the new instance before copying.

Check that it works:

```sh
curl -H "apikey: pgd_pub_…" https://k7f3m2q9.api.pgdock.ng/data/v1/health
# {"status":"ok","project":"k7f3m2q9","role":"anon","region":"ng-lagos"}
```

## Reading data

`https://<ref>.<domain>/data/v1/<table>` reads a table or view in the
exposed schemas (`public` unless you choose others). Name a table in another
exposed schema as `schema.table`.

```sh
curl -G https://k7f3m2q9.api.pgdock.ng/data/v1/todos \
  -H "apikey: pgd_pub_…" -H "Authorization: Bearer $ACCESS_TOKEN" \
  --data-urlencode "select=id,title,done,owner:profiles(name)" \
  --data-urlencode "where=done:eq:false" \
  --data-urlencode "order=created_at:desc" --data-urlencode "limit=20"
# {"data":[{"id":3,"title":"c","done":false,"owner":{"name":"Ada"}}, …],"next_cursor":"eyJr…"}
```

| Parameter | |
| --- | --- |
| `select` | Columns (`*` by default), `alias:column`, JSON paths (`meta->plan`), and related rows through foreign keys: `author(name)` gives an object (or `null`) when this table points at it, and `comments(id,body)` an array when it points at this table. Name a relation by its table (or by the foreign key column without `_id`); when two foreign keys reach the same table, say which: `editor:profiles!editor_id(name)`. Up to 3 levels; arrays hold up to 1,000 rows. |
| `where` | `column:operator:value`, repeatable (all must hold). Operators: `eq`, `neq`, `lt`, `lte`, `gt`, `gte`, `in` (`a,b,"c,d"`), `like`, `ilike` (`%` wildcards), `is` (`null`, `true`, `false`), `contains`, `contained_by` (arrays, jsonb), `search` (full text, `websearch_to_tsquery`). JSON paths compare as text: `where=meta->tag:eq:work`. |
| `or` | One group of conditions any of which may hold: `or=status:eq:draft,owner_id:eq:…`. Repeatable; each group must hold. |
| `order` | `column:asc` or `column:desc`, comma-separated. The primary key is added so pages are stable. |
| `limit` | Default 100, at most 1,000. |
| `cursor` | The `next_cursor` of the previous page (keyset pagination, for any depth). `offset` also works, up to 10,000. |
| `count` | `exact` (capped at 2 seconds, `null` if it ran out) or `estimated` (the planner's). |

- **One row:** `GET /data/v1/todos/42` (single-column primary keys) returns
  `{"data":{…}}`, or `404` when it doesn't exist or the caller can't see it.
- **Complex queries:** `POST /data/v1/todos/query` with
  `{"select":"…","where":{"or":[{"column":"done","op":"eq","value":true},{"not":{…}}]},"order":"title:desc","limit":50}`;
  `and`, `or` and `not` nest.
- **The project's OpenAPI document:** `GET /data/v1/openapi.json` with the
  secret key, generated from the current schema.

### Row-level security comes first

With the publishable key, requests run as `<db>_anon` (or `<db>_user` with a
signed-in user's token), and **a table without row-level security is not
readable**: the API answers `403 rls_required`. Enable it and write policies,
or, for data anyone may read, list the table under **Public tables**.
Views must run as the caller (`CREATE VIEW … WITH (security_invoker = true)`)
so the underlying tables' policies apply; embedded tables are checked the
same way. The secret key bypasses row-level security entirely.

```sql
ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
CREATE POLICY readable ON posts FOR SELECT
  USING (published OR author_id = pgd_auth.uid());
```

Column privileges hold too: `REVOKE SELECT (email) ON profiles FROM "<db>_anon"`
hides a column from anonymous callers.

### Guardrails

- Each request has its statement timeout (8 s by default), at most 30
  filters and 3 levels of relations, and a 10 MB result.
- Every new shape of query is planned once (`EXPLAIN`) and refused with
  `400 query_too_expensive` when its estimated cost is over the project's
  limit (1,000,000 by default: a scan of a few million rows). Add an index or
  a filter, or raise the limit in the settings.
- Schema changes are picked up within about two seconds, with no restart.

### Errors

```json
{"error":{"code":"unknown_column","message":"todos has no column \"nope\"","request_id":"req_…"}}
```

| Status | Codes |
| --- | --- |
| 400 | `invalid_filter`, `invalid_select`, `invalid_value`, `unknown_column`, `unknown_relation`, `ambiguous_relation`, `invalid_cursor`, `invalid_limit`, `query_too_expensive`, … |
| 401 | `key_required`, `invalid_key`, `invalid_token` |
| 403 | `rls_required`, `permission_denied`, `secret_key_in_browser`, `origin_not_allowed`, `project_suspended` |
| 404 | `unknown_table`, `not_found` |
| 413 | `result_too_large` |
| 429 | `rate_limited` |
| 503, 504 | `project_resuming`, `database_unavailable`, `statement_timeout` |

## Settings

- **Allowed origins**: the pages that may call the API. Empty allows any
  origin, which is fine for the publishable key; list your app's origins
  once you go live.
- **Statement timeout**: each request's limit, 8 seconds by default and 15
  at most.
- **Rate limits**: requests per minute per IP address (600) and per key
  (12,000). Over the limit, the edge answers `429` with `Retry-After`.
- **Exposed schemas**: the schemas the data API serves (`public`). Exposing
  another grants the request roles use of what the owner makes there.
  Platform schemas (`pgd_*`, `pg_*`, `pgdock`) can't be exposed.
- **Public tables**: tables and views the publishable key may read without
  row-level security. Anyone with your app can read them.
- **Maximum query cost**: the cost guard's limit (above).

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

These come in the next milestones (V4 §14): the data API's writes, functions
and type generation (M30), auth (M31–M32), storage (M33), realtime (M34) and
read replicas (M35). Rating the new usage on invoices and per-plan limits come
with billing (M37).
