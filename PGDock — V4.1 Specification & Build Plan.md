# PGDock — V4.1 Specification & Build Plan

*Builds on V3, V3.1 and V4. References like "V3 §3.1" point to those
documents; "Gap n" points to the gap review of 2026-10-09 (V3 to V4
against their specs and build plans).*

|  |  |
| --- | --- |
| **Status** | Draft v1, for review |
| **Theme** | Finish what V3, V3.1 and V4 promised but didn't build, and close the launch gates that are still open |
| **Builds on** | V4 (M28–M37), merged into `feature/pgdock4` |
| **Scope** | Every gap in the review except those the decisions log already records as deliberate deviations |

---

## 1. Overview

### 1.1 Why now

V4 is merged and GA-ready in code, but the review found three kinds of gaps:

| Kind | Examples | Risk if left |
| --- | --- | --- |
| **Promised and dropped** | Per-plan limits for backend services (V4-M28 decision 10 said M37); automatic status page subscriptions (M17 decision 13 said M20) | Free projects use unlimited API requests and MAU; paying customers don't hear about outages |
| **Specified, never built** | Billing add-ons, instance resize, on-demand dedicated hosts, edge caching, per-table API docs, branch services, cost attribution for V4 | Revenue the price book can't charge for; manual work for operators; margins on V4 unknown |
| **Launch gates that need people** | Accountant's answers, real payment sandboxes, Hetzner rehearsal, penetration test, 20 GB move, 1,000-project load | Launch on unproven numbers |

### 1.2 What V4.1 adds

| # | Milestone | Gaps | Section |
| --- | --- | --- | --- |
| 1 | Doc and legal corrections | 2 (doc), 13, 24 | §2 |
| 2 | Per-plan limits for backend services | 1 | §3 |
| 3 | Billing add-ons: extended PITR, longer backup retention, region premium | 3 | §4 |
| 4 | Dedicated resize, disk growth, hosts provisioned on demand | 4, 6 | §5 |
| 5 | Postgres version lifecycle and dedicated upgrade preflight | 7 | §6 |
| 6 | Status page: automatic subscriptions, more components, per-region heartbeats; app-wide banners | 2, 5, 8 | §7 |
| 7 | V3.1 leftovers: region page, proposed announcements, email preview, rebalancing in the window | 14, 15, 16 | §8 |
| 8 | Backend-services developer experience: quick-start, per-table docs, usage, CLI, branches | 19–23 | §9 |
| 9 | Edge caching of anonymous reads (optional) | 18 | §10 |
| 10 | Cost attribution for backend services | 25 | §11 |
| 11 | Test coverage: RLS sample apps, auth review, code brute force, transform pool | 26, 27, 30 | §12 |
| 12 | Scale proof and launch gates | 9–12, 17, 28, 29 | §13 |

### 1.3 Not in V4.1

The V5 list (V4 §15) stays V5: edge functions, custom domains, SSO/SAML,
Kotlin/Swift/Python SDKs, logical-decoding realtime, malware scanning,
GraphQL, dollar billing, Terraform, schema diff, branch masking. So do the
recorded deviations (leaked-password check, auth email bounces,
region-local replica endpoints, per-region API domains, moving files with a
cross-region move, NULLS FIRST/LAST).

### 1.4 Principles

- **Nothing that works changes behaviour silently.** New limits apply only
  from the price book or quota plan that sets them; a V4 install upgraded
  with nothing configured behaves as V4 does, except where a limit is a
  documented promise (Free plan inclusions, §3).
- **Prices stay in price books.** Every new charge is a price book field,
  published like any other (V3 §3.9), never a constant.
- **One mechanism per job.** Monthly limits reach the edge the way storage
  quotas already do (the sweep sets flags on the feed, V4-M33 decision 9);
  resizes reuse the agent's `Recreate`; announcements reuse V3.1's.
- **Done-when or it isn't done.** Each milestone ends with its test, docs,
  a `docs/decisions.md` section (`V4.1-Mn`) and CI green, as before.

---

## 2. V4.1-M1 — Doc and legal corrections (0.5 week)

Small and first, because they are wrong today.

| Change | File | What |
| --- | --- | --- |
| Lagos etcd | `docs/lagos-launch.md` step 9 | Replace "per-region etcd for later" with: set up the Lagos etcd cluster (Platform → Nodes → etcd, region `ng-lagos`) on three nodes in three failure domains; move Lagos HA projects onto it (V3.1 §3.3). |
| Status page limits | `docs/status-page.md` "Limits" | Remove the M19/M20 future tense; state what is true (two vantage points exist; automatic subscriptions arrive in V4.1-M6). |
| Sub-processors | `internal/legal/defaults.go` (DPA template) | Add rows: Termii (SMS codes), Africa's Talking (SMS codes, fallback), Meta / WhatsApp Business Platform (WhatsApp codes and support), Twilio (only when a project brings its own), Cloudflare (CDN for public files, captcha). Publishing the template as a **new DPA version** makes owners accept it again (V3 §7.3), which is the correct behaviour for a sub-processor change; the 30 days' notice in the DPA applies, so the version gets an effective date 30 days out. |
| Docs index | `docs/site.json` | Add this plan's companion docs as they land. |

**Done when:** `TestDocs` passes; `TestLegalDocuments` gains a case
checking the default DPA lists every provider in
`internal/messaging` that the platform itself holds credentials for (so a
new provider without a DPA row fails CI).

