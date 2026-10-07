# PGDock — V3.1 Specification & Build Plan

*Builds on V3. References like "V3 §2.2" point to that document.*

|  |  |
| --- | --- |
| **Status** | Draft v1, for review |
| **Theme** | Make the Lagos region's HA and SLA hold up without a person on call: failure domains, etcd that can heal and live in its region, and maintenance the SLA can exclude honestly |
| **Builds on** | V3 (M17–M27) |
| **Scope** | The three gaps V3 left in its own docs (`docs/ha.md` "Not yet"): items 12–14 of the deferred list |

---

## 1. Overview

### 1.1 Why now

V3 ships HA for dedicated instances and a Lagos region, but three of its
promises still rest on manual work or on luck:

| Gap | Today | Risk |
| --- | --- | --- |
| **Failure domains** (§2) | One Hetzner placement group for the whole platform (`PGDOCK_HETZNER_PLACEMENT_GROUP_ID`); manual nodes have no notion of rack or host. HA standbys, etcd members and pooler hosts only avoid the *same node*. | Two members of an HA pair, or two etcd members, on the same physical host or rack. One power or switch failure takes both, and the SLA with them. |
| **etcd** (§3) | One cluster, set up once, in the home region. A dead member can't be replaced while HA projects use it; Lagos HA projects use etcd in the EU. | A lost etcd node is permanent degradation (one more loss and failover stops). Lagos failover depends on the link to Europe: a cut cable means no automatic failover in Lagos (writes continue thanks to failsafe mode, but nothing takes over a dead primary). |
| **Announced maintenance** (§4) | V3 §2.7 excludes "scheduled maintenance announced 72 hours in advance" from the SLA, but nothing records an announcement, so nothing is excluded. Maintenance-window work (minor upgrades, rebalancing) counts as downtime. | Either the SLA record overstates downtime (credits owed that the terms don't require), or an admin edits the record by hand, which the SLA's credibility can't afford. |

### 1.2 What V3.1 adds

| # | Area | Section |
| --- | --- | --- |
| 1 | Failure domains on every node, enforced for HA pairs, etcd members and pooler hosts; per-region Hetzner placement groups created automatically | §2 |
| 2 | etcd member replacement, and one etcd cluster per region | §3 |
| 3 | Scheduled maintenance: announced on the status page and by email, applied to the SLA record only when announced ≥ 72 hours ahead | §4 |

### 1.3 Not in V3.1

Everything else on the deferred list stays deferred: V4's backend services,
dollar billing, read replicas, SSO/SAML, a Terraform provider, schema diff,
branch data masking, the receivables-by-age report. Growing an etcd cluster
beyond three members (five-member clusters) is also out: three members, one
per failure domain, is the target.

### 1.4 Principles

- **Nothing in V3 breaks.** A V3 install upgraded with nothing configured
  behaves as V3 does: every existing node gets a failure domain of its own,
  the existing etcd cluster becomes the home region's, and no maintenance
  is announced.
- **Refuse rather than pretend.** Where a placement can't satisfy the
  failure-domain rule, PGDock says so and refuses (or, for existing
  placements, warns), as residency does in V3 §6.3.
- **The SLA record is evidence.** Exclusions are derived from records made
  before the event (the announcement), never edited after it.

---

## 2. Failure Domains

### 2.1 Model

Every node has a **failure domain**: a short label naming what fails
together (a physical host, a rack, a power feed). Two nodes in the same
domain are assumed to fail together.

| Provider | Where the domain comes from |
| --- | --- |
| Hetzner | Derived from the server's *spread* placement group (§2.3): servers in the same spread group are guaranteed different physical hosts, so each is its own domain. Servers in different groups might share a host, so PGDock doesn't count them as separated. |
| Manual (colocation, Lagos) | Set by the admin when adding the node (`rack-a`, `lagos-dc1-r3`), editable later. A node without one gets its own ID as its domain, as if alone. |

The domain is a property of the node, shown in Platform → Nodes and on
each node's page, and returned by the nodes API.

### 2.2 The rule

Within a region, members of the same group must sit in **different failure
domains**:

| Group | Rule | When it can't be met |
| --- | --- | --- |
| An HA project's primary and standby | Different domains | Enabling HA is refused, naming the domains available; a switchover or failover never moves a member, so it doesn't apply. |
| A region's etcd members | All three in different domains | Setup and member replacement are refused. |
| A region's two pooler hosts | Different domains | Adding the second host warns, and the region page shows "pooler hosts share a failure domain". (Pooler hosts are added by the admin; PGDock can't move them.) |

