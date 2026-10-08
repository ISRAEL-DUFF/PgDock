Markdown · pgdock-v4-spec.md

# PGDock — V4 Specification & Build Plan

*Builds on the V1–V3 specs and the Infrastructure Cost & Phasing doc. References like "V3 §2.1" point to those documents.*

|  |  |
| --- | --- |
| **Status** | Draft for review |
| **Theme** | Backend services on top of the database platform: data API, auth, storage, realtime, read replicas |
| **API strategy** | **PGDock's own APIs and SDKs.** Not Supabase-compatible. A migration helper imports users, files, and data from Supabase projects (§9). |
| **Builds on** | V3: billing and usage rating, regions, WireGuard mesh, HA, free-tier pause/archive, provider interface |

---

## 1. Overview

### 1.1 What V4 is for

V1–V3 made PGDock a reliable, billable managed Postgres platform. V4 lets a team build a whole app backend on it without writing a server:

- Read and write data over HTTPS, safely, straight from a browser or mobile app (§3).
- Sign users up and in, with the phone-first methods Nigerian apps need (§4).
- Store and serve files and images (§5).
- Push live updates to connected clients (§6).
- Scale reads on dedicated projects with replicas (§7).
- Use typed SDKs for TypeScript, Dart/Flutter, and Go (§8).

### 1.2 Why our own APIs

|  | Own APIs (chosen) | Supabase-compatible (rejected) |
| --- | --- | --- |
| Control | Full: API shape, versioning, roadmap | Follow Supabase's changes indefinitely |
| Multi-tenancy | Designed in from the start | PostgREST and GoTrue assume one database or project per process |
| Fit | Go services in the existing monorepo, sharing auth, metering, and tenancy code | Mixed stack (Haskell, Elixir, TypeScript) to operate |
| Differentiation | Phone and WhatsApp OTP, naira metering, Nigeria-first defaults built in | Constrained to Supabase's feature set |
| Migration cost for customers | Client code is rewritten; data, users, and files are imported by the migration helper (§9) | URL and key change only |

### 1.3 Not in V4

Edge functions (sandboxed customer code), GraphQL, custom domains for the API, organisation SSO/SAML, dollar billing and international payment processors, a Terraform provider, schema diff, branch data masking. These move to V5 (§15).

### 1.4 Principles

- **Secure by default.** Client-facing access goes through row-level security. A table without RLS is not reachable with a client key unless the developer explicitly opts in (§3.6).
- **Postgres is the source of truth.** Auth users, storage metadata, and realtime subscriptions are rows in the project's own database, governed by the same RLS policies. Nothing about a customer's app lives only in PGDock's metadata.
- **One request, one transaction.** Every data API call runs in a single transaction with the caller's role and claims set locally, so it works through the pooler in transaction mode.
- **Multi-tenant services.** Each service runs once per region for all projects. A free project costs a few rows of config, not a set of processes.
- **Metered from day one.** Every service emits usage records (V2 §10.9) that V3 billing rates.
- **Versioned.** Every public API path carries a version (`/data/v1`, `/auth/v1`, …). Breaking changes ship as a new version with at least 12 months of overlap.

---

## 2. Architecture

### 2.1 The edge service

A new Go binary, **`pgdock-edge`**, built from the existing monorepo. It contains the gateway and all four services as modules; each can run in the same process (small regions) or as separate processes (scaling a hot module independently).

```
Client app ──HTTPS/WSS──▶ <ref>.api.pgdock.ng
                              │
                    ┌─────────▼──────────────────────────────────────────┐
                    │ pgdock-edge (per region)                            │
                    │  Gateway: TLS, key check, CORS, rate limit, metering│
                    │   ├─ /data/v1      Data API        (§3)             │
                    │   ├─ /auth/v1      Auth            (§4)             │
                    │   ├─ /storage/v1   Storage         (§5)             │
                    │   └─ /realtime/v1  Realtime (WSS)  (§6)             │
                    └──────┬─────────────────────┬────────────────────────┘
                           │ mesh                 │
              Edge pooler (transaction mode)   Object storage (per region)
                           │
                Project database (shared or dedicated)
```

- **Hostname:** each project gets `https://<ref>.api.pgdock.ng`, where `<ref>` is a short random project reference (e.g. `k7f3m2q9`). Region is resolved from the ref; DNS uses a wildcard per region pointing at that region's edge.
- **Placement:** `pgdock-edge` runs on the region's existing nodes at first (it's stateless). Dedicated edge nodes are added through capacity proposals (V3 §5.2) when traffic justifies it.
- **Database access:** through the edge pooler in transaction mode, over the mesh (V3 §2.1.1), using a per-project `<db>_edge` login role (§2.3).
- **Configuration:** the edge keeps an in-memory cache of each project's settings (keys, JWT keys, CORS, rate limits, auth config, buckets). The control plane pushes changes over a streaming channel; the edge also reloads a project's config on a version mismatch. Cold lookups fall back to the metadata DB.
- **TLS:** a wildcard certificate per region (`*.api.us.pgdock.ng`-style or `*.api.pgdock.ng` with region routing), obtained by DNS-01.

### 2.2 Project API keys

| Key | Prefix | Where it's used | Database role |
| --- | --- | --- | --- |
| **Publishable key** | `pgd_pub_` | Browsers and mobile apps. Safe to embed. | `<db>_anon` until the user signs in, then `<db>_user` |
| **Secret key** | `pgd_sec_` | Servers only. Bypasses RLS. | `<db>_service` |

- Keys are random, shown once (secret) or always visible (publishable), and stored hashed (SHA-256). Projects can have several of each, with names, for rotation without downtime.
- The secret key is refused from browsers: requests with an `Origin` header and a secret key are rejected unless the project explicitly allows it. The dashboard and SDK docs warn about it.
- User identity travels separately as a JWT in `Authorization: Bearer <access token>` (§4.4). The publishable key identifies the project; the JWT identifies the user.

### 2.3 Roles and request context

V4 adds four roles per project database (created when backend services are enabled):