---

## 3. V4.1-M2 — Per-plan limits for backend services (1.5 weeks)

### 3.1 What the spec asks (V4 §10)

|  | Free | Pro | Team |
| --- | --- | --- | --- |
| Data API requests / month | 500k, hard | 5M included, then metered | 25M included, then metered |
| Auth MAU / month | 10k, hard | 50k included, then metered | 200k included, then metered |
| API request timeout | 5 s | 8 s | 15 s |
| Rate limits | per-key/IP buckets plus per-project ceilings from the plan |
| SMS daily cap | from the plan |

Inclusions and overage prices already exist in the price book (V4-M37).
What is missing is the **enforcement**: hard limits for Free, the
per-plan timeout, and ceilings.

### 3.2 Design

**New quota plan limits** (`internal/store/limits.go`, next to the
storage and realtime ones):

| Key | Personal (Free) | Pro | Team | Unlimited |
| --- | --- | --- | --- | --- |
| `api_requests_per_month` | 500,000 | 0 (no hard limit) | 0 | 0 |
| `auth_mau_per_month` | 10,000 | 0 | 0 | 0 |
| `api_timeout_ms` | 5,000 | 8,000 | 15,000 | 15,000 |
| `api_rate_per_ip_per_min` (ceiling) | 300 | 600 | 1,200 | 6,000 |
| `api_rate_per_key_per_min` (ceiling) | 3,000 | 12,000 | 30,000 | 120,000 |
| `sms_codes_per_day` | 0 (platform SMS off on Free, as today) | 200 | 1,000 | 5,000 |

`0` means "no hard limit" for monthly counters, as for existing limits.
Paid plans are limited by the spend cap (V4-M37), not by a hard count.

**Effective settings.** A project's settings (`project_services.settings`)
may ask for any value; the feed sends `min(project setting, plan
ceiling)` for timeouts and rates. The dashboard shows both ("8 s, the
most your plan allows"). Changing plan updates the ceiling within one
feed cycle, because the plan change already bumps the org's projects on
the feed (the same trigger M37 added for `capped`, extended to plan
changes).

**Monthly hard limits.** Exactly the storage-quota pattern: the
5-minute sweep (`internal/services`, beside the file-storage sweep)
reads this month's `api_requests` and `auth_mau` usage per organisation,
compares with the limits and sends two flags per project on the feed:
`api_requests_exhausted`, `mau_exhausted`.

- `api_requests_exhausted`: data, storage and realtime requests answer
  `429 plan_limit_reached` with `Retry-After` set to the start of next
  month and a message naming the limit and the upgrade path. `/auth/v1`
  keeps working (signing in must not break; V4 §12 says throttle, never
  break sign-in), as does `/data/v1/health`.
- `mau_exhausted`: sign-ins and refreshes of users **already counted this
  month** still work; a user who would be a new MAU gets
  `429 mau_limit_reached`. The edge can tell, because MAU reports already
  carry the user: the feed sends a small per-project Bloom filter
  (or, under 10k users, the exact set of hashed ids) of this month's
  counted users. Decision for review: the exact set at 10k users is
  160 KB per project on the feed; a Bloom filter at 1% false positives is
  12 KB. Recommend the Bloom filter, with false positives erring
  toward letting the user in.
- Overshoot is bounded by one sweep interval of traffic, as for storage
  (V4-M33 decision 9). Documented.

**SMS cap from the plan.** The pumping check at pgdock-server
(V4-M32 decision 4) reads the cap from the org's quota plan instead of
the flat 200, with the project's own lower cap still honoured.

**Notices.** At 80% and 100% of a hard limit, owners and admins get one
email each per month (the same mailer and once-a-month guard as budget
alerts, V3 §3.10). The Usage page shows the bar.

### 3.3 Data model

```sql
-- 00044_plan_limits.sql
-- New keys only: quota_plans.limits is jsonb. The migration sets the
-- defaults above on the built-in plans that have no value yet.
UPDATE quota_plans SET limits = limits || '{"api_requests_per_month":500000,
  "auth_mau_per_month":10000,"api_timeout_ms":5000,"api_rate_per_ip_per_min":300,
  "api_rate_per_key_per_min":3000,"sms_codes_per_day":0}'::jsonb
  WHERE name = 'Personal' AND NOT limits ? 'api_timeout_ms';
-- (likewise Pro, Team, Unlimited)
```

Feed: `edgeapi.ProjectConfig` gains `PlanLimits {TimeoutMs, RatePerIP,
RatePerKey}`, `APIRequestsExhausted bool`, `MAUExhausted bool`,
`CountedUsers []byte` (Bloom filter). Older edges ignore the new fields.

### 3.4 API, UI, CLI

- `GET /projects/{id}/services` gains `effective` (what the edge applies)
  and `plan_ceilings`.
- Project → API → Access and limits: the effective values and ceilings.
- Org → Usage: API requests and MAU bars against the plan's limit.
- `pgdock services status` prints effective limits.

### 3.5 Done when

`TestPlanLimitsBackendServices`: a Free project with the request limit
set to 50 in its plan serves 50 requests, then `429 plan_limit_reached`
on data and storage while sign-in still works; upgrading the org to Pro
lifts it within one feed cycle; the MAU limit set to 2 lets the two
counted users sign in again and refuses a third; a 6-second query times
out on Free (5 s) and succeeds on Team; a project asking for 20,000
requests/min per key gets the plan's ceiling; the SMS cap follows the
plan. Unit tests for the effective-settings merge and the Bloom filter.

---

## 4. V4.1-M3 — Billing add-ons (2 weeks)

V3 §3.1 lists HA, extra disk, extended PITR (14 or 30 days), extra backup
retention, Lagos region premium and synchronous replication. Built: HA,
synchronous replication, disk (per GB-hour). Missing: the other three.

### 4.1 Extended point-in-time recovery (dedicated)

Today WAL-G keeps `RetainFull = 7` base backups for every instance
(`internal/dedicated/dedicated.go`), one a day, so the window is about 7
days.

- **Per-instance setting** `pitr_days` ∈ {7, 14, 30}, default 7.
  Stored on `instances` (`pitr_days int NOT NULL DEFAULT 7`).
- The base-backup job passes `RetainFull: pitr_days` (WAL-G deletes WAL
  older than the oldest kept base backup, so the window follows).
- Lowering it takes effect at the next base backup (older backups are
  deleted then); the UI says so. Raising it is immediate for the future
  only: the window grows day by day to the new length, and the UI shows
  the current earliest restorable time (already computed for PITR).
- **Billing:** usage metrics `pitr_14_hours` and `pitr_30_hours` (one per
  hour while set), price book `addons.pitr_14_hour`, `addons.pitr_30_hour`.
  The extra WAL and base backups are already metered as backup storage;
  the add-on price is the service premium on top, as the spec's add-on
  list intends.
- **Plans:** allowed on Pro and Team only (quota plan limit
  `pitr_days_max`: Personal 7, Pro 30, Team 30).
- **Residency:** unaffected (same target).

### 4.2 Longer backup retention (shared and dedicated logical backups)

Today `backup.DefaultRetention = {Daily: 7, Weekly: 4}` for every project.

- **Per-project setting** `backup_retention` ∈ `standard` (7 daily, 4
  weekly), `extended` (30 daily, 12 weekly), `long` (30 daily, 52
  weekly). Stored in `projects.settings`.
- Retention reads the project's policy instead of the constant.
- **Billing:** metrics `backup_retention_extended_hours`,
  `backup_retention_long_hours`; price book fields alongside PITR.
  Storage used is metered as today.
- **Plans:** limit `backup_retention_max` (Personal `standard`, Pro and
  Team `long`).
- Archived Free projects keep their archive backup as now (no change).

### 4.3 Region premium (Lagos)

- Price book gains `addons.region_premium_percent: {"ng-lagos": "25"}` (a
  map: any region can carry one; none by default).
- Rating adds one line per project per region with a premium: the
  percentage of that project's **dedicated, HA standby, read replica and
  synchronous replication** lines (the things a region's hardware costs
  drive), labelled "Lagos region premium". Shared projects in a premium
  region pay the premium on their plan's overage lines only (the plan fee
  itself is the same everywhere; Lagos shared is not offered at launch,
  V3 §6.2, so this is a rule for later).