Shared projects aren't affected: they have no pair to separate. Drains
and rebalancing don't move HA members (V3 moves them by switchover), so
they have nothing to check yet.

### 2.3 Hetzner placement groups

`PGDOCK_HETZNER_PLACEMENT_GROUP_ID` is replaced by groups PGDock manages:
one spread group per region (`pgdock-<region>`), created on first use and
found by name at the provider; each server's group is recorded on its node. Hetzner caps a spread group at 10 servers; when a group is
full, PGDock opens the next (`…-2`). Because only servers within one
group are known to be apart, the planner keeps a region's HA and etcd
nodes in the same group where it can, and places a pair across groups
only when no single group has room (with a warning).

The old variable, if set, stays the home region's dedicated group, so
existing installs keep their placement.

### 2.4 Checking what exists

A daily check lists every HA pair, etcd cluster and pooler pair that
breaks the rule (after an upgrade, or after an admin edits a domain) and
raises a `failure_domain` warning alert per group. The fix is a move (HA
member: the existing node move; etcd: member replacement, §3.2).

---

## 3. etcd

### 3.1 One cluster per region

Each region with HA projects has its own three-member etcd cluster on
three of its nodes, in three different failure domains. A region's HA
projects use only their region's cluster, so failover in Lagos depends
only on Lagos.

- Admin → Regions → the region → **etcd cluster**: set up (pick three
  nodes), see members and health, replace a member.
- Enabling HA in a region without a healthy etcd cluster is refused with
  a link to set one up.
- On upgrade, the existing cluster becomes the home region's. Existing HA
  projects in other regions (possible in V3) keep using it until moved
  (§3.3), and the region page says so.