| Role | Purpose |
| --- | --- |
| `<db>_edge` | Login role used by `pgdock-edge`. `NOINHERIT`; can `SET ROLE` to the three below and nothing else. |
| `<db>_anon` | Unauthenticated client requests. |
| `<db>_user` | Signed-in users. |
| `<db>_service` | Secret-key requests. `BYPASSRLS`, still not a superuser. |

**Per-request flow** (one transaction):

```sql
BEGIN;
SET LOCAL ROLE p_7f3k9x2m4q_user;
SELECT set_config('pgd.claims', '{"sub":"…","role":"user","phone":"+234…"}', true);
SET LOCAL statement_timeout = '8s';
-- the generated query
COMMIT;
```

Helper functions in a `pgd_auth` schema (owned by `pgdock_admin`, `STABLE`, `SECURITY INVOKER`) read the claims for RLS policies:

```sql
pgd_auth.uid()     -- uuid of the signed-in user, or NULL
pgd_auth.role()    -- 'anon' | 'user' | 'service'
pgd_auth.claims()  -- full claims as jsonb
pgd_auth.claim(text) -- one claim, e.g. pgd_auth.claim('org_id')
```

Example policy a developer writes:

```sql
ALTER TABLE todos ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_todos ON todos
  FOR ALL TO p_7f3k9x2m4q_user
  USING (owner_id = pgd_auth.uid())
  WITH CHECK (owner_id = pgd_auth.uid());
```

The table editor (V2 §4) gains an **RLS policy helper** with templates ("owner only", "members of an org", "public read, owner write") that generate this SQL with a preview, and friendly role names (`anon`, `user`, `service`) shown instead of the opaque ones.

### 2.4 Enabling backend services

Backend services are **per project and opt-in**: Project → Settings → Backend services → Enable. Enabling:

1. Creates the roles in §2.3, the `pgd_auth`, `pgd_storage`, and `pgd_realtime` schemas, and their tables (§11.2).
2. Generates the first publishable and secret keys and the project's JWT signing key (§4.4).
3. Registers the project with the region's edge and creates its DNS name.
4. Shows a quick-start with SDK snippets.

Disabling stops the edge from serving the project and revokes keys. The schemas and data stay until the developer drops them, so re-enabling restores everything.

### 2.5 Interaction with existing platform features

| Feature | Behaviour with backend services |
| --- | --- |
| Promotion / demotion / logical moves (V3 §2.3) | Roles and `pgd_*` schemas move with the database; the edge follows the pooler route, so the API URL doesn't change. |
| Branching (V2 §8) | Branches get their own ref, keys, and JWT key. Auth users are copied with the data (with the sensitive-data defaults, V2 §8.5); storage objects are **not** copied by default (metadata only, with an option to copy files). |
| Free-tier pause (V3 §4.2) | API requests to a paused project return `503` with `Retry-After` and trigger a resume, like the Postgres waker. |
| Suspension (V2 §10.8) | The edge refuses all requests for the org's projects. |
| Data residency (V3 §6.3) | Storage objects stay in the region's in-country object store; auth SMS and email providers are listed as sub-processors. |
| Read replicas (§7) | `GET` data API requests can be routed to replicas. |

---

## 3. Data API

### 3.1 Scope

Automatic HTTPS endpoints for every table, view, and function in the schemas the project exposes (default: `public`). The edge introspects the schema, caches it per project, and refreshes it on DDL (an event trigger bumps a schema version that the edge watches) or on demand.

Base path: `https://<ref>.api.pgdock.ng/data/v1`

### 3.2 Reading

```
GET /data/v1/{table}
    ?select=id,title,created_at,author(name,avatar_url)
    &where=status:eq:published
    &where=created_at:gte:2026-10-01
    &order=created_at:desc
    &limit=20
    &cursor=eyJpZCI6...
```

| Parameter | Meaning |
| --- | --- |
| `select` | Columns, plus **embedded relations** through foreign keys (`author(name)`, `comments(id,body)`). Nesting up to 3 levels. Aliases with `alias:column`. |
| `where` | Repeatable. `column:operator:value`. Operators: `eq`, `neq`, `lt`, `lte`, `gt`, `gte`, `in` (comma list), `like`, `ilike`, `is` (`null`, `true`, `false`), `contains`, `contained_by` (arrays/jsonb), `search` (full-text). Combined with AND. |
| `or` | `or=status:eq:draft,owner_id:eq:…` for OR groups. |
| JSON paths | `where=meta->plan:eq:pro`, `select=meta->plan`. |
| `order` | \`column:asc |
| `limit` | Default 100, maximum 1,000. |
| `cursor` | Keyset pagination (opaque, encodes the order key of the last row). The response includes `next_cursor`. `offset` is also accepted, up to 10,000. |
| `count` | `exact`, `estimated`, or omitted. Exact counts are capped by a timeout. |

**Single row:** `GET /data/v1/{table}/{primary key}`.

**Complex queries:** `POST /data/v1/{table}/query` with the same structure as JSON (nested AND/OR, typed values), for filters that are awkward in a URL.

**Response:**

```json
{
  "data": [ { "id": "…", "title": "…", "author": { "name": "…" } } ],
  "next_cursor": "eyJpZCI6...",
  "count": 128
}
```

### 3.3 Writing

| Operation | Request |
| --- | --- |
| Insert one or many | `POST /data/v1/{table}` with an object or array (max 1,000 rows) |
| Upsert | `POST /data/v1/{table}?on_conflict=email` (`merge` or `ignore`) |
| Update by key | `PATCH /data/v1/{table}/{pk}` |
| Update by filter | `PATCH /data/v1/{table}?where=…` (**a filter is required**; unfiltered updates are refused) |
| Delete by key / filter | `DELETE /data/v1/{table}/{pk}` or `?where=…` (filter required) |
| Return rows | \`?return=minimal |
| Transactions across tables | `POST /data/v1/batch` with an ordered list of operations, executed in one transaction, max 50 operations |

Updates and deletes by filter also accept `max_affected` (default 1,000): if more rows would change, the transaction rolls back with an error.

### 3.4 Functions (RPC)

`POST /data/v1/rpc/{function}` with named arguments as JSON. Functions in exposed schemas are callable if the caller's role has `EXECUTE`. `STABLE`/`IMMUTABLE` functions can also be called with `GET` (cacheable, replica-routable). Set-returning functions accept `select`, `where`, `order`, and `limit` on their output.

### 3.5 Errors

```json
{ "error": { "code": "not_null_violation", "message": "title is required",
             "details": { "column": "title", "pg_code": "23502" }, "request_id": "req_…" } }