- The cost estimate (`POST /billing/estimate`) gains `region` and shows
  the premium line.
- Validation: at most 500%, like the HA premium.

### 4.4 UI, API, CLI

- Project → Settings → Backups: retention choice; Dedicated → PITR window
  choice; both with the cost estimate component (`CostEstimate.tsx`) and
  the plan gate.
- `PATCH /projects/{id}` accepts `backup_retention`; `PATCH
  /projects/{id}/instance` (new, also used by §5) accepts `pitr_days`.
- Price book editor (Admin → Billing → Price books) shows the new fields;
  the preview (V3 §3.9) includes them.
- `pgdock backup retention <p> extended`, `pgdock pitr window <p> 30`.

### 4.5 Done when

`TestBillingAddOns`: a dedicated project set to 14-day PITR keeps 14 base
backups after 15 simulated days (the fake clock and the test WAL-G
target) and restores to a point 12 days back; a shared project on
`extended` retention keeps 30 dailies; a Lagos dedicated project's
invoice carries a 25% premium line computed on exactly its dedicated
and HA lines; the ledger check (`billing.Check`) balances; Personal is
refused 14-day PITR with `plan_required`.

---

## 5. V4.1-M4 — Resize, disk growth, hosts on demand (2 weeks)

### 5.1 Resizing a dedicated instance

- `PATCH /projects/{id}/instance` with `cpus`, `memory_mb` (from the
  size list, `GET /dedicated/sizes`, or custom within the allowance).
- Preflight: the dedicated allowance (V2 §10.3) and the node's free
  capacity. If the node can't fit it, the resize becomes a **node move**
  (V3 §2.3, zero-downtime) to a node that can, and the preflight says so.
- **Non-HA:** the agent's `Recreate` with the new limits (the same path
  minor upgrades use): a restart of a few seconds with the poolers holding
  clients. Postgres settings derived from memory (`shared_buffers`,
  `effective_cache_size`, `work_mem`) are recomputed.
- **HA:** rolling: resize the standby (recreate), wait for it to stream,
  switch over (V3 §2.2), resize the old primary. Pause equals a
  switchover's.
- **Read replicas:** resized with the instance (same size rule as V4 §7),
  one at a time, each leaving rotation while it restarts.
