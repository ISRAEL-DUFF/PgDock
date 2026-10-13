# Upgrading from V2 to V4.1

This runbook takes an install running V2 (`v2.0.0` or `v2.1.0`) to the
latest release in one step. The server applies every migration from 00020
to 00050 on its first start, so the upgrade itself is the usual
`./install.sh`. The work is in deciding a few settings beforehand: V3 adds
billing and a Free tier that pauses idle projects, and both apply to every
existing organisation the moment the new server starts.

[Upgrading](upgrade.md) explains each version's changes. This page puts
them in order.

## 1. Before the day

### Cut the release

The release is a tag on `main`: merge the release branch into `main`, add
the version's heading to [CHANGELOG.md](../CHANGELOG.md) (everything under
*Unreleased* since `v2.1.0`), and tag it (`git tag v4.1.0`). CI on the tag
must be green: unit and integration tests, e2e, the docs install test.

### Decide what customers see on the first day

Write these down; you will set them before the upgrade.

| Question | Why it matters | Where |
| --- | --- | --- |
| Which organisations pay, and on which plan? | The billing migration gives every organisation a billing account on the **Free** plan. Free projects with no client connections for 7 days are **paused**, and archived after 90 days paused. Paid plans are never paused, and neither are projects of an organisation on the **Unlimited** quota plan (your own platform admin's organisation, where your own projects live). | Each organisation's owner chooses its plan (Org → Billing → **Change plan…**) after the upgrade; until they have, keep the Free tier's clocks long (below). |
| How long before idle Free projects pause? | The activity clock of every project starts at the upgrade, so the first pauses come 7 days later unless you change it. | `XENIBASE_FREE_PAUSE_AFTER` (e.g. `8760h` while you move customers to their plans), `XENIBASE_FREE_ARCHIVE_AFTER`. See [the Free tier](free-tier.md). |
| What are the real prices? | The first price book has placeholder prices. Nothing is invoiced until you turn on automatic issue. | Admin → Billing → Price books, then Settings. See [billing](billing.md). |
| Which region is this? | Every existing node is put in `XENIBASE_REGION` (default `eu-central`). | `.env` before the upgrade. See [regions](regions.md). |
| Should HA minor upgrades wait for an announced window? | From V3.1 they do, by default, and announcements email organisation owners. | `XENIBASE_MAINTENANCE_REQUIRE_ANNOUNCEMENT=false` keeps V2's behaviour. See [HA](ha.md). |
| Are the legal documents ready? | On first start the server publishes template SLA and DPA documents and adds an acceptable use policy to the terms. | Review them before customers sign in: [legal documents](legal.md). |
| Who gets query insights? | Shared projects get them only on the plans in `XENIBASE_INSIGHTS_PLANS` (default `pro,team`). | `.env`; `all` without billing. |

Payments, support email and WhatsApp, the status page, cloud capacity and
backend services all stay off until their settings are set. They can wait
until after the upgrade.

### Rehearse on a copy

Do the whole upgrade once on a staging server built from production's
metadata.

**Isolate it first.** The restored metadata holds production's agents
(and the credentials to command them), the SMTP settings, customers'
email addresses and webhook URLs. Firewall the staging server so it can
reach nothing outside itself: no production nodes or poolers, no SMTP, no
payment providers. Allow only your own access to its dashboard.

1. Install `v2.1.0` on a staging server ([install](install.md)), with its
   own domain.
2. Restore last night's metadata self-backup into it, as in
   [disaster recovery](disaster-recovery.md#2-restore-the-metadata).
   Project data isn't needed: the migrations only touch Xenibase's own
   database. Operations that need the projects' servers (applying
   settings, backups) fail on staging; that is expected.
3. Set the `.env` values you chose above, then upgrade as in section 3.
4. Check: the server starts and `xenibase-server -version` shows the new
   version; the server log shows no failed migration; Admin →
   Organisations lists every organisation with its billing account;
   Admin → Billing → Price books has the first book.
5. Throw the staging server away afterwards: it holds production's
   secrets.

Note how long the server took from start to serving: that is your
downtime estimate for the dashboard and API. Clients connected to their
databases through the poolers reconnect in a second or two.

## 2. On the day, before upgrading

- [ ] Announce a window to customers: the dashboard and API are down for
      a few minutes; database connections blip once.
- [ ] Check last night's metadata self-backup succeeded (Settings → Backup
      checks), and take one now if it is old. It is your way back.
- [ ] Keep `deploy/compose/.env` and the backup key off the server.
- [ ] Add the settings you decided to `deploy/compose/.env`, for example:

      XENIBASE_REGION=eu-central
      XENIBASE_FREE_PAUSE_AFTER=8760h
      XENIBASE_MAINTENANCE_REQUIRE_ANNOUNCEMENT=false