```

Stable, documented error codes mapped from Postgres error classes; HTTP status follows (`400` validation, `401` no or invalid JWT, `403` RLS/permission, `404`, `409` conflict, `422` constraint, `429` rate limit, `503` paused or resuming).

### 3.6 Exposure and safety

- **Exposed schemas** are chosen per project (default `public`). `pgd_*` schemas are never exposed.
- **RLS gate:** for `anon` and `user` roles, a table without RLS enabled returns `403 rls_required` unless the developer marks it **public** in settings (with a warning). Views must be `security_invoker`; others are flagged.
- **Security advisor** in the dashboard: tables without RLS, policies that allow everything, `SECURITY DEFINER` functions in exposed schemas, columns named like secrets (`password`, `token`) exposed to `anon`.
- **Column privileges** are respected (`GRANT SELECT (col)`), so sensitive columns can be hidden per role.
- **Guardrails:** `statement_timeout` (default 8 s, per plan), result size cap (10 MB), embed depth (3), filter count (30), and a **query cost check**: the edge runs `EXPLAIN` for queries not seen before and refuses ones whose estimated cost exceeds the plan's limit (results cached per normalised query shape).

### 3.7 Generated artefacts

- **Per-project OpenAPI document** at `/data/v1/openapi.json` (secret key), regenerated with the schema.
- **Type generation:** `pgdock gen types --lang ts|dart|go --project <p>` produces typed table and function definitions for the SDKs (§8).
- **API docs page** in the dashboard per table, with request examples in each SDK.

### 3.8 Caching

`GET` responses for requests made with only the publishable key (no user JWT) can be cached at the edge when the project sets a cache TTL per table or function (default off). Writes to a table invalidate its cached entries via the same schema-version and change signals realtime uses.

---

## 4. Auth

### 4.1 Sign-in methods

| Method | Notes |
| --- | --- |
| **Email + password** | argon2id; password strength rules per project; email confirmation optional/required |
| **Magic link and email OTP** | 6-digit code or link, 10-minute validity |
| **Phone OTP via SMS** | Nigerian numbers normalised to E.164 (`+234…`); SMS sent through a pluggable provider (§4.6) |
| **Phone OTP via WhatsApp** | Through the WhatsApp Business Platform; often more reliable and cheaper than SMS for Nigerian users |
| **OAuth** | Google, Apple, GitHub, Facebook, Microsoft at launch. Each project supplies its own client ID/secret (shared PGDock dev credentials allowed on Free for testing only) |
| **Anonymous sign-in** | Creates a user without credentials, upgradable later by linking a method |
| **MFA** | TOTP and phone OTP as a second factor; per-project policy (optional, required for some users via a claim, or required for all) |

Users can link several identities (e.g. phone and Google) to one account.

### 4.2 Where users live

Users are stored **in the project's own database**, in the `pgd_auth` schema (owned by `pgdock_admin`):

- `pgd_auth.users` — id, email, phone, confirmation timestamps, metadata (app and user), banned_until, created_at, last_sign_in_at.
- `pgd_auth.identities`, `pgd_auth.sessions`, `pgd_auth.refresh_tokens`, `pgd_auth.mfa_factors`, `pgd_auth.one_time_codes`, `pgd_auth.audit_log`.

Developers can reference `pgd_auth.users(id)` in foreign keys and read a safe view, `pgd_auth.user_profiles` (no secrets), from their own SQL. Password hashes, tokens, and factors are never readable by the `anon`, `user`, or `service` roles.

Consequences: users move with the database on promotion, branching, and restore; exports include them; data residency covers them.

### 4.3 Endpoints (selection)

```
POST /auth/v1/signup                 email/password or phone
POST /auth/v1/signin/password
POST /auth/v1/signin/otp             request code (email | sms | whatsapp)
POST /auth/v1/verify                 verify code or magic-link token
GET  /auth/v1/authorize?provider=google&redirect_to=…    OAuth (PKCE)
GET  /auth/v1/callback
POST /auth/v1/token                  refresh (rotation)
POST /auth/v1/signout                current session or all
GET  /auth/v1/user · PATCH           current user, update email/phone/password/metadata
POST /auth/v1/mfa/enroll · /challenge · /verify
POST /auth/v1/recover                password reset
GET  /auth/v1/.well-known/jwks.json  public signing keys