- Billing follows automatically: the hourly recorder already reads the
  instance's size each hour.
- Operation kind `resize_instance`; history on the project's Operations.

### 5.2 Growing the disk

Volumes are Docker named volumes with no hard size; `disk_gb` is the
allowance, the disk-warn threshold and what is billed.

- `PATCH /projects/{id}/instance` with `disk_gb` (up only; shrinking is
  refused with a pointer to a move into a smaller instance).
- Preflight: the node's free disk minus other instances' allocations; if
  it doesn't fit, offer a move.
- On providers with attachable volumes (Hetzner), a node's data disk is
  grown first through the provider interface (`CreateVolume` /
  `AttachVolume`, V3 §5.1, already implemented in the fake) when the
  capacity planner says the node is short; manual nodes need the admin.
- The disk-warn setting (`DiskWarnBytes`, 80% of volume) is updated.

### 5.3 Dedicated hosts provisioned on demand (V3 §5.2)

Today creation fails with `no capacity` when no host fits.

- When placement finds no host, and the region has a provider that can
  create servers (Hetzner), creation proceeds as a **two-phase
  operation**: open (or reuse) a capacity proposal for a dedicated host
  sized for the request; if it is within the infrastructure budget it is
  applied at once (V3 §5.2), and the project's `create_dedicated`
  operation waits for the new node (up to 25 minutes) before placing.
- The project is shown as **Waiting for a host** with an estimate
  ("usually 5–10 minutes"); the UI already streams operation steps.
- Over budget: the creation is refused with `capacity_pending_approval`,
  the proposal waits for the admin (who gets the existing
  capacity alert), and the user is told it has been requested.
- Manual-only regions keep refusing, naming the region.

### 5.4 UI and CLI

- Project → Settings → Instance: size picker and disk field, each with
  `CostEstimate` (V3 §3.10 "cost estimate before every billable action").
- `pgdock instance resize <p> --cpus 4 --memory 8192`, `pgdock instance
  disk <p> --gb 160`.

### 5.5 Done when

