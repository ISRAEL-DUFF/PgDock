# Changelog

PGDock follows semantic versioning; server, agent, UI, and the install
bundle share one version (spec §11.5).

## Unreleased

### Integrations (Taskiem's asks)
- Webhooks have a `description` and string `metadata` for the tool that
  made them; every change event carries `project_id` and, for tables with
  one, `primary_key`; `rotate-secret` takes `overlap_seconds` (up to a
  day) during which deliveries are signed with both secrets (two `v1=`
  values: verifiers must accept any match). New migration 00050.
- `Idempotency-Key` on the data API's writes, batches and function calls:
  a repeat within 24 hours gets the first answer
  (`Idempotent-Replayed: true`), a different request with the same key
  `422 idempotency_key_reused`. New project schema version 6, applied by
  the reconciler on upgrade.
- Management API calls that need a paused or archived Free project's
  database answer `503 project_resuming` / `project_restoring` with
  `Retry-After` and wake it (they answered `409 conflict`). Every `429`
  carries `Retry-After`.
- The OpenAPI file declares its security schemes (`bearerAuth`,
  `sessionCookie`).
- Docs: the error code catalogue with retryable codes (docs/errors.md),
  the deprecation policy, the webhook management contract, building an
  integration (docs/integrations), and signed sample deliveries.