Admin (secret key):
GET|POST|PATCH|DELETE /auth/v1/admin/users[/:id]   list, create, invite, ban, delete
POST /auth/v1/admin/users/:id/signout
POST /auth/v1/admin/generate-link                  magic link / invite / recovery
```

### 4.4 Sessions and tokens

- **Access token:** a JWT signed with the project's **ES256** key, 1-hour lifetime by default (configurable 5 minutes–24 hours). Claims: `sub`, `role`, `aud` (project ref), `exp`, `iat`, `session_id`, `aal` (MFA level), `email`/`phone` if present, `app_metadata`, plus custom claims (§4.7).
- **Refresh token:** opaque, single-use, **rotated on every refresh**. Reuse of an already-rotated token revokes the whole session family (theft detection).
- **Signing keys:** generated per project, private key encrypted with the master key, public keys published at the JWKS endpoint. Rotation adds a new key, signs with it, and keeps the old one for verification until outstanding tokens expire.
- Developers' own servers verify tokens with the JWKS endpoint; no shared secret is ever handed out.
- Session controls: maximum session length, inactivity timeout, single-session-per-user option, and "sign out all devices".

### 4.5 Email

- Templates per project (confirm, magic link, OTP, recovery, invite, email change) with variables and a preview.
- **Platform email** works out of the box with a low rate limit (e.g. 30 emails/hour per project) for development. **Production projects configure their own SMTP or email provider**; the dashboard nudges this before launch.
- Bounce and complaint handling where the provider supports webhooks.

### 4.6 SMS and WhatsApp providers

- Pluggable `MessageProvider` interface (same composable pattern as payments, V3 §3.4.1).
- **Platform-provided:** PGDock holds accounts with one or two SMS providers that deliver well to Nigerian networks, plus a WhatsApp Business account, and **resells at cost plus margin**, metered per message (§12). Provider choice is an open question (§16).
- **Bring your own:** projects can configure their own provider credentials (Twilio, Africa's Talking, Termii, or similar), paying the provider directly.
- **Abuse controls:** per-number and per-IP rate limits, a daily send cap per project (from the plan), country allow-list (default: Nigeria only, others opt-in), and optional captcha on OTP requests. SMS pumping fraud is the main risk; caps and country limits are the defence.
- DND (do-not-disturb) registered numbers: use the provider's transactional/DND route for OTPs, and document the behaviour.

### 4.7 Hooks

Hooks let developers customise auth without running a server:

| Hook | Implemented as | Use |
| --- | --- | --- |
| **Custom claims** | A Postgres function `(event jsonb) returns jsonb`, called when a token is issued | Add `org_id`, roles, plan to the JWT |
| **Before sign-up** | Postgres function or signed HTTP webhook | Block domains, require invite codes |
| **After sign-up / sign-in** | Signed HTTP webhook (reuses V2 webhook delivery, retries, and SSRF protection) | Create profile rows, notify CRM |
| **Send message** | Signed HTTP webhook | Replace PGDock's email/SMS sending entirely |

Postgres hooks run with a 2-second timeout as a dedicated `<db>_auth_hook` role with only the grants the developer gives it.

### 4.8 Security

- Rate limits per endpoint, IP, and identifier (email/phone), with exponential lockout on repeated failures.
- Optional captcha (Cloudflare Turnstile) on sign-up, sign-in, and OTP requests.
- Redirect URL allow-list for OAuth and magic links; exact matching by default, wildcards opt-in.
- Leaked-password check (k-anonymity range lookup against a breached-password corpus) as an option.
- Auth audit log in `pgd_auth.audit_log` and the dashboard (sign-ins, failures, MFA changes, admin actions).
- Bans and deletions take effect at next token refresh; "sign out everywhere" revokes refresh tokens immediately, and a short access-token lifetime bounds the window.

### 4.9 Dashboard

Project → Auth: users list (search by email, phone, id), user detail (identities, sessions, MFA, metadata, audit), invite, ban, delete, sign out; providers configuration; templates; SMS/WhatsApp settings and spend; hooks; policies helper link; usage (MAU, messages).

---

## 5. Storage

### 5.1 Model

- **Buckets** per project: public or private, with a maximum file size and an allowed MIME-type list.
- **Objects** are stored in the region's object store (R2 by default; the in-country store for data-residency projects, V3 §6.3) under `projects/<ref>/<bucket>/<path>`.
- **Metadata lives in the project database** in `pgd_storage.buckets` and `pgd_storage.objects` (path, size, MIME type, checksum, owner, user metadata, timestamps).
- **Access is governed by RLS on `pgd_storage.objects`.** Developers write policies like "users can read and write objects under `avatars/<their uid>/`", using helper functions:

```sql
CREATE POLICY own_avatar ON pgd_storage.objects FOR ALL TO p_7f3k9x2m4q_user
  USING (bucket = 'avatars' AND pgd_storage.folder(path, 1) = pgd_auth.uid()::text)
  WITH CHECK (bucket = 'avatars' AND pgd_storage.folder(path, 1) = pgd_auth.uid()::text);
```

The edge checks access by running the corresponding metadata query under the caller's role before touching the object store, so the same policy language covers data and files.

### 5.2 Endpoints (selection)

```
POST   /storage/v1/object/{bucket}/{path}          upload (≤ 50 MB direct)
POST   /storage/v1/upload/{bucket}/{path}          start a large upload → presigned multipart URLs
POST   /storage/v1/upload/{id}/complete
GET    /storage/v1/object/{bucket}/{path}          download (private: auth required)
GET    /storage/v1/public/{bucket}/{path}          public bucket download (CDN-cached)
POST   /storage/v1/sign/{bucket}/{path}            signed download URL (expiry)
POST   /storage/v1/sign-upload/{bucket}/{path}     signed upload URL (for direct client uploads)
GET    /storage/v1/list/{bucket}?prefix=&cursor=
POST   /storage/v1/move · /copy
DELETE /storage/v1/object/{bucket}  (body: list of paths)
GET    /storage/v1/render/{bucket}/{path}?width=&height=&fit=&format=&quality=   image transform
```

### 5.3 Uploads

- **Direct uploads** up to 50 MB go through the edge (streamed, checksummed, MIME sniffed and checked against the bucket's allow-list).
- **Large uploads** (up to the plan limit, e.g. 5 GB) use **presigned multipart upload URLs** straight to the object store, so bytes don't pass through the edge; completion is confirmed through the API, which writes the metadata row in a transaction.
- **Resumable:** multipart parts can be retried individually; uploads not completed within 24 hours are aborted and cleaned up.
- Uploads respect RLS (`INSERT` policy on `pgd_storage.objects`) and the org's storage quota.

### 5.4 Downloads and CDN

- **Private objects:** streamed by the edge after the RLS check, or served through **signed URLs** (default 1-hour expiry, configurable) that the object store or CDN validates without hitting the database.
- **Public buckets:** served through Cloudflare's CDN in front of R2, with cache headers per bucket. Making a bucket private purges its CDN cache.
- Range requests supported (video and audio seeking).

### 5.5 Image transforms

- On-the-fly resize, crop (`cover`, `contain`, `fill`), format conversion (WebP, AVIF, JPEG, PNG), and quality, using libvips from Go.
- Results cached in the object store (`_transforms/` prefix) and at the CDN; repeated requests don't re-render.
- Limits: source images up to 25 MP and 25 MB; output up to 2,500 px per side; per-plan monthly transform quota. Disabled by default on Free, or a small quota.

### 5.6 Data hygiene

- Deleting an object row (through the API) deletes the stored object asynchronously; a nightly reconciler removes orphaned objects (bytes without metadata, after 7 days) and flags metadata without bytes.
- Deleting a project schedules deletion of all its objects; data-residency and retention rules from V2 §10.10 apply.
- Optional malware scanning (ClamAV) for uploads to buckets that enable it, as a later add-on (V5 if not done here).

---

## 6. Realtime

### 6.1 Channels

Clients open one WebSocket (`wss://<ref>.api.pgdock.ng/realtime/v1`) and join channels:

| Channel type | What it delivers |
| --- | --- |
| **Database changes** | Inserts, updates, deletes on chosen tables, with optional filters (`eq`, `in` on one column), **filtered by RLS** for the subscriber |
| **Broadcast** | Low-latency messages from client to clients on the same channel (chat, cursors, typing), optionally persisted |
| **Presence** | Who is online on a channel, with per-client state, synced across clients |

Channel authorisation: database-change channels check RLS (§6.3). Broadcast and presence channels are public by default or **private**, in which case joining is authorised by an RLS policy on `pgd_realtime.channel_access` (the developer writes "users can join `room:<id>` if they're a member").

### 6.2 Change capture

Realtime reuses V2's transactional outbox (V2 §9.1), extended:

- Enabling realtime on a table installs the same `AFTER` row trigger (shared function, owned by `pgdock_admin`), writing to `pgd_realtime.outbox`.
- `pgdock-edge` holds one `LISTEN` connection per active project (only while it has subscribers), plus polling as a fallback, and reads outbox rows in commit order.
- Because capture happens in the same transaction, rolled-back changes are never delivered.
- **Why not logical decoding:** a replication slot per database doesn't scale on shared clusters (hundreds of databases, each slot holding WAL if a consumer stalls). The outbox has bounded, observable cost per table. Dedicated projects can opt into logical-decoding capture later (V5) for high-volume tables.

### 6.3 RLS filtering

For each change, the edge must decide which subscribers may see the row:

1. Subscribers on the same table and filter are grouped by **claims fingerprint** (role + the claims that policies on that table reference, detected from policy definitions; otherwise the full `sub`).
2. For each group, the edge runs `SELECT 1 FROM <table> WHERE <pk> = $1` under that group's role and claims (one transaction, batched for multiple rows).
3. Visible → deliver to every subscriber in the group. Deletes are delivered with the primary key only (the row no longer exists to check), to subscribers who could previously see it per the edge's per-subscriber cache.

Limits keep this affordable: subscribers per channel, groups checked per change (plan limit), and a per-project change rate cap. Above the cap, changes are coalesced and clients receive a `resync` signal to refetch.

### 6.4 Delivery semantics

- **At-most-once to connected clients**, in commit order per table. Missed messages while disconnected are not replayed; clients refetch on reconnect (the SDK does this automatically for subscribed queries).
- Heartbeats every 25 seconds; idle connections closed after 2 minutes without heartbeat.
- Broadcast messages can be persisted (optional, per channel) for history up to 7 days.

### 6.5 Scaling

- Each region's edge handles realtime for its projects. Multiple edge processes share subscriber state through a small pub/sub layer (Postgres `LISTEN/NOTIFY` on the metadata DB at first; NATS if load requires it).
- Connection limits and message quotas per plan (§12).

---

## 7. Read Replicas

- **Dedicated projects only.** Up to 2 replicas per project in V4 (more later).
- A replica is a **streaming standby** (same mechanism as HA standbys, V3 §2.2), placed on another node, possibly another region (for read latency near users).
- **Endpoints:** the pooler gets a read-only route `<db>_ro` (`postgresql://…@db.us.pgdock.ng:6543/<db>_ro`) that load-balances across healthy replicas. The data API routes `GET` requests to replicas when the request has `Read-Replica: allowed` or the project enables it by default for publishable-key reads.
- **Lag:** shown in the dashboard; replicas lagging more than a threshold (default 10 s) are removed from rotation until they catch up.
- **HA interaction:** replicas follow the new primary after a failover (Patroni-managed). A replica can be promoted to a standalone project (detach) for analytics or migration.
- **Billing:** each replica bills like a dedicated instance of its size.

---

## 8. SDKs and Developer Experience

### 8.1 SDKs

| SDK | Priority | Why |
| --- | --- | --- |
| **TypeScript/JavaScript** (`@pgdock/client`) | Launch | Web and React Native apps; most common client |
| **Dart/Flutter** (`pgdock` on pub.dev) | Launch | Flutter is widely used by Nigerian mobile teams |
| **Go** (`github.com/…/pgdock-go`) | Launch | Servers; same language as PGDock |
| Kotlin, Swift, Python | V5 | As demand appears |

All SDKs share one design: a client created from the project URL and publishable key; `client.data`, `client.auth`, `client.storage`, `client.realtime`; typed results from generated types (§3.7); automatic token refresh; consistent error types.

```ts
const pgd = createClient("https://k7f3m2q9.api.pgdock.ng", "pgd_pub_…");

await pgd.auth.signInWithOtp({ phone: "+2348012345678", channel: "whatsapp" });
await pgd.auth.verifyOtp({ phone: "+2348012345678", code: "123456" });

const { data } = await pgd.data.from("todos")
  .select("id, title, done, list(name)")
  .where("done", "eq", false)
  .order("created_at", "desc")
  .limit(20);

pgd.realtime.channel("todos").onChange({ table: "todos" }, (e) => refresh(e));
await pgd.storage.bucket("avatars").upload(`${user.id}/me.jpg`, file);
```

### 8.2 CLI additions

`pgdock gen types`, `pgdock keys list|create|revoke`, `pgdock auth users …`, `pgdock storage buckets|ls|cp|rm`, `pgdock policies list|lint`, `pgdock logs api --follow`.

### 8.3 Dashboard additions

API section per project: keys, URL, quick-start, per-table API docs, request explorer (run API calls as anon, a chosen user, or service), **API logs** (method, path, status, latency, role, user id; 7 days), security advisor, usage per service.

---

## 9. Migration Helper (from Supabase and others)

Own APIs mean client code changes, but the **data, users, and files** move with tooling:

- **Database:** V1 import (V1 §6.8), now with an option to keep `public` schema RLS policies and rewrite Supabase role names and helper calls to PGDock equivalents (`auth.uid()` → `pgd_auth.uid()`, `authenticated` → `<db>_user`, `anon` → `<db>_anon`), with a report of anything it couldn't rewrite.
- **Auth users:** imports users and identities from a Supabase project (via its database `auth` schema). Password hashes in bcrypt are accepted as-is and **transparently upgraded to argon2id** at each user's next sign-in. OAuth identities keep their provider IDs, so users sign in with Google etc. without re-linking. Sessions are not migrated; users sign in again once.
- **Storage:** copies buckets and objects from a Supabase project (S3 API) into PGDock storage, preserving paths and metadata, and recreates storage policies in PGDock's helper-function form where they map cleanly.
- **Client code guide:** a side-by-side mapping of common `supabase-js` calls to `@pgdock/client` calls.

---

## 10. Plans, Quotas, and Abuse

### 10.1 Inclusions by plan (indicative; final numbers from cost attribution, V3 §3.1)

|  | Free | Pro | Team |
| --- | --- | --- | --- |
| Data API requests | 500k/month | 5M included, then metered | 25M included, then metered |
| Auth monthly active users (MAU) | 10k | 50k included, then metered | 200k included, then metered |
| SMS / WhatsApp OTP | Not included (BYO provider only) | Metered at cost + margin | Metered at cost + margin |
| Storage | 1 GB, 5 GB egress | 50 GB included, then metered | 200 GB included, then metered |
| Image transforms | 500/month | 10k included, then metered | 50k included, then metered |
| Realtime | 100 concurrent connections, 1M messages | 1,000 / 10M included, then metered | 5,000 / 50M included, then metered |
| Read replicas | — | Dedicated projects, billed per instance | Same |
| API request timeout | 5 s | 8 s | 15 s |

### 10.2 Enforcement

- Per-key and per-IP rate limits at the gateway (token buckets), plus per-project ceilings from the plan.
- Free projects hitting a limit get `429` with a clear message; paid projects are metered past inclusions, subject to the org's spend cap (V3 §3.10), which throttles rather than breaks the app.
- Outbound abuse (auth webhooks, hooks) shares the V2 per-org outbound limits.
- SMS caps per project per day, country allow-lists, and fraud alerts on unusual send volume.

---

## 11. Data Model Changes

### 11.1 Metadata database

```sql
CREATE TABLE project_services (
  project_id     uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  ref            text UNIQUE NOT NULL,              -- k7f3m2q9
  enabled        boolean NOT NULL DEFAULT false,
  exposed_schemas text[] NOT NULL DEFAULT '{public}',
  public_tables  text[] NOT NULL DEFAULT '{}',      -- explicitly exempt from the RLS gate
  cors_origins   text[] NOT NULL DEFAULT '{}',
  settings       jsonb NOT NULL DEFAULT '{}',       -- timeouts, cache TTLs, replica routing, limits
  config_version bigint NOT NULL DEFAULT 1,
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE project_api_keys (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind        text NOT NULL CHECK (kind IN ('publishable','secret')),
  name        text NOT NULL,
  key_hash    text UNIQUE NOT NULL,
  prefix      text NOT NULL,                        -- for display
  last_used_at timestamptz,
  revoked_at  timestamptz,
  created_by  uuid REFERENCES users(id),
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE project_jwt_keys (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kid         text UNIQUE NOT NULL,
  algorithm   text NOT NULL DEFAULT 'ES256',
  public_jwk  jsonb NOT NULL,
  private_enc bytea NOT NULL,                       -- encrypted with master key
  status      text NOT NULL,                        -- active | verifying | retired
  created_at  timestamptz NOT NULL DEFAULT now(),
  retired_at  timestamptz
);

CREATE TABLE project_auth_config (
  project_id  uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  config      jsonb NOT NULL,                       -- methods, token lifetimes, MFA policy, redirects, captcha, password rules
  providers_enc bytea,                              -- OAuth client secrets, SMTP and SMS credentials (encrypted)
  templates   jsonb NOT NULL DEFAULT '{}'
);

CREATE TABLE message_sends (                        -- SMS/WhatsApp metering and fraud monitoring
  id          bigserial PRIMARY KEY,
  project_id  uuid NOT NULL,
  channel     text NOT NULL,                        -- sms | whatsapp | email
  provider    text NOT NULL,
  country     text,
  status      text NOT NULL,
  cost_minor  bigint, currency text,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE read_replicas (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  instance_member_id uuid REFERENCES instance_members(id),
  region_id    text REFERENCES regions(id),
  size         text NOT NULL,
  status       text NOT NULL,
  lag_bytes    bigint,
  created_at   timestamptz NOT NULL DEFAULT now()
);
```

**New usage metrics** (V2 §10.9): `api_requests`, `api_egress_gb`, `auth_mau`, `messages_sms`, `messages_whatsapp`, `storage_gb_hours`, `storage_egress_gb`, `image_transforms`, `realtime_connection_minutes`, `realtime_messages`, `replica_hours`.

**Operation kinds added:** `enable_services`, `disable_services`, `rotate_jwt_key`, `create_replica`, `delete_replica`, `detach_replica`, `migrate_supabase_users`, `migrate_supabase_storage`.

### 11.2 Project database (created when services are enabled)

```sql
CREATE SCHEMA pgd_auth AUTHORIZATION pgdock_admin;
  -- users, identities, sessions, refresh_tokens, mfa_factors, one_time_codes, audit_log
  -- view user_profiles; functions uid(), role(), claims(), claim(text)

CREATE SCHEMA pgd_storage AUTHORIZATION pgdock_admin;
  -- buckets, objects (RLS enabled; developer policies apply), multipart_uploads
  -- functions folder(path, n), filename(path), extension(path)

CREATE SCHEMA pgd_realtime AUTHORIZATION pgdock_admin;
  -- outbox, channel_access (RLS enabled; developer policies apply), broadcast_history
```