- Every cluster uses the one platform etcd CA (as built: per-region CAs
  would add no isolation, since pgdock-server holds every key, and one CA
  keeps Patroni's certificates valid across a move).

### 3.2 Replacing a member

**Replace** on a member (dead or alive) runs an operation that keeps the
cluster's quorum throughout:

1. Preflight: the cluster has a quorum without the member being replaced;
   the new node is in the region, healthy, and in a failure domain not
   used by the remaining members.
2. Remove the old member from the cluster (`member remove`), so the
   cluster is two members with a quorum of two.
3. Add the new member (`member add`), start etcd on the new node with
   `initial-cluster-state=existing`, and wait until it is healthy and
   caught up.
4. (As built: Patroni refreshes the member list from the cluster itself,
   and containers created later get the new list, so running members'
   configuration isn't rewritten.)
5. Stop and remove the old member's container if its node answers;
   record the replacement.

Steps 2–3 are the only time the cluster runs with two members; a failure
there leaves it at two (still with a quorum) and the operation can be
retried. A second member lost during that window is the one thing this
can't survive, and the preflight refuses to start while any other member
is unhealthy.

Replacing a member whose node is gone (the common case) skips step 5.
Replacing a healthy member (to move it off a node being drained) is the
same operation, and a drain of a node holding an etcd member now does it
automatically, to a node in an unused domain.

### 3.3 Moving a region's HA projects onto its own cluster

For HA projects whose region has a new cluster of its own, **Move to the
region's etcd** (per project, from its HA card) is a planned operation (as
built):

1. Remove the standby.
2. Re-create the primary pointed at the new cluster, with the poolers
   holding clients; Patroni initialises the empty scope from the running
   data, as when HA is first enabled.
3. Build a new standby from the newest base backup.

Target pause: under 10 seconds, no lost commits (5–7 s in the test). The
project has no standby until step 3 finishes.

---

## 4. Announced Maintenance

### 4.1 Model

A **maintenance announcement** is a status page incident of severity
`maintenance` with a scheduled start and end, a region (or all regions),
and optionally the projects or nodes it affects. It is created by the
admin (Admin → Incidents → **Schedule maintenance**) or proposed by PGDock
for the weekly maintenance window when work is queued (minor upgrades,
approved rebalancing) and confirmed by the admin.

When it is created, PGDock:

- posts it to the status page as upcoming maintenance;
- emails the owners of affected organisations (all organisations with
  projects in the region, when no projects are named) and status page
  subscribers;
- records `announced_at`, which can't be changed afterwards.

Editing the window after announcing creates a new announcement; the old
one is cancelled (and stays in the record).

### 4.2 The SLA

A minute of an HA project's availability record is **excluded** when it
falls inside an announcement that:

- covers the project (its region, or it, or its node), and
- was announced at least **72 hours** before the minute.

The exclusion is applied when the minute is recorded and re-derived when
the month is closed for SLA credits, from the announcements as they stood;
an announcement made less than 72 hours ahead is shown with a warning in
the admin form ("won't be excluded from the SLA before <time>") but is
still posted. Maintenance run outside any announcement isn't excluded.

The project's HA card and the SLA report show excluded minutes separately
("4 min excluded, announced maintenance on 12 Oct") so a customer can see
what was excluded and why.

### 4.3 Doing maintenance inside the window

The weekly maintenance window (V3 §2.4) only starts disruptive work
(minor-upgrade recreations, rebalancing moves, switchovers for node
maintenance) for projects covered by an announcement for that window, when
announcements are required (`PGDOCK_MAINTENANCE_REQUIRE_ANNOUNCEMENT`,
default on for regions offering the SLA). Without one, the work waits for
the next announced window.

---

## 5. Data Model Changes

```sql
-- §2 Failure domains
ALTER TABLE nodes ADD COLUMN failure_domain text;      -- null: the node's own
ALTER TABLE nodes ADD COLUMN placement_group text;     -- Hetzner group id

-- §3 etcd per region
ALTER TABLE etcd_members ADD COLUMN region text REFERENCES regions(id);
                                                       -- existing rows: home region
ALTER TABLE instances ADD COLUMN etcd_region text;     -- the cluster an HA instance uses
-- (As built: no per-region CA table, and replacements are recorded as
-- operations.)

-- §4 Announced maintenance
ALTER TABLE incidents
  ADD COLUMN scheduled_start timestamptz,
  ADD COLUMN scheduled_end   timestamptz,
  ADD COLUMN announced_at    timestamptz,
  ADD COLUMN cancelled_at    timestamptz,
  ADD COLUMN replaces        uuid REFERENCES incidents(id);
CREATE TABLE incident_scope (
  incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
  project_id  uuid,
  node_id     uuid,
  CHECK (project_id IS NOT NULL OR node_id IS NOT NULL)
);
ALTER TABLE availability_minutes ADD COLUMN excluded_by uuid REFERENCES incidents(id);
```

## 6. API Additions

| Method and path | What |
| --- | --- |
| `PATCH /api/v1/nodes/{id}` | Gains `failure_domain`. |
| `GET /api/v1/admin/failure-domains` | Groups that break the rule (§2.4). |
| `GET /api/v1/admin/etcd?region=…`, `POST /api/v1/admin/etcd` | A region's cluster; set up (as built, on the existing paths). |
| `POST /api/v1/admin/etcd/members/{node_id}/replace` | Replace a member (`{"node_id": …}`), an operation. |
| `POST /api/v1/projects/{id}/ha/etcd-move` | Move an HA project onto its region's cluster (§3.3). |
| `POST /api/v1/admin/maintenance` | Announce (`start`, `end`, `region`, optional `projects`/`nodes`, `title`, `body`). |
| `DELETE /api/v1/admin/maintenance/{id}` | Cancel. |
| `GET /api/v1/projects/{id}/ha` | `availability` gains `excluded_minutes` and the announcements behind them. |

`GET/POST /api/v1/admin/etcd` stays as the home region's, for the CLI and
scripts written against V3.

## 7. Web UI Additions

- **Platform → Nodes:** a Failure domain column and field; a banner when
  any group breaks the rule.
- **Platform → Regions → region:** etcd cluster (members, health, set up,
  replace), pooler pair's domains, HA projects still on another region's
  etcd with **Move all**.
- **Admin → Incidents → Schedule maintenance:** window, region, scope,
  the 72-hour warning, preview of the email.
- **Project → HA card:** excluded minutes and the announcement links.

## 8. Build Plan

Same rules as V3 (`docs/v3-plan.md`): one milestone at a time, each
finished with its done-when test, docs, decisions and CI green.

**Numbering.** V3.1's milestones are numbered `V3.1-M1` to `V3.1-M3`, not
after V3's M27: M28 onwards stays free for V4, which begins where V3
stops. Any later point release does the same (`V3.2-M1`, …), and its
decisions go under its own heading in `docs/decisions.md`.

### V3.1-M1 — Failure domains (Week 1)

Node failure domains (data model, API, UI, manual entry), per-region
Hetzner placement groups replacing the global variable, the rule enforced
for enabling HA, etcd setup, pooler hosts and drain/rebalance targets, the
daily check and `failure_domain` alert. **Done when:** with three Lagos
nodes in two racks, enabling HA puts the standby in the other rack and is
refused when only same-rack nodes are free; moving a node into its
pair's rack raises the alert; capacity provisioning in a test region
creates a spread group and records it on each server (fake Hetzner).

### V3.1-M2 — etcd per region and member replacement (Weeks 2–3)

Per-region clusters and CAs, the home cluster migrated, HA enable and
Patroni configuration by region, member replacement (dead and alive), drain
of an etcd node, moving HA projects onto their region's cluster. **Done
when:** in the Docker HA environment, a Lagos cluster serves a Lagos HA
project with a writer running; one member's container and volume are
destroyed; **Replace** onto a fourth node brings the cluster back to three
healthy members with no client errors and no failover; a switchover after
the replacement still works; a drain moves an etcd member; an HA project on the home cluster moves to
the Lagos cluster with under 10 seconds of paused writes and no lost
commits.

### V3.1-M3 — Announced maintenance and the SLA (Week 4)

Announcements (incidents with schedule, scope and `announced_at`),
emails, the status page's upcoming maintenance, SLA exclusion with the
72-hour rule, the maintenance window requiring announcements, the HA
card and SLA report. **Done when:** with the clock moved, a maintenance
window announced 73 hours ahead excludes a switchover's unavailable
minutes for the projects in scope and not for others; one announced 71
hours ahead excludes nothing; work queued for an unannounced window
waits; the month's SLA report lists excluded minutes with their
announcement.

### Timeline summary

| Week | Milestone | Outcome |
| --- | --- | --- |
| 1 | V3.1-M1 Failure domains | HA pairs and etcd can't share a rack or host |
| 2–3 | V3.1-M2 etcd per region | Lagos failover depends only on Lagos; a dead etcd node is a routine repair |
| 4 | V3.1-M3 Announced maintenance | The SLA record matches the terms |

## 9. Risks

| Risk | Mitigation |
| --- | --- |
| Member replacement goes wrong mid-way | Preflight refuses unless the other two are healthy; the cluster keeps a quorum of two throughout; each step is idempotent and the operation can be retried; Patroni's failsafe mode (M27) keeps primaries writing even if it does lose quorum. |
| Moving HA projects between etcd clusters | Planned operation with poolers paused, Patroni paused rather than stopped on the primary, rollback before the switch; tested with a live writer. |
| Wrong failure domains entered for manual nodes | Shown prominently, checked daily, and the Lagos launch rehearsal (pull a rack's power, or simulate it) verifies the labels against reality. |
| Hetzner's 10-server spread group limit | Groups opened per region and role as they fill; the planner keeps HA pairs in one group where it can. |
| Customers disputing exclusions | Announcements are immutable once made, emailed, posted publicly, and listed against the excluded minutes. |

## 10. Open Questions

1. **Lagos failure domains.** How many racks (or power feeds) will the
   Lagos colocation have at launch? Three domains are needed for a Lagos
   etcd cluster; with two, the third member must sit elsewhere (another
   Lagos facility, or the EU, which brings back the dependency §3 removes).
2. ~~**Branching.**~~ Answered: V3.1 lands on `feature/pgdock3`, as V3 did.
3. **Maintenance announcements by default.** Should the weekly window
   propose an announcement automatically 4 days ahead whenever work is
   queued (admin confirms), or should admins always create them by hand?
4. **Emails.** Should maintenance emails go to every org with projects in
   the region, or only to orgs with HA projects (the ones the SLA covers)?
