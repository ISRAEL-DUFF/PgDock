# Regions and data residency

PGDock V3 runs projects in more than one region (V3 §6). The control
plane (pgdock-server and its metadata database) stays in one place, the
**home region** (`PGDOCK_REGION`, default `eu-central`). A region groups:

- **nodes** (`nodes.region`), on Hetzner or on machines you register by
  hand (colocation, or a local provider);
- a **pooler pair** with its own hostname, the one in its projects'
  connection strings;
- a **storage target** for its projects' backups, and a **copy target**
  for cross-region copies of them.

Regions are managed under **Platform → Regions** (or
`PUT /api/v1/admin/regions/{id}`).

## Adding a region

1. **Platform → Regions → Add region.** Give it an ID (`ng-lagos`), a
   name, its two-letter country, and the pooler hostname
   (`db.ng.example.com`). Pick **Manual** for colocated machines or a
   local provider.
2. **Storage.** Add an S3-compatible bucket in the region as a platform
   storage target (Settings → Platform storage targets) and choose it as the
   region's **backup target**. Choose a **copy target** in another region
   or provider for cross-region copies (below).
3. **Nodes.** Record each node's [failure domain](failure-domains.md)
   (its rack) as you add it. HA standbys, etcd members and pooler hosts
   are kept in different ones. Add the region's nodes (`POST /api/v1/nodes` with
   `"region": "ng-lagos"`, or Platform → Nodes) and start their agents.
   Give one a shared cluster if the region offers the shared tier.
4. **Poolers.** Add two pooler hosts in the region (see
   [edge poolers](edge-poolers.md)), and set the region's floating IP ID.
   Point the region's hostname at that floating IP. Until a region has
   pooler hosts of its own, the home region's poolers serve its projects
   (Platform → Regions says "served by the home poolers").
5. **Data residency** (optional): tick **Offer data residency**. It needs
   the country and an in-country backup target.

Hidden regions take no new projects; existing ones keep running.

## Projects and regions

- **At creation** a project chooses its region (New project → Region,
  `pgdock projects create --region ng-lagos`, or `region` in
  `POST /api/v1/projects`). The default is the home region. Its node, its
  HA standby, its backups and its branches are all in that region.
  Restores into a new project stay in the source's region.
- **Promotions, demotions, upgrades and HA** stay in the project's region.
- **Moving regions** is a zero-downtime move to a node in the other region
  (Project Settings → Compute → Moves, platform admins). At the cutover the
  project's region changes and so does its hostname. **The old hostname
  keeps routing for 30 days**: the old region's poolers keep a forward to
  it, and Project Settings shows when it ends. Tell the customer to update
  their connection strings within that time.

Each region's pooler hosts get their own configuration: their projects'
routes and logins, plus those forwarded to them, with their own
generation and floating IP in the arbiter.

## Data residency

A project in a region that offers it can turn on **Data must stay in
<country>** (V3 §6.3), at creation or later under Project Settings →
General → Region. Turning it on or off is for **organisation owners**,
needs a fresh step-up (password and code), and is audited. With it on:

- **Backups go only to targets in the region**: the region's own target,
  or a target the platform admin marked as being there. Pointing the
  project at any other target is refused, and so is a backup when the
  region has no in-country target.
- **No cross-region copies.** Copies to a copy target outside the region
  are skipped; turning residency on deletes existing ones.
- **The project can't be moved out of the region**; turn residency off
  first.
- **Branches** are created in the parent's region.
- **Exports** (backup downloads by owners) stay available: the customer
  decides where their own copy goes.

What it guarantees: PGDock stores the project's data, WAL archive and
backups only on machines and buckets in the region. Whether a regulator
requires in-country hosting is the customer's determination. A residency
region should have its own pooler hosts, or client connections pass
through the home region's poolers.

## Cross-region backup copies

Every backup on a platform storage target is copied to its region's copy
target, asynchronously (every 5 minutes, 20 at a time), and verified by
checksum on the way and read back (V3 §2.5). The copy keeps the object
key. Failures are retried after an hour. Retention deletes the copy with
the original.

- Org (bring-your-own) targets aren't copied; that's the organisation's
  responsibility.
- Residency projects are skipped unless the copy target is in their
  region.
- WAL-G base backups and WAL archives aren't copied object by object; use
  the copy bucket's own replication for them if you need it.
- **The weekly restore test alternates** between a primary backup and a
  copy. Its log says which ("from its cross-region copy").

Platform → Regions shows the last 7 days' copy status (copied, pending,
failed, skipped).