- [ ] **Remote nodes first.** Agents must run the same major version as
      xenibase-server and a minor at least as new. On each node, replace
      the agent image or binary with the new release's and restart it;
      it keeps its identity ([remote nodes](upgrade.md#remote-nodes)).
      Then rebuild or pull the `xenibase-postgres` images there
      (`make pg-image`): V3's HA needs Patroni in them, and query insights
      need hypopg.

## 3. Upgrade the control node

The safest way is `upgrade.sh`, which backs up the metadata database, checks
the result and rolls back if it fails ([deploying](deploying.md); the first time,
take the script from the new tag as described there):

```sh
XENIBASE_DIR=$PWD sh /tmp/upgrade.sh v4.1.0     # after: git show v4.1.0:deploy/compose/upgrade.sh > /tmp/upgrade.sh
```

By hand, it is:

```sh
cd xenibase
git fetch --tags
git checkout v4.1.0
cd deploy/compose
./install.sh
```

The server runs migrations 00020 to 00050 before it serves; a failure
leaves the old schema and the server refuses to start (see
[rolling back](#rolling-back)).

## 4. Right after

- [ ] `docker compose exec xenibase-server xenibase-server -version` shows
      the new version.
- [ ] Nodes shows every agent healthy, at the new version.
- [ ] Operations shows nothing failed. Migrations queue some background
      operations; let them finish.
- [ ] Recreate the bundle's shared cluster once, so its projects can move
      by logical replication (it now runs `wal_level=logical`):
      `docker compose up -d shared-pg`. Connections to shared projects
      drop for a few seconds; do it inside the window.
- [ ] Open a shared and a dedicated project: connect through the poolers,
      run the SQL console, check backups are scheduled.
- [ ] Admin → Billing: set real prices (publish a new price book) and your
      company's details before turning on automatic issue.
- [ ] Ask paying customers' owners to choose their plan (Org → Billing →
      **Change plan…**). Once they have, set `XENIBASE_FREE_PAUSE_AFTER`
      back to what you want (or remove it for the default 7 days) and run
      `./install.sh` again. Free organisations' owners are warned a day
      before a project is paused.

## 5. Turn on what you need, when you need it

Each of these is independent; none is needed for V2's features to keep
working.

| Feature | What to set | Guide |
| --- | --- | --- |
| Backend services (data API, auth, storage, realtime) | `XENIBASE_API_DOMAIN`, `XENIBASE_EDGE_SECRET` in `.env`; xenibase-edge in each region with a wildcard DNS record and certificate for `*.<domain>`; a backup storage target in each region (files live there) | [Running xenibase-edge](backend-services.md#running-xenibase-edge) |
| Platform email and SMS for apps' sign-in | The platform SMTP; Termii and WhatsApp template variables | [Platform SMS and WhatsApp](backend-services.md#platform-sms-and-whatsapp-operators) |
| Payments (organisations' wallets, funded by bank transfer) | `XENIBASE_ISWALLET_*`, and the provider's webhook URL | [Payments](payments.md) |
| Open signup | `XENIBASE_TURNSTILE_SITE_KEY`, `XENIBASE_TURNSTILE_SECRET` | [The Free tier](free-tier.md) |
| Support inbox and WhatsApp | `XENIBASE_SUPPORT_*`, `XENIBASE_WHATSAPP_*` | [Support](support.md) |
| Status page | `xenibase-status` on other infrastructure; `XENIBASE_STATUS_URL`, `XENIBASE_STATUS_PUSH_SECRET` | [Status page](status-page.md) |
| HA | An etcd cluster per region (Platform → Nodes) | [HA](ha.md) |
| Standby pooler | A second pooler host and a floating IP | [Edge poolers](edge-poolers.md) |
| Buying servers | `XENIBASE_CLOUD_PROVIDER=hetzner` and its variables, a monthly budget | [Capacity](capacity.md) |

After changing `.env`, run `./install.sh` again; it restarts what changed.

Backend services' general availability waits on the gates in
[the backend runbook](backend-runbook.md#ga-gates). You can turn them on
for chosen projects before then.

## Rolling back

Take the metadata self-backup before you upgrade (section 2): it is the way
back.

**Before the new server has started**, rolling back is checking out
`v2.1.0` and running `./install.sh`.

**After it has run its migrations, do not just check out `v2.1.0`.** `v2.1.0`
does not notice that the database is at a newer schema: it starts, and runs on
tables it doesn't know, which can write wrong data without any error
(rehearsal on a copy of a v2.1.0 database showed it starting and processing
queued operations against the V4 schema). Instead:

1. Stop xenibase-server (`docker compose stop xenibase-server`).
2. Restore the metadata database from the self-backup, as in
   [disaster recovery](disaster-recovery.md#2-restore-the-metadata).
3. Check out `v2.1.0` and run `./install.sh`.

Anything done in the dashboard since the upgrade is lost; project data is
not affected (it lives in the projects' databases).

From V4.1 on, a server refuses to start on a database that a newer release
migrated (`the metadata database was migrated by a newer Xenibase than this
one`), so rolling forward-then-back between V4.1 and later releases fails
loudly instead. `v2.1.0` and earlier don't have that check, which is why the
restore is needed here.