### V4.1 (on feature/pgdock4)
- The DPA template lists the SMS and WhatsApp providers used for apps'
  sign-in codes (Termii, Africa's Talking, Meta) and Cloudflare's CDN as
  sub-processors. Existing installs: see docs/legal.md before publishing
  it as a new version.
- Docs: the Lagos launch checklist uses the region's own etcd cluster
  (V3.1); the status page's limits are up to date.
- Per-plan limits for backend services (V4.1-M2): plans set monthly data
  API requests and monthly active users (Personal: 500,000 and 10,000),
  past which requests get `429 plan_limit_reached` and new users `429
  mau_limit_reached` until the month ends (sign-in keeps working), and
  ceilings on a project's statement timeout, rate limits and daily SMS
  codes. Owners are emailed at 80% and 100%. The API page and `pgdock
  services status` show what is in effect. On upgrading installs the
  limits start with the first full month. New migration 00044.
- Fixed: the data API couldn't call functions in a schema with no tables.
- Billing add-ons (V4.1-M3): 14- and 30-day point-in-time recovery for
  dedicated projects and extended (30 daily, 12 weekly) or long (30
  daily, 52 weekly) backup retention, on Pro and Team, billed by the hour
  (Backups page, `pgdock pitr window`, `pgdock backup retention`); a price
  book can set a per-region premium on dedicated, HA and read replica
  lines. New migration 00045. Price books published before this have no
  add-on prices until a new one is published.
- Fixed: a price book saved through the API dropped its message margin.
- Resizing dedicated projects (V4.1-M4): a new size or a bigger disk from
  Project Settings → Size and disk, `pgdock instance resize|disk` or
  `PATCH /projects/{id}/instance`, with a cost estimate and a dry run. The
  instance restarts in a few seconds under the poolers' pause, or with HA
  switches over to a resized standby; a size its node can't hold moves it
  to one that can. Dedicated placement now checks a node has room.
- Dedicated hosts on demand: with no room in the region and a provider
  that creates servers, a dedicated create provisions a host within the
  infrastructure budget and waits for it; over the budget it is refused
  with `409 capacity_pending_approval` while the proposal waits.
- Postgres version lifecycle (V4.1-M5): Admin → Platform → Postgres
  versions sets each major to preview, supported, deprecated (with a
  retirement date at least 180 days out) or retired. Deprecation emails
  the owners and admins of affected organisations, with reminders at 90,
  30 and 7 days, and puts a banner on their projects; retired majors take
  no new projects, while existing ones keep running. Preview majors can be
  chosen on the create form. The terms template has a Postgres versions
  section. New migration 00046.
- The major upgrade preflight now checks dedicated projects too, restoring
  the schema into a temporary instance of the new major, and warns about
  features the new major removed.
- Status page (V4.1-M6): paying organisations' owners and billing
  contacts are subscribed to the incidents on the components and regions
  their projects use, synced hourly (a per-contact *Status page emails*
  toggle; an unsubscribe is kept). New **billing** and per-region
  **backend services** components; pgdock-edge now reports at least every
  30 seconds. Upgrade pgdock-status with pgdock-server and add the new
  components to status.toml (see status.example.toml). New migration
  00047.
- Proposed maintenance (V4.1-M7): when an HA project's minor upgrade waits
  for an announced window, PGDock drafts the announcement for the first
  window at least 96 hours away; confirm it (it shows the exact email,
  and how many it reaches) or discard it under Admin → Incidents →
  Scheduled maintenance. The schedule form previews its email too. New
  migration 00048.
- Region page: a region's pooler pair and failure domains, its etcd
  cluster, and its HA projects still on another region's etcd, with
  **Move all**, which moves them one at a time.
- With automatic rebalancing on, rebalance moves run only inside the
  maintenance window.
- Banners: every member of an organisation sees an overdue, restricted or
  suspended billing state or a declined payment (owners and billing
  members with the pay link, no amounts), owners and billing members see
  the budget reaching 80% and 100%, and everyone sees open incidents
  affecting the organisation's projects.
- API page (V4.1-M8): a **Quick start** with the project's URL and
  publishable key in TypeScript, Dart and Go; **API docs** for each
  exposed table, view and function (columns, row-level security, policies,
  who may do what, and examples in curl, TypeScript, Dart and Go, with
  **Try it** in the request explorer); and **Usage this month** against
  the plan, with the project's share of the month's charges for owners and
  billing members. New `GET /projects/{id}/services/catalog` and
  `/services/usage`.
- CLI: `pgdock policies list` and `pgdock policies lint` (exit 1 on a
  danger finding, for CI); `pgdock logs api` with `--status`, `--path` and
  `--follow`.
- Branches of a project with backend services get their own API: a new
  URL, keys and signing key, the parent's settings without its auth
  secrets, and the users the copy brought, signed out. The create
  response, the credentials panel, the .env download and
  `pgdock branch create --env` give `PGDOCK_API_URL`,
  `PGDOCK_PUBLISHABLE_KEY` and `PGDOCK_SECRET_KEY`; `copy_files` (or
  `--copy-files`) copies the parent's stored files in the background.
- Edge caching of anonymous reads (V4.1-M9): list tables and stable
  functions with a TTL (Settings → API → **Cache anonymous reads**, or
  `cache_ttl_seconds`, at most 3,600 seconds) and publishable-key GETs
  without a user's token are served from each edge's cache (`X-Cache`,
  `Age`, `Cache-Control: public`). Writes through the API drop what they
  change at once; other changes show within the TTL. Cached answers are
  still metered. Upgrade pgdock-edge with pgdock-server;
  `PGDOCK_EDGE_CACHE_MB` sizes the cache (64 by default).
- Cost attribution for backend services (V4.1-M10): new cost categories
  **edge** (edge nodes, and a configurable share of shared nodes, split by
  each organisation's requests and realtime minutes), **files** and
  **messages** (the provider's charge for each platform-sent code), and a
  **Margin by service** table on Platform → Costs & margins. Nodes can
  have the role `edge`; edges report their CPU, and an hour above 70% in
  a region proposes an edge node, which runs pgdock-edge only
  (`PGDOCK_CLOUD_EDGE_IMAGE`). Upgrade pgdock-edge with pgdock-server. New
  migration 00049.
- Tests and hardening (V4.1-M11): image transforms run in separate
  `pgdock-edge render-worker` processes, so a bad image costs one request
  (`500 transform_failed`) rather than the edge
  (`PGDOCK_EDGE_RENDER_MEMORY_MB`, 512 by default). Fixed: a sign-in code
  used up by five wrong tries no longer lets a new one be sent at once;
  changing a password now signs out the user's other sessions. Project
  auth was reviewed against ASVS 4.0 level 2 (docs/security-review.md),
  and new tests run three sample apps' RLS policies through the data API,
  storage and realtime. Upgrade pgdock-edge with pgdock-server.
- Launch gates (V4.1-M12): `TestBackendLoad` can run against a real
  install (`PGDOCK_LOAD_TARGET`), with a six-server rig in
  `deploy/loadtest/` for the 1,000-project, 2,000 requests a second gate;
  docs/backend-runbook.md lists every GA gate with its dated status.
  Fixed: the compose install now passes `PGDOCK_API_DOMAIN`,
  `PGDOCK_EDGE_SECRET` and `PGDOCK_CLOUD_EDGE_IMAGE` from `.env` to
  pgdock-server; set the first two there to serve edges.

### V4 (on feature/pgdock4)
- Backend services' edge foundation (V4-M28): a project can turn on backend
  services (Project → Settings → API, `pgdock services enable`) and gets an
  API hostname, `https://<ref>.<domain>`, with a publishable key for apps
  and a secret key for servers. `pgdock-edge`, a new binary run in each
  region, serves them: it checks the key (and a signed-in user's ES256
  token) against that project only, applies the allowed origins and rate
  limits, and runs each request in one transaction through the pooler as
  the project's anon, user or service role. Requests are metered
  (`api_requests`, `api_egress_gb`) and logged for 7 days.
  `GET /data/v1/health` is the first endpoint; the data API, auth, storage
  and realtime follow. See docs/backend-services.md.
- New migration 00036 (backend services).
- Data API reads (V4-M29): `GET https://<ref>.<domain>/data/v1/<table>`
  with `select` (columns, JSON paths, related rows through foreign keys),
  `where`/`or` filters, ordering, cursor pages and counts; one row by key;
  `POST …/query` for nested filters; a per-project OpenAPI document. Tables
  are readable with the publishable key only with row-level security (or
  listed as public), so a forgotten policy fails closed. Schema changes are
  picked up within seconds. Exposed schemas and public tables are in
  Project → Settings → API. Backups, restores and promotions of projects
  with backend services keep their `pgd_*` schemas and grants.
- Data API writes, functions and types (V4-M30): insert and upsert
  (`POST /data/v1/<table>`, `on_conflict`), update and delete with a
  required filter and `max_affected`, all-or-nothing batches
  (`POST /data/v1/batch`), and function calls (`/data/v1/rpc/<fn>`, GET for
  stable functions). Typed clients for TypeScript, Dart and Go
  (`pgdock gen types`, or a download on the API page), a security advisor,
  a request explorer that runs as anon, a user or the service role, and a
  row-level security policy helper in the Table Editor.
- Auth core (V4-M31): users in the project's own database (`pgd_auth`),
  signing up and in by email and password, confirmation by link or code,
  magic links and email codes, password recovery and email change; ES256
  access tokens verified with the project's JWKS, refresh tokens that
  rotate on every use and end the session when reused, sign-out of one
  session, the others or all; lockout and per-address limits; an admin API
  with the secret key. Project → Authentication lists, invites, bans,
  signs out and deletes users, and sets the sign-in rules, email templates,
  the project's own SMTP server and signing-key rotation; `pgdock auth`.
  Monthly active users are recorded (`auth_mau`).
- New migration 00037 (auth).
- Auth: phone, OAuth, MFA, hooks (V4-M32): sign-in with SMS and WhatsApp
  codes (Termii and PGDock's WhatsApp number by default, or the project's
  own Termii, Africa's Talking, Twilio or WhatsApp Cloud API account), phone
  and password sign-up, phone changes; SMS-pumping limits (allowed
  countries, 5 codes an hour per number, a daily cap with alerts) and
  per-message metering with spend; OAuth with Google, Apple, GitHub,
  Facebook and Microsoft (PKCE), linking by verified email and identity
  linking; anonymous users; TOTP and phone second factors with an MFA
  policy; custom-claims and before-sign-up Postgres hooks (as the new
  `<db>_auth_hook` role), after-sign-up/in and send-message webhooks;
  Turnstile captcha. Project → Authentication gains Phone, Providers, MFA
  and captcha, and Hooks; `pgdock auth config|set|hooks`. A Flutter sample
  is in docs/examples/flutter-auth.
- New migration 00038 (auth messages, hooks and alerts); project schema
  version 3.
- Security: backend services' request roles are now logins of their own
  and pgdock-edge connects as them directly, so database code that runs
  `RESET ROLE` (an RPC function, an auth hook, or SQL injection in dynamic
  SQL) can no longer reach the edge's login, the auth tables or the service
  role. New migration 00039.
- Storage (V4-M33): buckets (public or private, size limits, allowed
  types) and files in the region's object store, with access decided by
  row-level security on `pgd_storage.objects` and helpers such as
  `pgd_storage.folder(path, 1)`; uploads up to 50 MB through the edge and
  up to the plan's limit (5 GB on Pro and Team) by presigned multipart
  URLs, resumable for 24 hours; downloads with ranges, public URLs and
  signed URLs; listing, move, copy and delete; MIME sniffing; image
  resizing and conversion to WebP, AVIF, JPEG and PNG, cached in the store;
  file storage, download and transform quotas and metering
  (`storage_gb_hours`, `storage_egress_gb`, `image_transforms`); a nightly
  reconciler; a CDN purge when a bucket goes private (Cloudflare). Project →
  Storage and `pgdock storage buckets|ls|cp|rm|sign`.
- New migration 00040 (storage); project schema version 4.
- Realtime (V4-M34): one WebSocket per client
  (`/realtime/v1/websocket`, the protocol Supabase's realtime clients
  speak) with database changes on tables with realtime on, each subscriber
  getting only the rows its policies let it read (deletes only for rows it
  was sent); rolled-back changes never delivered; filters; broadcast and
  presence across edge processes; private channels decided by policies on
  `pgd_realtime.channel_access`; broadcast history for chosen topics; an
  HTTP broadcast for servers; heartbeats, resync signals, connection and
  message limits, and metering (`realtime_messages`,
  `realtime_connection_minutes`). Project → Realtime and
  `pgdock realtime status|enable|disable|history`.
- New migration 00041 (realtime limits); project schema version 5.
- Read replicas (V4-M35): up to two per dedicated project, on other nodes
  (another region if wanted; data-residency projects stay in theirs).
  Each is a Patroni member that is never promoted and never synchronous,
  so it follows a new primary after a failover. The pooler's read-only
  route `<db>_ro` balances reads across the replicas within 10 seconds of
  the primary, and falls back to the primary, read-only, when none is.
  Data API GETs go to replicas with `Read-Replica: allowed`, or by default
  for publishable-key reads when the project turns that on. A replica can
  be detached into a standalone project. Replicas bill like dedicated
  instances of the primary's size (`replica_hours` and resource metrics).
  Project → Settings → Read replicas, `pgdock replicas list|create|delete|detach`.
  See docs/read-replicas.md.
- New migration 00042 (read replicas). HA standby billing no longer counts
  removed members.
- Supabase migration helper (V4-M36), run after importing a Supabase
  database into a project with backend services:
  - **Policies:** imported policies and column defaults are rewritten to
    the project's request roles and `pgd_auth` helpers.
  - **Users:** users and identities are copied with their ids. Supabase's
    bcrypt password hashes keep working and become argon2id at each user's
    next sign-in. OAuth identities keep their provider ids.
  - **Files:** buckets and files are copied over Supabase's S3 protocol,
    and storage policies are recreated on `pgd_storage.objects`.
  - **Reports:** each step lists what it couldn't carry over, and can run
    again.
  - Run it from Project → API → Migrate from Supabase,
    `pgdock migrate supabase`, or `POST /api/v1/projects/{id}/migrate/supabase`.
    See docs/migrate-from-supabase.md and the supabase-js mapping
    (docs/supabase-client-mapping.md).
- Password sign-in accepts bcrypt hashes and re-hashes them with argon2id
  on success (`password_rehashed` in the auth log).
- SDKs: `@pgdock/client` for TypeScript (sdk/js), Dart and Flutter
  (sdk/dart, `pgdock` on pub.dev), and Go (sdk/go). Each covers data, auth,
  storage and realtime, with:
  - typed rows from `pgdock gen types`;
  - token refresh before expiry, one refresh at a time;
  - consistent errors;
  - live queries that refetch on changes, resyncs and reconnects.

  `TestSDKs` runs each one against a real edge.
- A documentation site: `make docs-site` renders docs/ with navigation,
  search and a link check. It adds guides for Next.js, React Native and
  Flutter, and SDK references.
- Built with Go 1.26.9 (1.25 is out of support; html/template and net/http
  fixes are only in 1.26), and linted with golangci-lint 2.14. ES256 keys
  are encoded and parsed through `ecdsa.PublicKey.Bytes` and
  `ParseUncompressedPublicKey` instead of the deprecated coordinates.
- The Usage page lists file storage, downloads and image transforms (they
  were recorded but not shown).
- pgdock-server's metadata connection pool defaults to 16 connections
  (it was pgx's 4 on a small box) unless `PGDOCK_DATABASE_URL` sets
  `pool_max_conns`; the auth message and hook senders no longer hold a
  connection while they send.
- Backend services billing, hardening and GA readiness (V4-M37):
  - **Rating:** invoices charge each plan's backend-services usage above
    its allowance: data API requests and transfer, monthly active users,
    file storage (GB-hours) and downloads, image transforms, realtime
    connection-minutes and messages. SMS and WhatsApp codes are charged at
    the provider's cost plus the price book's margin (20% by default).
    Published price books without these lines charge nothing for them.
  - **Billing page:** every invoice and forecast line names its service,
    and **This month so far** breaks the month down by service, so far
    and projected.
  - **Spend caps** slow backend services instead of stopping them: a
    quarter of the data and storage rate limits, new image transforms
    paused, new realtime connections refused; sign-in, token refresh and
    open connections carry on. New migration 00043.
  - **SMS failover:** with Africa's Talking configured
    (`PGDOCK_AFRICASTALKING_*`), platform SMS codes go by Termii and, when
    it fails, by Africa's Talking; a failed provider is tried last for a
    minute.
  - **Security review of the edge** with fuzz tests of its isolation
    (hosts, keys, tokens), query SQL safety and signed URLs
    (docs/security-review.md), and the scope of the external penetration
    test (docs/pentest-scope.md).
  - **Load and failure tests:** `TestBackendLoad` (projects with backend
    services, a data API rate, image transform bursts; docs/load-test.md)
    and failure injection: an edge killed mid-upload, an SMS provider
    outage, a realtime process lost.
  - **Runbook** for backend services (docs/backend-runbook.md).

### V3.1 (on feature/pgdock3)
- Failure domains (V3.1-M1): each node can record what fails with it (a
  rack, host or power feed); servers PGDock creates on Hetzner go into a
  per-region spread placement group. An HA project's standby, the etcd
  members and a region's pooler hosts are kept in different domains:
  enabling HA and setting up etcd refuse otherwise, a second pooler host
  in the same rack is warned about, and a `failure_domain` alert reports
  existing groups that share one. Platform → Nodes. See
  docs/failure-domains.md.
- New migration 00033 (failure domains).
- etcd per region (V3.1-M2): each region gets its own etcd cluster, so a
  region's HA failover depends only on that region. Platform → Nodes → etcd
  cluster for HA has a region picker and **Replace…** on each member: a
  dead or live member moves to another node in the region while the
  cluster keeps its quorum, and draining a node moves its member
  automatically. An HA project whose state is in another region's
  cluster gets **Move to the region's etcd** on its HA card (one restart,
  writes paused a few seconds). See docs/ha.md.
- New migration 00034 (etcd per region).
- Announced maintenance (V3.1-M3): Admin → Incidents → **Schedule
  maintenance** announces a window for a region or some projects. It
  shows on the status page as upcoming, is emailed to the owners and
  admins of the organisations it covers, and resolves by itself. HA
  projects' availability minutes inside a window announced at least 72
  hours ahead are excluded from the SLA, listed per announcement on the
  HA card. The weekly minor-upgrade sweep waits for an announced window
  before switching over an HA project
  (`PGDOCK_MAINTENANCE_REQUIRE_ANNOUNCEMENT`, on by default).
- New migration 00035 (announced maintenance).

### V3 (in progress, on feature/pgdock3)
- Standby edge pooler: two pooler hosts behind a floating IP with
  keepalived. pgdock-server pushes the PgBouncer configuration to both and
  moves the floating IP itself if keepalived doesn't. Admin → Nodes shows
  both hosts and recent pooler events. See docs/edge-poolers.md.
- `pgdock-status`, a status page to run on separate infrastructure. It
  probes PGDock from outside and takes signed heartbeats for the rest. It
  opens and resolves incidents by itself, keeps 90 days of history, and
  emails subscribers. Admin → Incidents posts incidents to it. See
  docs/status-page.md.
- New migrations 00020 and 00021 (pooler hosts, incidents).
- Moves by logical replication: promotion, demotion, node moves (platform
  admins: Project Settings → Compute → Moves, `pgdock move`) and major
  upgrades copy the data while the project serves, and pause writes only
  for the switch (about 0.1 s in the tests, at 1 and 3 GB). They fall back
  to dump and restore when they must, and say why. Schema changes are
  refused during a move. See docs/moves.md.
- Postgres versions: `PGDOCK_PG_VERSIONS` (default 17 and 18), a version
  picker when creating a project or shared cluster, and major upgrades
  with a preflight that test-restores the schema on the new version
  (Project Settings → Compute → Postgres version, `pgdock upgrade`).
- Minor releases are applied automatically in a weekly maintenance window
  (Admin → Nodes; default Sunday 02:00–06:00 UTC), one instance at a time
  with the poolers holding clients.
- HA for dedicated projects: a standby on another node under Patroni,
  with a three-member etcd cluster. The pooler route follows the leader,
  so writes are back through the same URL about 20 seconds after the
  primary's node dies. Planned switchovers, optional synchronous
  replication, failover history, and the month's availability measured
  from pgdock-server and the status page (the SLA's two vantage points).
  Project Settings → Compute → High availability, `pgdock ha`, Admin →
  Nodes → etcd. See docs/ha.md.
- Billing core: price books (versioned prices, repricing at least 30
  days ahead, grandfathering, a preview of each org's invoice), Free, Pro
  and Team plans with prorated plan changes and annual terms, monthly
  invoices (usage in arrears, the plan fee in advance, VAT, expected WHT)
  numbered `PGD-YYYY-NNNNNN` and downloadable as PDF, credit notes, a
  double-entry ledger the database keeps balanced and append-only, the
  month's forecast, budget alerts, a spend cap that pauses new billable
  resources, and cost estimates before promoting or enabling HA. A new
  `billing` org role for finance staff. Org → Billing, Admin → Billing,
  `pgdock billing`. Invoices aren't issued automatically until an admin
  turns it on. See docs/billing.md.
- Payments: cards through Flutterwave (saved and charged when an
  invoice is issued), bank transfers into each organisation's own virtual
  account (iSpend, falling back to Flutterwave), Pay with iSpend and
  wallet mandates, optional USDT top-ups, and manual payments with proof.
  Webhooks are verified with the provider before anything is posted, and
  re-queried hourly and nightly. Receipts, refunds from credit, prepaid
  balances with daily deduction, alerts and auto top-up, WHT matching and
  credit-note uploads, a dunning ladder (overdue, restricted, suspended,
  deletion only if enabled), and nightly reconciliation with each
  provider. Org → Billing, Admin → Billing → Payments / WHT / Events /
  Reconciliation, `pgdock billing pay|transfer|payments`. Admin →
  Organisations → an org → Billing sets its mode (postpaid or prepaid),
  payment terms and price book. Refunds can reopen the invoices a payment
  settled. See docs/payments.md.
- The Free tier: a Free project idle for 7 days (no client connections
  through the poolers) is paused after a day's warning, and one paused for
  90 days is archived to a verified backup. The next connection wakes it:
  the client gets a message saying it is resuming (or being restored),
  and a retry gets in. Resume from the dashboard, `pgdock resume`, or the
  API. Archived projects are deleted after a year, with 30 and 7 days'
  notice. Paid plans are never paused. See docs/free-tier.md.
- Open signup: Cloudflare Turnstile on the signup page and a cap on
  accounts per IP address a day. A Pricing page in the dashboard, and
  public prices at `GET /api/v1/pricing`.
- Support:
  - Tickets from the dashboard (Organisation → Support), email (an
    inbound webhook, threaded by reference and headers) and WhatsApp
    (Pro and Team, from registered numbers), all in one console
    (Platform → Support).
  - Each ticket shows the organisation's plan, billing standing,
    projects, recent operations and incidents beside it.
  - Response targets in Nigerian business hours. Replies go back by the
    same channel; staff can add internal notes.
  - A new `support` platform role sees the console and nothing else. See
    docs/support.md.
- Revenue dashboard (Platform → Revenue):
  - MRR and ARR, with new, expansion, contraction and churn.
  - Paying organisations, ARPA, and conversions from Free.
  - Metered revenue, invoiced and collected amounts, receivables by age,
    and outstanding WHT.
  - A CSV for the accountant.
- Legal documents:
  - An acceptable use policy accepted with the terms.
  - A versioned SLA and DPA, and per-organisation order forms, accepted
    by an owner (Organisation → Legal, or with a plan change) and listed
    with their acceptances for the platform admin. See docs/legal.md.
- New migration 00029 (tickets, support phones, legal documents and
  acceptances, MRR snapshots).
- Capacity automation:
  - Hetzner Cloud servers created by PGDock, joining by themselves
    through cloud-init and a one-time token.
  - Proposals when a region's shared disk is projected past 70% within 14
    days, or no dedicated host fits the largest size. They are
    provisioned within a monthly budget, and wait for approval above it.
  - Drains with zero-downtime moves, weekly rebalancing, and deletion of
    servers that stay empty for a day.
  - Platform → Capacity. See docs/capacity.md.
- Cost attribution and margins:
  - Each day's node, storage, transfer, floating IP and overhead costs,
    divided among organisations.
  - Margin by plan and organisation, the cost of the Free tier, and unit
    costs for repricing.
  - An FX view with recorded naira rates and the erosion since costs were
    incurred, and a CSV for the accountant.
  - Platform → Costs & margins.
- New migration 00030 (node provider, region, cost and lifecycle;
  capacity proposals; drain and rebalance moves; exchange rates; daily
  cost allocations).
- Regions:
  - Projects choose a region at creation (New project → Region,
    `pgdock projects create --region`, `pgdock regions`). Each region
    has its own pooler hosts and hostname, backup target and copy target.
  - Moving a project to another region is a zero-downtime move; the old
    hostname keeps working for 30 days.
  - Data residency (owners, with step-up): backups only in the region, no
    cross-region copies, no moves out, branches stay. Exports stay
    available.
  - Backups on platform targets are copied to a second region, verified
    by checksum, and the weekly restore test alternates between primary
    and copy.
  - Platform → Regions. See docs/regions.md.
- New migration 00031 (regions, project region and residency, per-region
  pooler generations, backup copies).
- Query insights (Pro, Team and dedicated; Project → Query insights,
  `pgdock insights`):
  - Top queries over an hour to 30 days, each with its latency and calls
    over time and its EXPLAIN as a plan tree.
  - A slow-query log.
  - Index suggestions with their `CREATE INDEX CONCURRENTLY`, a hypopg
    estimate where it is enabled, and save as migration.
  - Unused and duplicate indexes, table bloat with reclaim space, and
    blocking chains.
  - See docs/query-insights.md.
- New migration 00032 (query statistics). The Postgres image includes
  hypopg.
- Hardening for the Lagos launch (M27):
  - A billing audit: the ledger check now ties every invoice, credit note
    and payment to its ledger entries and the receivable to what is owed
    (docs/billing-audit.md, with questions for the accountant).
  - Fixed: a credit note on a partly paid invoice made the receivable
    negative, and a fully credited invoice stayed open for dunning.
  - Fixed: a signed but forged provider event could redirect another
    organisation's transfer. Refunds, manual payments, credit notes,
    attributing events, publishing price books and changing an org's
    billing terms now ask admins to confirm with a password or code.
  - Payment provider outages: a charge that hits one is tried again when
    the provider is back, a card retry isn't used up by one, and neither
    counts as a decline or emails the customer.
  - Fixed: with pooler hosts in two regions, the split-brain alert fired
    for a MASTER in each. It is now per region.
  - The waker restarts if it stops; a `waker_down` alert fires while it is
    unreachable, and Free projects aren't paused or archived meanwhile.
  - HA: Patroni's failsafe mode is on, so losing etcd's quorum doesn't
    stop writes; existing clusters get it from pgdock-server. A
    switchover with synchronous replication waits for the standby to be
    synchronous instead of failing.
  - Chaos tests (provider outage, pooler split brain, waker failure, etcd
    member and quorum loss), a rating load test at 1,000 organisations,
    and the Lagos launch gate (docs/lagos-launch.md).
- Fixed: base backups of HA projects failed (WAL-G connected as
  `postgres`).
- Fixed: recreating a Postgres 17 instance lost its data (V3 only; V2 ran
  18), and recreating an agent-run shared cluster failed.
- New migrations 00022 and 00023 (moves, Postgres releases), 00026
  (billing), 00027 (payments), 00028 (the Free tier). Upgrade notes:
  docs/upgrade.md#upgrading-to-v3.

- Fixed: the first-run setup code printed by `install.sh` was missing its
  trailing `=`, so the wizard called it wrong. The installer now prints the
  whole code, new codes have no padding, and the server ignores spaces,
  `=` padding and letter case when comparing. (The code still changes every
  time the server restarts, unless `PGDOCK_SETUP_CODE` is set in `.env`.)
- `deploy/compose/preflight.sh <ui-host> <db-host>`: a read-only check to run
  on the server before `install.sh` (OS, resources, Docker, DNS, ports,
  outbound reach, optionally the backup bucket and mail server).
  See docs/install.md.
- docs/install.md: how to get Cloudflare R2 credentials for the backup
  storage step, what to do when the wizard says the setup code is wrong, and
  memory settings and swap for a 4 GB server.

## v2.1.0

UI fixes found by testing the redesign, a SQL filter bar for the Table
Editor, and a way to add and recover platform admins. No database
migrations: upgrade from v2.0.0 with `git checkout v2.1.0` and
`./install.sh` ([upgrades](docs/upgrade.md)).

### Platform admins
- A platform admin can make another account a platform admin, or an
  ordinary user again (Admin → Users → Make admin / Remove admin), with a
  fresh password and code. The account must be active, approved, verified
  and have two-factor set up; its sessions end and it is emailed; the last
  active admin can't be removed. Setup still makes exactly one admin, so
  add a second (docs/install.md, docs/admin-runbook.md).
- Recovery when no admin can sign in: `pgdock-server admin list | promote |
  demote | reset-2fa | reset-password <email>`, run on the server. Changes
  are audited as done on the server. See docs/operations.md.

### SQL filter bar
- The Table Editor has a filter bar above the grid: type a condition as
  you would after `WHERE` (`email = 'a@b.com' or phone like '081%'`) and
  press Enter. Suggestions (Ctrl+Space, or as you type) list the table's
  columns with their type and nullability, then operators and functions.
  Recent filters are remembered per table, errors show the database's own
  message at the character that caused it, and the grid keeps its last
  good view. It combines with the Filter popover, and the count and export
  follow it.
- Safety: the condition is checked (one expression; nothing that can end
  a `WHERE` clause, such as `;`, `UNION`, `ORDER BY`, `LIMIT`) and runs as
  the project's read-only role in a read-only transaction under the
  statement timeout, so it can read what the console can read and write
  nothing. The API takes it as the `where` parameter of the rows, count
  and export endpoints.

### UI fixes from QA
- Text fields show focus with a thin accent border and a soft glow, not the
  heavy 2px outline buttons and links get.
- Phones: the top bar's switchers no longer overlap (names and badges
  show from the `sm` breakpoint up), and the Table Editor and SQL Editor
  show their table or query list and the editor in turns, with a
  Tables/Queries button, instead of squeezing the grid. The grid's footer
  fits.
- Column default values: a bare word on a text column (`free`) now shows
  how to quote it (`'free'`) before the database refuses it.
- SQL Editor completion picks up tables and columns created by the script
  you just ran, without a reload. The shortcuts sheet names Ctrl+Space
  (⌘Space is Spotlight on macOS).
- The grid's row checkboxes are 14px (they were the browser's 20px, nearly
  as tall as the row) and follow the theme, with a tick and a dash for
  partial selection.
- A branch or project that fails no longer shows a "save the password"
  card, and the message says when cleanup was incomplete.
- Row save errors in a side panel show once, inline, rather than also as
  a toast over the Save button.
- The schema change preview wraps long statements; the Plans table
  names its units; the new project hint describes opaque names; the 404
  page for signed-in users no longer uses the sign-in layout.

## v2.0.0

PGDock V2: users and organisations, quotas and usage, API tokens and the
CLI, visual table editing, your own backup storage, database branching,
demotion, webhooks and scheduled jobs, and a redesigned UI.

**Upgrading from v1.0.0:** read [docs/upgrade.md](docs/upgrade.md) first,
and upgrade agents before the server.

### A redesigned UI
- The whole UI follows Supabase Studio's layout: a top bar with
  organisation, project and branch switchers, an icon rail with section
  menus, and dark and light themes (dark by default).
- **Table Editor:** a spreadsheet grid with filters, multi-column sort,
  immediate cell edits (conflicts reported), rows and columns edited in
  side panels, CSV/JSON/SQL export, and every schema change shown as SQL
  before it runs. Columns can be created with UNIQUE, CHECK and foreign
  keys; tables can be duplicated and commented.
- **SQL Editor:** a code editor with schema-aware completion and
  formatting, saved queries (private or shared with the project, with
  favourites), history and templates.
- Project pages: Database (branches, backups, webhooks, jobs, extensions,
  migrations), Reports, Logs, and settings split into General, Database,
  Compute and tier, and Backup storage.
- Organisation, account and admin pages in the same style; projects shown
  as cards with their branches.
- Keyboard shortcuts (press `?`), loading skeletons, a navigation drawer
  on phones, and colours that meet WCAG AA in both themes.

### Fixes from QA
- Request bodies with unknown fields are refused (`400`) instead of
  ignored; a misspelled field could clear an organisation's outbound
  allow-list.
- `GET /me` with an API token returns the token's real `expires_at`.
- Approving or rejecting a dedicated request, and demoting, accept an
  empty body (every field is optional).
- A project-restricted API token can no longer list organisation members.
- Signing out forgets the organisation remembered in the browser.
- S3-compatible stores that are unreachable or stall now fail within
  seconds (connect, TLS and reply timeouts) instead of holding an
  operation, and its slot in the operations-in-flight quota, for 30 minutes.
- Starting a stopped dedicated instance refreshes its recorded published
  port (dev mode, where Docker picks a new one).
- Switching to opaque credentials: the UI and docs now say members'
  personal logins are renamed at once. Per-project plan defaults are
  512 MB (Personal) and 8 GB (Team); the spec now says so.
- Demoting a project whose instance is down returns `503` with a reason,
  not a bare `500`.
- Upgrade: shared projects created before V2 get `temp_file_limit` (a
  migration queues `apply_settings` for each); `install.sh` recreates the
  poolers so they read the new configuration; an agent on an older minor
  version than the server is refused work (upgrade agents first).

### Hardening (V2 M16)
- **Security:** scheduled SQL jobs no longer run on a superuser
  connection, and copying a tenant's data (branches, restores, imports,
  the restore test, promotion, demotion) no longer runs the tenant's
  functions as the superuser. NAT64 and 6to4 addresses can't reach
  internal or metadata addresses. Upgrade before inviting anyone else.
- Organisation owners can download any backup as a `pg_dump` file
  (Backups → Download, `pgdock backup download`).
- Background work survives being stopped half-way: storage locks, the
  reaper, webhook delivery, the scheduler (runs now end with a stopping
  server), a member removed mid-session, an org suspended mid-backup.
- Faster webhook delivery: kept-alive connections, one destination check
  per batch, batched outbound counters.
- A V2 load check (300 projects on two nodes, webhooks, jobs, branches),
  a user guide, a platform admin runbook, an incident process and a terms
  template.

### Webhooks and scheduled jobs (V2 M15)
- Database webhooks: inserts, updates and deletes of chosen tables POSTed
  to a URL, recorded in the same transaction (a rolled-back change never
  sends anything), in commit order per webhook, signed with HMAC-SHA256,
  retried with backoff for 24 hours, then kept as dead letters you can
  replay. Column filters for updates, static headers, test events, a
  7-day delivery log, auto-pause after 50 failures, and broken-trigger
  detection.
- Scheduled jobs: SQL as the project owner, or a signed HTTP call, on a
  cron schedule in your time zone, with timeouts, skip-or-queue overlap,
  history, run now, and an email after three failures in a row.
- Outbound safety: requests only to public addresses (resolved, checked,
  connected to the checked address, no redirects); the platform admin can
  allow-list internal hosts per organisation, turn its outbound traffic
  off, and see its requests by host. Per-organisation rate limits queue
  webhook deliveries rather than dropping them.
- `pgdock webhooks …` and `pgdock jobs …`; Project → Webhooks and Jobs.

### Demotion (V2 M14)
- Move a dedicated project back to the shared tier with its URL, app
  password and every member's personal login unchanged, after a write
  freeze while the data is copied and verified. A failure before the
  switch leaves it on its dedicated instance.
- A preflight checklist (also `pgdock demote --check`): size against the
  organisation's shared storage limits, extensions, custom roles, peak
  connections, database settings that reset, and a shared cluster with
  room, the organisation's own when it has one.
- Guardrails go back to the shared defaults; point-in-time recovery ends,
  with a logical backup at once, and the old base backups stay
  restorable for 7 days. The stopped dedicated instance is kept for 48
  hours, then destroyed, which releases it from the dedicated allowance.
- `pgdock demote` and Settings → Move back to shared.

### Database branching (V2 M13)
- Branches: throwaway copies of a project on the shared tier, from its
  latest backup or live, schema only or with data, that delete themselves
  after a TTL (7 days by default) with an email a day before.
- Reset a branch from its parent without changing its URL, password or
  members' logins; detach it to keep it as a standalone project.
- Organisation branch quotas (10 on Personal, 25 on Team), branch-hours
  and branch GB-hours usage, and "contains sensitive data" projects whose
  branches copy only the schema by default.
- `pgdock branch list|create|reset|extend|detach|delete`, with `--env`
  for `$GITHUB_ENV`, and an example GitHub Actions workflow that gives
  every pull request its own database.

### Backup storage targets (V2 M12)
- Platform storage targets (one is the default) and organisation targets:
  buckets an organisation brings itself, invisible to everyone else and
  not counted against its backup quota. Every target is live-tested
  (write, read, list, delete) before it's saved; credentials are never
  shown again.
- A project chooses where its new backups go; existing ones stay
  restorable where they are, or are copied over, verified by checksum.
  Dedicated projects move their WAL-G archive and take a fresh base
  backup at once.
- Per-project backup keys: backups become standard OpenPGP messages, and
  the downloaded key file (re-authentication, audited) restores them with
  gpg and pg_restore alone.
- Backup storage on platform targets counts toward the `backup_storage_mb`
  quota.
- Point-in-time recovery reads WAL with the source archive's own
  credentials and key.

### Visual table editing (V2 M11)
- The table browser filters (equals, contains, ranges, null, lists),
  sorts by any column, opens foreign-key rows in a side panel, and
  exports up to 100,000 rows as CSV or JSON.
- Row editing for tables with a primary key: staged inline edits, new
  rows and deletes saved in one transaction, with type-aware inputs.
  If someone changed a row since you loaded it, the save stops with a
  conflict showing their version instead of overwriting it.
- A schema editor for tables, columns, constraints, foreign keys,
  indexes, schemas and enums. Every change previews its SQL with risk
  notes (table rewrites, NOT NULL scans, volatile defaults), runs with a
  5-second lock timeout (indexes concurrently), is audited with its SQL,
  and exports as a plain SQL, goose or dbmate migration.

### API tokens and the CLI (V2 M10)
- API tokens for scripts and CI: one organisation each, read/write/admin
  scopes, an optional project restriction, and a required expiry (90
  days by default, at most a year). Shown once; revocable by their owner
  and by the organisation's owners and admins; disabled when the
  organisation is suspended and revoked when their user leaves it.
- The `pgdock` CLI for linux, macOS and Windows: device login in the
  browser, contexts for several servers and organisations,
  `PGDOCK_TOKEN` for CI, `--json` everywhere, and commands for
  organisations, projects, SQL, connecting, backups, promotion, members,
  tokens and operations. See [docs/cli.md](docs/cli.md).

### Tenancy hardening, quotas and usage (V2 M9)
- Opaque database and role names for new projects; existing projects are
  renamed behind an alias (same URLs) and can switch to opaque
  credentials with a grace period. `pg_stat_activity` no longer shows
  other tenants.
- Quota plans with per-org overrides, checked when creating projects,
  backups, restores, console queries and connections.
- Storage enforcement on the shared tier: warning at 90%, read-only at
  100%, no app logins at 120%, with the console still working and
  Reclaim space. Long statements (10 min) and idle transactions (5 min)
  are ended; `temp_file_limit` is 2 GB.
- Per-org shared clusters, dedicated allowances and dedicated requests.
- Org suspension, break-glass sessions, and an admin console for
  organisations, plans and requests.
- Hourly usage recording, a Usage & quotas page with CSV export, and
  organisation deletion with a 7-day grace period.
- The isolation check runs nightly and covers metadata leaks, quota
  bypass and suspended orgs.

### Users and organisations (V2 M8)
- Many users: sign-up (invite-only by default, approval, or open with
  email domains), email verification, password reset, recovery codes,
  and versioned terms. SMTP is now a setup step.
- Organisations with owner, admin, and member roles; invitations,
  ownership transfer, leaving, and project transfer between orgs.
- Project roles (admin, developer, read-only) with a personal database
  login per member; removing someone drops their login and ends their
  connections at once.
- Organisation, project, and platform audit logs; an org switcher and
  account pages.
- Upgrading: existing projects move into the platform admin's personal
  organisation unchanged. `/api/v1/audit` is now `/api/v1/admin/audit`.

## v1.0.0

The first release: self-hosted managed PostgreSQL 18 with a web UI, on one
VPS or several nodes.

**Install:** [docs/install.md](docs/install.md). **Run it:**
[docs/operations.md](docs/operations.md). **Upgrade:**
[docs/upgrade.md](docs/upgrade.md). **Recover:**
[docs/disaster-recovery.md](docs/disaster-recovery.md).

### Projects
- Shared-tier projects in seconds: a database and role on the shared
  cluster, reached through PgBouncer in session (`:5432`) and transaction
  (`:6543`) mode with TLS required, guardrails per project, password
  rotation, and deletion with a final backup.
- Dedicated projects: their own PostgreSQL 18 container on a node (sizes
  small, medium, large), with continuous WAL archiving and point-in-time
  recovery for 7 days.
- Promotion from shared to dedicated with the same URL and password and a
  short write freeze, verified row by row; the shared copy is kept
  read-only for 48 hours.
- Import from any PostgreSQL 15+ database, Supabase included.

### Backups
- Nightly logical backups (7 daily, 4 weekly), encrypted before upload to
  any S3-compatible bucket; on-demand backups; restore into a new project or
  in place with a safety backup; weekly automatic restore tests; nightly
  self-backups of the metadata database.

### Console, browser, metrics
- SQL console scoped to the project's role (timeout, cancel, 1,000-row cap,
  CSV export, read-only mode), read-only table browser, extension
  allow-list.
- Metrics for projects (size, connections, TPS, cache hit ratio, top
  queries) and nodes (CPU, memory, disk, I/O), 1 h / 24 h / 7 d charts, and
  a Prometheus endpoint.

### Operations and security
- Alerts by signed webhook and SMTP email: backup failed or overdue,
  restore test failed, node disk over 85%, node unreachable, project over
  its disk warning, pooler down, and a failed isolation check.
- The tenant-isolation checklist (spec §7.1) checked in CI and weekly
  against every live shared cluster; `pg_hba.conf` limited to the control
  plane and the poolers.
- One owner account with argon2id passwords and TOTP, server-side
  sessions, CSRF protection, re-authentication for destructive actions,
  rate limiting and lockout, an append-only audit log.
- Secrets encrypted with a master key, rotatable with
  `pgdock-server -rotate-master-key`; agents on mTLS with pinned
  certificates and a major-version check.
- Multi-node: add nodes from the UI, per-node roles, placement by load.

### Tested
- Unit, integration, isolation, failure-injection (agent killed mid-dump,
  pooler killed, disk full, S3 lost), promotion with live writers, a
  150-project load test ([results](docs/load-test.md)), a browser journey
  from a fresh install to every feature, and the install guide itself run
  on a clean machine.
