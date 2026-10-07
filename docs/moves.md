# Moves and Postgres versions

PGDock moves a project's database between Postgres instances in four
situations: a **promotion** (shared → dedicated), a **demotion**
(dedicated → shared), a **node move** (the platform admin puts a project on
another node), and a **major upgrade** (to an instance of a newer Postgres
version). All four use the same engine, which copies the data by
**logical replication** while the project keeps serving (V3 §2.3, §2.4).
Writes pause only for the switch. Minor releases are applied
separately, in a weekly **maintenance window**.

## How a move works

1. **Preflight.** PGDock checks that logical replication can be used: the
   source has `wal_level=logical`, there are free replication slots and WAL
   senders, the target runs the same or a newer major, no large objects or
   unlogged tables, and every extension exists on the target. Tables with no
   primary key get `REPLICA IDENTITY FULL` for the move, so their updates
   and deletes replicate too.
2. **Schema.** The schema is copied first, keeping its owners. From here
   until the move ends, **schema changes are refused** on the project
   (an event trigger); data changes carry on.
3. **Copy and stream.** A publication on the source and a subscription on
   the target copy every table, then stream changes. Progress (tables
   copied, bytes behind) shows under Project Settings → Compute → Moves, in
   `pgdock moves <p>`, and in the operation log.
4. **Switch.** Once every table is copied and the target is less than 1 MB
   behind for a few seconds, writes freeze: the project's roles can't log in,
   the poolers hold clients, and remaining sessions end. A marker row
   written on the source is waited for on the target, which proves the
   target has every commit. Sequences are copied with a margin of 1,000, and
   row counts of small tables (up to 16 MB each) are compared.
5. **Route.** The pooler route switches to the target, and clients waiting
   in the poolers continue there with the same URL and password. The
   subscription, slot, publication and DDL block are removed.

Clients on the **pooled URL** wait during the pause rather than fail.
Session-mode clients are disconnected once and reconnect. In the tests the
pause is about 0.1 to 0.5 seconds, and stays there as the database grows:
a dedicated project moved nodes with a client committing continuously in
28 s at 1 GB (writes paused 115 ms) and 67 s at 3 GB (135 ms), with no
acknowledged commit lost.

### When logical replication can't be used

The engine falls back to a **dump and restore during the freeze** when a
preflight check fails (large objects, unlogged tables, no free replication
slot, `wal_level` not logical). Then writes pause for the whole copy, about
a minute per few hundred MB. The preflight and the move record say which
mode was used and why (`fallback_reason`).

### What waits during a move

- **Webhooks** wait and deliver once the project is active again; no event
  is lost.
- **Scheduled jobs** whose time falls during the move are **skipped**, and
  the run history says why.
- **Schema changes** are refused (see step 2). Run migrations before or
  after.
- Backups of a moving project wait for the next schedule.

### After the move

- A shared copy left behind (a promotion or a shared node move) is kept
  **read-only for 48 hours**, then dropped.
- A dedicated instance left behind (a demotion, a dedicated node move, a
  dedicated upgrade) is **stopped and kept for 48 hours**, then destroyed.
- A dedicated target takes its first base backup right away. Point-in-time
  recovery on the new instance starts from that backup.

A failure before the switch leaves the project where it was, with no data
lost. The target copy is dropped and the project returns to `active`.

## Network access

During a move, the target connects to the source as the move's own login
(`pgdock_move_<id>`, with a random password, removed when the move ends).
Every other database login stays limited to the control plane and the
poolers. The agents let move logins in from `PGDOCK_AGENT_MOVE_ALLOW`:
by default the private ranges (10/8, 172.16/12, 192.168/16), and in the
bundle its Docker network. Each agent adds the rule to an instance's
`pg_hba.conf` when it starts the instance, and to every running instance
when the agent itself starts. If your nodes reach each other
on other addresses, set `PGDOCK_AGENT_MOVE_ALLOW` on every agent to the
nodes' private network.

## Node moves

Platform admins can move a project to another node from Project Settings →
Compute → Moves, `pgdock move <p> --node <id>`, or
`POST /api/v1/admin/projects/{id}/move`. A shared project moves into the
target node's shared cluster, which must run the same Postgres version. A
dedicated project gets a new instance of the same size there.

## Postgres versions

`PGDOCK_PG_VERSIONS` (default `17,18`) lists the majors PGDock offers; the
newest is the default for new projects. Each needs its image on every node
(`pgdock-postgres:{major}-walg3.0.9`; `install.sh` and `make pg-image` build
17 and 18). Shared projects go to a shared cluster of their version, so add
one per version you offer (Nodes → a node → **Run a shared cluster here**,
choosing the version).

### Major upgrades

Project Settings → Compute → **Postgres version → Upgrade…** (or
`pgdock upgrade <p> --to 18`). The preflight:

- checks the version is newer and supported, and where the project would
  go: the least-loaded shared cluster of the new version, or a new
  dedicated instance of the same size on the same node;
- for a shared target, **restores the schema into a scratch database on
  the new version** as an ordinary user, and reports anything that fails
  (removed functions, changed syntax, missing extensions). Nothing is
  changed;
- estimates the pause, and says whether logical replication can be used.

The upgrade is then a move as above. Test the application against the new
version first, for example on a branch: a new major can change query plans
and defaults.

### Minor upgrades and the maintenance window

Minor releases (18.1 → 18.2) need no data change, only a restart on the
new binaries. Rebuild or pull the images on each node (`install.sh` does it,
or `make pg-image`). Each agent then reports which release every instance runs
and which its image tag holds, and Nodes → a node shows "18.2 available".

In the **maintenance window** (Admin → Nodes → Maintenance window; default
Sunday 02:00–06:00 UTC), PGDock restarts the instances that are behind,
**one at a time**: dedicated instances first, then shared clusters. For
each restart the poolers hold the projects' clients, so the restart (about
a second or two) looks like a short pause rather than an outage. An
instance whose projects are in the middle of something (a move, a restore,
a queued operation) waits for the next sweep. The window can be disabled,
and **Upgrade now** restarts one instance at once. The panel lists the
instances still behind and the last 20 restarts, with how long clients were
held.

`GET /api/v1/admin/maintenance`, `PUT /api/v1/admin/maintenance/window` and
`POST /api/v1/admin/instances/{id}/minor-upgrade` do the same over the API.

## Checking the 20 GB target

M18 is done when a 20 GB project under continuous writes moves nodes with
under 5 seconds of paused writes and zero lost commits. CI runs that test
(`TestMoveUnderLoad`) at 1 GB. To run it at full size:

```sh
make test-move                         # 20 GB
make test-move PGDOCK_TEST_MOVE_GB=5   # another size
```

It needs about three times the size in free disk (the source, the target,
and the WAL), and takes a few minutes per 10 GB on an SSD. It fills eight
tables, starts a client that commits continuously, moves the project to a
second node, and checks the pause, every acknowledged commit, and every
row of the bulk tables.