Grants: `anon`/`user` roles get `USAGE` on the helper functions and `SELECT/INSERT/UPDATE/DELETE` on `pgd_storage.objects` and `pgd_realtime.channel_access` subject to RLS; no access to auth tables except the `user_profiles` view (filtered to the caller). `service` reads `user_profiles` for all users. Schema migrations for these tables are versioned and applied by the control plane, like extensions.

---

## 12. Billing Integration

- Each new metric is rated by the V3 rating engine against plan inclusions and the org's price book.
- **SMS and WhatsApp** are priced as **provider cost plus margin**, with the provider cost recorded per message (`message_sends`), so naira repricing tracks provider rate changes. Prepaid credit is recommended for heavy OTP users.
- **MAU** counts distinct users who signed in or refreshed a token in the month, per project.
- The billing page gains a per-service breakdown and forecast; spend caps (V3 §3.10) throttle metered services (rate limits tighten, transforms pause, new realtime connections refused) rather than breaking sign-in or data access.
- Cost attribution (V3 §5.4) allocates edge node cost by request and connection share, object storage by GB, and SMS by actual cost.

---

## 13. Security and Testing

| Area | Approach |
| --- | --- |
| **Tenant isolation in the edge** | Every request resolves exactly one project from the hostname; the key must belong to that project; the JWT `aud` must match the ref; database connections use that project's role only. Fuzz tests send mismatched hosts, keys, and tokens and assert refusal. |
| **RLS correctness** | Test suite with sample apps (todo, marketplace, chat) asserting what `anon`, two users, and `service` can see through data, storage, and realtime. |
| **Query safety** | Parameterised SQL generation only; property-based tests generating random filters and selects against the query builder; cost-guard tests. |
| **Auth** | OWASP ASVS-aligned review; token rotation and reuse detection tests; OTP brute-force and SMS-pumping simulations; OAuth state/PKCE tests. |
| **Storage** | Path traversal, MIME spoofing, oversized uploads, signed URL tampering and expiry. |
| **Realtime** | No delivery of rows a subscriber can't read; delete events don't leak to new subscribers; rate-cap and resync behaviour. |
| **Load** | 1,000 projects with services enabled on one region; 2,000 req/s data API mix; 10k concurrent realtime connections; image transform bursts. |
| **External review** | A focused penetration test of the edge (auth, data API, storage) before general availability. |

---

## 14. Build Plan

Milestones continue from V3 (M17–M27). One engineer, roughly full time: about **24 weeks**. **Data API plus auth is usable by week 11**, so early adopters can start building before storage and realtime land.

### M28 — Edge foundation (Weeks 1–2)

`pgdock-edge` skeleton; per-region deployment; wildcard DNS and TLS; project refs; API keys; config cache and push channel; per-request transaction with role and claims; `pgd_auth` helper functions; enable/disable services operation; gateway rate limiting, CORS, request logs, and usage metering. **Done when:** a project with services enabled answers `GET /data/v1/health` at its own hostname, rejects keys from another project, and records request usage.