`TestDedicatedResize`: a running dedicated project with a writer is
resized 2→4 vCPU (pause under 10 s, no lost commits); an HA project is
resized by switchover (no client errors beyond the switchover's); disk
grows 40→80 GB and the warn threshold follows; a resize that doesn't fit
its node moves it; the next hour's usage carries the new size.
`TestDedicatedHostOnDemand`: with the fake Hetzner and no free host, a
dedicated create provisions a node within budget and lands on it; over
budget it is refused with `capacity_pending_approval` and an open
proposal.

---

## 6. V4.1-M5 — Postgres version lifecycle (1 week)

### 6.1 Lifecycle

- New table `pg_versions (major int PRIMARY KEY, status text, -- preview |
  supported | deprecated | retired, deprecated_at timestamptz,
  retires_at timestamptz, notes text)`, seeded from `PGDOCK_PG_VERSIONS`
  on upgrade.
- **Preview** (V3 §2.4 "the next major once it has had its first minor
  release"): selectable for new projects only behind a flag on the create
  form, never the default, shown with a badge. An admin promotes it to
  supported.
- **Deprecate** (Admin → Platform → Postgres versions): sets
  `retires_at` at least **180 days** ahead (refused otherwise), emails the
  owners and admins of every org with a project on that major (a list of
  their projects and the one-click upgrade), and puts a banner on those
  projects. Reminders at 90, 30 and 7 days.
- **Retired:** no new projects; existing ones are not upgraded
  automatically (a major upgrade is the owner's decision), but the
  project page shows "unsupported" and the terms say what that means.
  Decision for review: auto-upgrade at retirement instead? Recommend not:
  an upgrade can break an app; the notices are the safeguard.

### 6.2 Upgrade preflight for dedicated targets

V3 §2.4: "checks extension compatibility on the target version and flags
deprecated features found in the schema". Shared targets restore the
schema into a scratch database today (V4-M18 decision 10); dedicated
targets get no check.

- For a dedicated target, the preflight starts a **temporary instance of
  the target major** on the same node (small, no WAL-G), restores the
  schema-only dump into it as a non-superuser, reports failures, then
  removes it. Same report format as shared.
- **Deprecated-feature scan** for both: a list of known removals per
  major (kept in `internal/logical/deprecated.go`: e.g. removed functions,
  changed GUC names, `pg_stat_statements` column renames) matched against
  `pg_proc` bodies, views and settings; warnings, not blocks.

### 6.3 Done when

`TestPgVersionLifecycle`: deprecating 17 with `retires_at` 100 days ahead
is refused, 180 accepted; owners of a 17 project get the email and the
banner; after the clock passes `retires_at`, new 17 projects are refused.
`TestDedicatedUpgradePreflight`: a dedicated 17 project with an object
that fails on 18 (seeded) is reported by the preflight and the move is
not started; the temporary instance is gone afterwards.

---

## 7. V4.1-M6 — Status page and banners (1.5 weeks)

### 7.1 Automatic subscriptions (V3 §2.6)

- pgdock-status gains a signed endpoint `PUT /api/v1/subscribers/managed`
  taking the full list of managed subscribers: email, the components
  (and regions) they care about. Managed subscribers skip double opt-in
  (they are customers' billing contacts or owners, under the terms) but
  every email carries the unsubscribe link, and an unsubscribe is
  remembered (pgdock-status keeps a tombstone so the next sync doesn't
  re-add them).
- pgdock-server sends the list hourly: for each paying org (plan not
  Free, not suspended), its billing contacts and owners, with the
  components their projects use (shared tier / dedicated / HA / backend
  services) in their projects' regions.
- Org → Billing → Notifications: "Status page emails" toggle per contact
  (off removes them at the next sync).

### 7.2 Components and regions

- New heartbeat components: **billing** (degraded while a payment
  provider is unreachable per the outage tracking of M27, or invoices for
  the month haven't issued by the 2nd), **backend services** (per
  region: degraded when an edge stopped reporting (`edge_reports` older
  than 2 minutes) for some edges, down when for all; plus an external
  probe of `/healthz` on each region's edge address, configurable in
  pgdock-status's TOML).
- Heartbeats are sent per region (`ComponentState.Region`); the page
  groups components by region as the config already allows.

### 7.3 App-wide banners (V3 §11)

`OrgBanners` (`web/src/components/Layout.tsx`) gains:

- **Billing state**, to every member: overdue, restricted, suspended
  (billing), payment failed. Members without billing access see the
  state and "ask an owner or a billing member"; owners and billing
  members get the pay link. The text never shows amounts to members
  without billing access.
- **Budget thresholds** (80%, 100%), to owners and billing members.
- **An incident affecting your projects:** an open incident whose
  components and region match one of the org's projects (or which names
  one of its projects or nodes, V3.1 `incident_scope`), with a link to
  the status page. New endpoint `GET /orgs/{org}/incidents` returns only
  matching incidents with public fields.

### 7.4 Done when

`TestStatusSubscriptions`: a Pro org's billing contact appears on the
status page's subscriber list after a sync and gets an incident email; an
unsubscribe survives the next sync; a Free org's owner isn't added.
`TestStatusComponents`: a stopped edge marks backend services degraded in
its region only. E2E: a member of an overdue org sees the billing banner
without amounts; an open incident in the org's region shows the incident
banner.

---

## 8. V4.1-M7 — V3.1 leftovers (1 week)

### 8.1 Region page (V3.1 §7)

Platform → Regions → region:
- **Pooler pair:** its two hosts with their failure domains, and the
  warning when they share one (the check exists, `GET
  /admin/failure-domains`; this shows it in place).
- **etcd:** a link to the region's cluster on Nodes (it stays there) and
  its health summary.
- **HA projects on another region's etcd:** the list (already computed
  as `etcd_move_available`) with **Move all**, which queues the per-project
  `etcd_move` operations one at a time (each pauses its own project 5–7 s;
  never two at once in a region). `POST /admin/regions/{id}/etcd-move-all`.

### 8.2 Proposed announcements (V3.1 §4.1, open question 3)

- When work is queued that the window gate holds (an HA minor upgrade, an
  approved rebalance batch), PGDock **drafts** an announcement for the
  next window that is at least 96 hours away (72 for the SLA plus a day
  for the admin to confirm), scoped to the affected projects.
- Drafts appear in Admin → Incidents → Maintenance with **Confirm**
  (announces: sets `announced_at`, emails) and **Discard**. A draft never
  emails and never excludes anything; `announced_at` stays set only by
  confirming, so V3.1's rule ("exclusions derive from records made
  before the event") holds.
- Data model: `incidents.status` gains `draft` for maintenance incidents.

### 8.3 Email preview

The schedule form shows the exact email an affected owner will get
(rendered by the server from the same template, `POST
/admin/maintenance/announcements/preview`), with the count of
organisations and addresses it will go to.

### 8.4 Rebalancing inside the window (V3.1 §4.3)

Automatic rebalancing (V3 §5.3, "or enables automatic rebalancing during
the maintenance window") runs its approved moves only inside the window,
and, for moves touching an HA project's node by switchover, only under an
announcement covering it (the existing `maintenance_covers` function).

### 8.5 Done when

`TestMaintenanceProposals`: an HA instance behind on its minor release
produces a draft for the next window ≥ 96 h away; confirming it emails
and, 73 h later, the gate lets the upgrade run; discarding leaves the
work waiting; the preview matches the sent email byte for byte. E2E:
Move all on a region with two projects on the home etcd moves both, one
after the other.

---

## 9. V4.1-M8 — Backend-services developer experience (2 weeks)

### 9.1 Quick-start (V4 §2.4 step 4)

Project → API, after enabling: a **Quick start** panel with the project
URL and publishable key filled in, in TypeScript, Dart and Go
(install line, `createClient`, a read, sign-in with a phone code), and
"Generate types" linked. Collapsible, remembered per user.

### 9.2 Per-table API docs (V4 §3.7, §8.3)

Project → API → **Docs**: a list of exposed tables, views and functions
from the catalog (`internal/datacat`, the same source as typegen and
OpenAPI). Each page shows:
- columns with types, nullability, defaults, generated and enum values;
- the RLS state and policies (from the advisor), and whether anon/user
  can read or write;
- request examples for read (filters, embeds from its foreign keys),
  insert, update, delete, upsert and RPC, in curl, TypeScript, Dart and
  Go, with the project's URL and table names filled in;
- a **Try it** link into the request explorer, prefilled.

Rendered client-side from a new `GET /projects/{id}/services/catalog`
(platform read, as typegen does). No secrets in examples (publishable key
only).

### 9.3 Usage per service on the API page (V4 §8.3)

A **Usage this month** panel: requests, transfer, MAU, messages, file
storage and downloads, transforms, realtime minutes and messages, each
against the plan's inclusion or limit (from §3), and the month's charges
per service (the M37 breakdown, filtered to this project).

### 9.4 CLI (V4 §8.2)

- `pgdock policies list <p> [--table t]`: policies per table with roles
  and expressions, in the friendly role names.
- `pgdock policies lint <p>`: the security advisor's findings, exit code
  1 on any `danger` finding (for CI), `--json`.
- `pgdock logs api <p> [--follow] [--status 5xx] [--path /data/v1/…]`:
  the request log; `--follow` long-polls a new
  `GET /projects/{id}/services/logs?after=<cursor>&wait=25s`.

### 9.5 Branches with backend services (V4 §2.5)

When the parent has backend services enabled, creating a branch
(including from the CLI and the GitHub Actions example) also:
- enables services on the branch: a **new ref**, new publishable and
  secret keys, a new signing key (never the parent's: tokens from one must
  not work on the other);
- copies the parent's settings (exposed schemas, public tables, CORS
  origins, auth configuration **without** OAuth secrets, SMTP password or
  captcha secret, which the branch's owner must set; templates kept);
- leaves auth users as the data copy made them (with V2 §8.5's
  sensitive-data rule: a schema-only branch has none);
- returns the branch's API URL and keys in the create response and
  `pgdock branch create --env` output (`PGDOCK_API_URL`,
  `PGDOCK_PUBLISHABLE_KEY`, `PGDOCK_SECRET_KEY`).
- **Optional file copy** (V4 §2.5 "an option to copy files"): `copy_files:
  true` on branch create copies the parent's objects into the branch's
  prefix in the background (a `copy_branch_files` operation, server-side
  copy in the store, counted against the org's file storage); without it,
  files show as missing, as today.

### 9.6 Done when

`TestBranchServices`: a branch of a services-enabled project answers at
its own ref, refuses the parent's keys and tokens, serves its copied
users, keeps the parent's exposed schemas and public tables, has no OAuth
secret; with `copy_files` its files download. `TestCLIPolicies`:
`policies lint` exits 1 on a table without RLS. E2E: the quick-start and
a table's docs page show the project's URL and key; "Try it" runs.

---

## 10. V4.1-M9 — Edge caching of anonymous reads (1.5 weeks, optional)

V4 §3.8, and open question 6 ("worth including in V4, or wait until
customers ask?"). Built only if the review says yes.

- **Opt-in per table or function**: `settings.cache_ttl_seconds:
  {"public.products": 60, "rpc.search_products": 30}`, max 3,600.
- **Eligible:** `GET` with the publishable key only (no user token, no
  `Read-Replica` primary override), on a listed relation; never storage,
  auth or realtime.
- **Key:** ref + path + normalised query (sorted parameters) + catalog
  version; the role is always anon, so policies are the same for all.
- **Store:** an in-process LRU per edge (bounded memory, e.g. 64 MB per
  process), plus `Cache-Control: public, max-age=<ttl>` so a CDN in front
  can cache too.
- **Invalidation:** writes through the data API to a table drop its
  entries on that edge immediately; other edges and writes made outside
  the API (SQL, jobs) are bounded by the TTL. Where realtime capture is
  enabled on the table, its change notifications also drop entries on
  every edge listening. Documented plainly: a TTL is a staleness budget.
- **Metering:** cached responses still count as requests and egress.
- **Headers:** `X-Cache: HIT|MISS`, `Age`.

**Done when:** `TestDataCache`: a listed table's second anonymous read is
a HIT; a user-token read is never cached; an insert through the API drops
the entry; an SQL insert shows within the TTL; requests are metered either
way.

---

## 11. V4.1-M10 — Cost attribution for backend services (1 week)

V4 §12: "allocates edge node cost by request and connection share, object
storage by GB, and SMS by actual cost."

- **Edge nodes:** a node can carry the role `edge` (dedicated edge nodes,
  V4 §2.1, via capacity proposals with tier `edge`). An edge node's daily
  cost is divided among orgs by their share of the day's
  `api_requests` (half) and `realtime_connection_minutes` (half). Where
  the edge runs on shared nodes, a configurable share of those nodes'
  cost (`edge_share_percent`, default 0) is moved from the shared pool to
  this category the same way.
- **File storage:** `storage_gb_hours` × the object storage price
  (`Settings.ObjectStorageGBMonth`, already there), plus
  `storage_egress_gb` × `EgressGB`.
- **Messages:** the recorded provider cost of each platform SMS and
  WhatsApp code (`messages_*_cost_kobo`, M37), converted to the cost
  currency at the day's FX rate, booked to the org directly.
- New categories `edge`, `files`, `messages` in `internal/costs`;
  margins per service on Platform → Costs & margins, next to the M37
  revenue breakdown.
- **Capacity proposals for edge nodes:** when the region's edge
  processes' CPU (reported in edge reports) stays above 70% for an hour,
  propose an `edge` node; it joins with the edge image only.

**Done when:** `TestCostAttributionBackendServices` checks each org's
edge, files and messages cost against a hand calculation (as
`TestCostAttributionMatchesManual` does), and an edge-tier proposal is
raised by sustained CPU.

---

## 12. V4.1-M11 — Test coverage (1.5 weeks)

### 12.1 RLS sample apps (V4 §13)

`test/integration/rls_apps_test.go`, one test per app, each asserting
what anon, two users and service see through **data, storage and
realtime**:

| App | Schema | Asserts |
| --- | --- | --- |
| **Todo** | (exists in `TestDataAPIReads`) | extended to storage (attachments per todo) and realtime (own rows only) |
| **Marketplace** | sellers, listings (public read, seller write), orders (buyer and seller read, buyer create), listing images in a public bucket, invoices in a private bucket | anon reads listings and images, not orders; a buyer sees own orders and their invoice file, not others'; a seller sees orders for their listings; realtime: sellers get new orders on their listings only |
| **Chat** | rooms, members, messages; private channels via `channel_access` | non-members can't read messages, join the room's channel or receive its broadcasts; a removed member stops receiving changes; presence shows members only |

### 12.2 Auth review (V4 §13 "OWASP ASVS-aligned")

`docs/security-review.md` gains **V4 auth against ASVS 4.0 level 2**: a
table of the V2 (authentication), V3 (session), V6 (stored
cryptography) and V11 (business logic: rate limits) requirements that
apply, how PGDock meets each, and the test that shows it; gaps found are
fixed in this milestone or listed as accepted.

### 12.3 Code brute force

`TestAuthCodeBruteForce`: five wrong codes for an email OTP, a phone OTP
and an MFA phone challenge use the code up (`otp_expired` on the sixth
attempt even with the right code); the per-number and per-IP limits stop
requesting new codes faster than the documented rate; a TOTP code can't
be reused.

### 12.4 Transforms in a separate pool (V4 §16)

Image renders run in a child process pool (`pgdock-edge render-worker`,
the same binary, started by the edge, talking over a pipe), so a decoder
crash or a memory blow-up kills a worker, not the edge. Pool size one per
two CPUs as now; memory per worker capped (`GOMEMLIMIT`, and
`RLIMIT_AS` where available). `TestRenderWorkerCrash`: a crafted image
that panics the decoder returns `500 transform_failed` while other
requests on the same edge keep working.

---

## 13. V4.1-M12 — Scale proof and launch gates (1 week of engineering, plus operator time)

Mostly work for people; this milestone gives each a script and a record.

| Gate | Engineering part | Operator part | Recorded in |
| --- | --- | --- | --- |
| **20 GB move** (M18 done-when) | `make test-move` already exists; add a CI job (`workflow_dispatch`) for a runner with ≥ 80 GB disk | Run it once; record copy time, pause and commits | `docs/moves.md`, decisions |
| **1,000 projects at 2,000 req/s** (V4 §13) | `deploy/loadtest/`: compose for an edge host, a load host and 4 shared nodes; `TestBackendLoad` against remote addresses (`PGDOCK_LOAD_TARGET`) | Run on 6 Hetzner servers (≈ €2 for the hour); record | `docs/load-test.md` |
| **Payment sandboxes** (M21) | A `make test-payments-sandbox` target that runs `TestPaymentsAcrossProviders` against `PGDOCK_FLW_*`/`PGDOCK_ISPEND_*` sandbox values, skipping simulated outages it can't cause | Get sandbox keys; run; fix differences | `docs/payments.md` |
| **Hetzner floating IP rehearsal** (M17) | `scripts/rehearse-pooler-failover.sh`: kills the holder and times recovery from a client | Run on two real servers | `docs/edge-poolers.md` |
| **Accountant's answers** (M27) | — | Six questions in `docs/billing-audit.md`; change settings to match | `docs/billing-audit.md` |
| **Lagos failure domains** (V3.1 open question 1) | A launch check that refuses to un-hide a region offering HA unless its etcd has three domains | Confirm racks or feeds with the facility | `docs/lagos-launch.md` |
| **Penetration test** (V4 §13) | Staging region per `docs/pentest-scope.md`; fix findings | Engage testers; retest | `docs/pentest-scope.md` tracking table |

**Done when:** every row has a dated result in its doc, and GA of backend
services is announced (or a dated list says what still blocks it).

---

## 14. Data model summary

```sql
-- 00044_plan_limits.sql       quota plan keys (§3)
-- 00045_billing_addons.sql
ALTER TABLE instances ADD COLUMN pitr_days int NOT NULL DEFAULT 7
  CHECK (pitr_days IN (7, 14, 30));
-- projects.settings gains backup_retention (jsonb, no DDL)
-- 00046_pg_versions.sql
CREATE TABLE pg_versions (
  major int PRIMARY KEY,
  status text NOT NULL CHECK (status IN ('preview','supported','deprecated','retired')),
  deprecated_at timestamptz, retires_at timestamptz, notes text);
-- 00047_maintenance_drafts.sql
-- incidents.status allows 'draft' for severity 'maintenance'
-- 00048_status_subscribers.sql
ALTER TABLE billing_contacts ADD COLUMN status_emails boolean NOT NULL DEFAULT true;
-- 00049_costs_services.sql
-- nodes.role allows 'edge'; cost categories are values, no DDL
```

**New usage metrics:** `pitr_14_hours`, `pitr_30_hours`,
`backup_retention_extended_hours`, `backup_retention_long_hours`.

**New operation kinds:** `resize_instance`, `grow_disk`,
`copy_branch_files`, `etcd_move_all` (a parent of per-project moves).

## 15. API additions

| Method and path | What | Milestone |
| --- | --- | --- |
| `PATCH /projects/{id}/instance` | `cpus`, `memory_mb`, `disk_gb`, `pitr_days` (preflight with `dry_run`) | M3, M4 |
| `GET /projects/{id}/services` | gains `effective`, `plan_ceilings` | M2 |
| `GET /projects/{id}/services/catalog` | tables, columns, policies, functions for the docs page | M8 |
| `GET /projects/{id}/services/logs?after=&wait=` | long-poll for `--follow` | M8 |
| `POST /projects/{id}/branches` | gains `copy_files`; response gains `api` (URL, keys) | M8 |
| `GET /orgs/{org}/incidents` | open incidents affecting the org | M6 |
| `GET/POST /admin/pg-versions[/{major}/deprecate]` | version lifecycle | M5 |
| `POST /admin/regions/{id}/etcd-move-all` | Move all | M7 |
| `POST /admin/maintenance/announcements/preview` | email preview | M7 |
| `POST /admin/maintenance/announcements/{id}/confirm` | confirm a draft | M7 |
| pgdock-status `PUT /api/v1/subscribers/managed` (signed) | managed subscribers | M6 |

## 16. Build plan and timeline

Same rules as V3 and V3.1 (`docs/v3-plan.md`): one milestone at a time,
each finished with its done-when test, docs, decisions (`V4.1-Mn` in
`docs/decisions.md`), changelog and CI green. Milestone numbering
`V4.1-M1` … `V4.1-M12`; M38 onwards stays free for V5.

| Week | Milestone | Outcome |
| --- | --- | --- |
| 0.5 | M1 Doc and legal corrections | Docs and the DPA true today |
| 1–2 | M2 Per-plan limits | Free tier bounded; plans mean what the pricing page says |
| 2–4 | M3 Billing add-ons | Every V3 add-on sellable |
| 4–6 | M4 Resize, disk, hosts on demand | Customers grow without a ticket |
| 6–7 | M5 Version lifecycle | Retiring a major is a process, not a surprise |
| 7–8 | M6 Status and banners | Customers hear about outages and billing problems |
| 8–9 | M7 V3.1 leftovers | Maintenance is proposed, previewed and gated |
| 9–11 | M8 Developer experience | First request in minutes; branches work for CI with the API |
| 11–12 | M9 Edge caching (optional) | Cheap anonymous reads |
| 12–13 | M10 Cost attribution | V4 margins visible |
| 13–14 | M11 Test coverage | The spec's test matrix complete |
| 14–15 | M12 Scale proof, launch gates | GA announced on evidence |

About **15 weeks** for one engineer, **13.5 without M9**. M1, M2 and M12's
operator rows can start in week 1 in parallel (they have lead times:
accountant, sandboxes, pen testers).

**Order rationale:** M1 and M2 fix what is wrong or promised; M3–M4
unlock revenue and remove operator tickets; M5–M7 are reliability and
trust; M8–M10 are product and margins; M11–M12 close V4's own test and
launch requirements and gate GA.

## 17. Risks

| Risk | Mitigation |
| --- | --- |
| New hard limits break Free apps on upgrade | Limits apply from the release's first full month; owners over 80% of a limit are emailed before; `429` names the limit and the upgrade |
| MAU filter lets a few extra users in | False positives only err toward allowing; the overshoot is billed nowhere (Free) and bounded |
| Resize restarts a primary | Poolers hold clients (as minor upgrades); HA resizes by switchover; preflight shows the pause |
| Auto-provisioned hosts cost money unexpectedly | Only within the infrastructure budget the admin set (V3 §5.2), proposals recorded, empty nodes cleaned up after 24 h |
| Managed status subscribers feel like spam | Only paying orgs' billing contacts and owners, per component; unsubscribe remembered; toggle per contact |
| Caching serves stale data | Opt-in per table with a TTL the developer chooses; API writes invalidate locally; documented as a staleness budget |
| Branch services leak secrets from the parent | New keys and signing key always; OAuth, SMTP and captcha secrets never copied (tested) |
| Render worker pool adds latency | One pipe hop per render (sub-millisecond) against renders of hundreds of milliseconds; cached renders don't use it |

## 18. Questions for review

1. **Free limits on upgrade (§3):** apply the 500k requests and 10k MAU
   limits immediately, or from the first full month after the release
   (recommended), with a notice?
2. **MAU check (§3.2):** Bloom filter (recommended) or the exact set?
3. **Retired Postgres majors (§6.1):** leave projects running unsupported
   (recommended), or upgrade them automatically at retirement?
4. **Region premium (§4.3):** 25% for Lagos as a starting default in the
   price book, or leave it at 0 until cost attribution sets it?
5. **Edge caching (§10):** build it now (M9), or keep it for when a
   customer asks (the spec's open question 6)?
6. **Managed status subscribers (§7.1):** billing contacts and owners
   (recommended), or owners only?
7. **Maintenance drafts (§8.2):** 96 hours ahead (72 for the SLA plus a
   day to confirm), or another lead time?
8. **Branch file copies (§9.5):** off by default (recommended, it costs
   storage), or on?