*(As built: the push channel is a signed long-poll feed with a full re-read every minute, and the edge never reads the metadata DB; the wildcard certificate comes from files an operator's DNS-01 client renews; user tokens are already verified. See `docs/decisions.md`, V4-M28, and `docs/backend-services.md`.)*

### M29 — Data API: reads (Weeks 3–5)

Schema introspection and DDL-driven refresh; select with embeds, filters, JSON paths, ordering, cursor pagination, counts; single-row and POST query; RLS gate; error mapping; cost guard; OpenAPI per project. **Done when:** the RLS test suite's read cases pass for `anon`, two users, and `service`, and a table without RLS returns `rls_required`.

*(As built: schema changes are detected by a catalog fingerprint checked inside requests, not an event trigger, which a restore by the project's owner couldn't recreate; materialized views need listing as public. See `docs/decisions.md`, V4-M29.)*

### M30 — Data API: writes, RPC, types (Weeks 6–7)

Insert, upsert, update/delete with required filters and `max_affected`, batch transactions, RPC (GET for stable functions), type generation for TS/Dart/Go, request explorer, security advisor (first version), RLS policy helper in the table editor. **Done when:** a sample todo app runs end to end with only the publishable key and generated types.

*(As built: public tables are never writable without row-level security; functions take named arguments only; the explorer is in Project → Settings → API with the advisor and type downloads. See `docs/decisions.md`, V4-M30.)*

### M31 — Auth core (Weeks 8–10)

`pgd_auth` schema; email/password, magic link, email OTP; ES256 keys, JWKS, rotation; access and refresh tokens with rotation and reuse detection; sessions; rate limits and lockout; platform email with templates; custom SMTP; admin user API; auth dashboard (users, detail, ban, delete). **Done when:** sign-up, sign-in, refresh, sign-out-everywhere, and reuse detection pass their tests, and RLS policies using `pgd_auth.uid()` enforce per-user data.

*(As built: pgdock-edge signs tokens with the active key it receives in the feed and sends auth emails through pgdock-server, which keeps SMTP credentials; rotation is a synchronous call, not an operation; a reused refresh token ends the whole session with no grace window. See `docs/decisions.md`, V4-M31.)*

### M32 — Auth: phone, OAuth, MFA, hooks (Weeks 11–13)

Message provider interface; platform SMS and WhatsApp OTP with caps, country allow-list, and metering; BYO providers; OAuth (Google, Apple, GitHub, Facebook, Microsoft) with PKCE; anonymous users and identity linking; MFA (TOTP, phone); custom-claims and before-sign-up Postgres hooks; webhook hooks; captcha. **Done when:** a Flutter sample app signs in with WhatsApp OTP and Google, a custom-claims hook adds `org_id` used by RLS, and an SMS-pumping simulation is stopped by caps.

*Early-adopter milestone: data API plus auth available to selected Pro customers from here.*

### M33 — Storage (Weeks 14–16)

`pgd_storage` schema and helpers; buckets; direct and multipart uploads with presigned URLs; downloads, signed URLs, public CDN; range requests; image transforms with caching; listing, move, copy, delete; reconciler; quotas and metering; storage dashboard. **Done when:** per-user avatar policies hold for upload, read, and delete; a 2 GB upload completes via multipart; transformed images are served from cache on the second request.

*(As built: files are in the region's backup storage target under version-keyed names, not `projects/<ref>/<bucket>/<path>`; transforms are pure Go (WebP and AVIF through WebAssembly) instead of libvips; signed URLs are checked by the edge, not the store; quotas reach the edge from a 5-minute sweep; branches carry file rows without their bytes. See `docs/decisions.md`, V4-M33, and `docs/backend-services.md`.)*

### M34 — Realtime (Weeks 17–19)

WebSocket server; database-change channels on the outbox with RLS group filtering; broadcast and presence; private channels via `channel_access` policies; heartbeats, reconnect, resync; multi-process fan-out; limits and metering. **Done when:** two users subscribed to the same table each receive only rows their policies allow, a rolled-back insert produces nothing, and 10k connections hold on one region in the load test.

*(As built: the outbox is read in transaction order by a per-project cursor over xid8 snapshots; edge processes relay broadcast and presence through NOTIFY on the project's own database, not the metadata DB; subscribers are grouped by role and all claims; the wire protocol is Supabase realtime's. See `docs/decisions.md`, V4-M34, and `docs/backend-services.md`.)*

### M35 — Read replicas (Week 20)

Replica creation on another node or region; pooler read-only route with health and lag rotation; data API replica routing; failover behaviour; detach; billing. **Done when:** read traffic shifts to replicas, a lagging replica leaves rotation automatically, and replicas follow a new primary after an HA failover.

### M36 — SDKs, docs, migration helper (Weeks 21–22)

`@pgdock/client`, Dart/Flutter, and Go SDKs with typed queries and auto-refresh; documentation site with guides (Next.js, React Native, Flutter); Supabase migration helper for schema/RLS rewrite, users (bcrypt upgrade), and storage; client code mapping guide. **Done when:** a real Supabase hobby project is migrated, including its users and files, and its users sign in on PGDock with their existing passwords.

### M37 — Billing integration, hardening, GA (Weeks 23–24)

Rating of all new metrics, billing page breakdown, spend-cap throttling; security review and external penetration test of the edge; load tests; failure injection (edge crash mid-upload, SMS provider outage, realtime node loss); runbooks. **General availability of backend services.**

### Timeline summary

| Weeks | Milestone | Outcome |
| --- | --- | --- |
| 1–2 | M28 Edge foundation | Per-project API hostnames and keys |
| 3–5 | M29 Data API reads | Safe reads from client apps |
| 6–7 | M30 Writes, RPC, types | Full CRUD with typed clients |
| 8–10 | M31 Auth core | Email auth, sessions, RLS by user |
| 11–13 | M32 Phone, OAuth, MFA, hooks | **Early adopters can build real apps** |
| 14–16 | M33 Storage | Files and images |
| 17–19 | M34 Realtime | Live updates, chat, presence |
| 20 | M35 Read replicas | Read scaling for dedicated projects |
| 21–22 | M36 SDKs, docs, migration | Supabase projects move across |
| 23–24 | M37 Billing, hardening | **GA** |

---

## 15. Roadmap after V4 (V5 candidates)

Edge functions (sandboxed runtime, deploy from CLI, secrets, logs); custom domains for project APIs; organisation SSO/SAML; Kotlin, Swift, and Python SDKs; logical-decoding realtime capture for high-volume dedicated tables; storage malware scanning; GraphQL; dollar billing and international expansion; Terraform provider; schema diff; branch data masking.

---

## 16. Risks & Mitigations

| Risk | Impact | Mitigation |
| --- | --- | --- |
| **Scope** — V4 is roughly the size of V1 and V2 combined | Delays | Early-adopter release after M32; realtime and storage can slip without blocking data API and auth. |
| **Cross-tenant leak through the edge** | Severe | One project per request from the hostname, key/`aud` checks, per-project DB roles, fuzzing, external pen test. |
| **Developers ship tables without RLS** | Customer data exposed | RLS gate by default, security advisor, policy helper, docs that start with RLS. |
| **SMS pumping fraud** | Large provider bills | Daily caps, country allow-list (Nigeria only by default), per-number limits, captcha, fraud alerts, prepaid credit for heavy senders. |
| **Realtime RLS checks are expensive** | Database load, latency | Claims-fingerprint grouping, batching, per-plan caps, coalescing with resync. |
| **Outbox growth** from realtime on busy tables | Disk and write amplification | Only enabled tables captured; rows deleted after delivery; caps; logical-decoding option for dedicated in V5. |
| **Own-API migration friction** | Slower adoption from Supabase | Migration helper for data, users, files; mapping guide; SDKs with familiar ergonomics. |
| **Email/SMS deliverability** | Users can't sign in | Custom SMTP nudged before production, multiple SMS providers with fallback, WhatsApp option, delivery logs. |
| **Image transforms as a CPU sink** | Edge overload | Size limits, quotas, caching, separate process pool for transforms. |

---

## 17. Open Questions

1. **SMS and WhatsApp providers.** Which providers should PGDock hold accounts with for Nigerian delivery (and as fallback)? This sets OTP cost and reliability. *Answered (M32): Termii for SMS (DND route) and Meta's WhatsApp Cloud API with an authentication template; projects may bring Termii, Africa's Talking or Twilio, or their own WhatsApp number.*
2. **Dart/Flutter at launch.** Is Flutter common enough among your target customers to justify a launch SDK, or start with TypeScript and Go only?
3. **Realtime in V4 or V5.** Realtime is the most expensive service to operate correctly. Keep it in V4 (as specced), or move it to V5 and ship V4 about 3 weeks sooner?
4. **Hostname.** Is `<ref>.api.pgdock.ng` the domain you want customers' apps to call, or will the product name change before V4 ships?
5. **Free plan and SMS.** Should Free projects get a small allowance of platform OTP messages (better onboarding, fraud risk), or BYO provider only (as specced)? *M32 keeps BYO only by default; an operator can allow Free projects with `PGDOCK_PHONE_AUTH_FREE`.*
6. **API caching.** Is edge caching of anonymous `GET` responses worth including in V4, or should it wait until customers ask?