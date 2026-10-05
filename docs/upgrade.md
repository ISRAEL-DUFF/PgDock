# Upgrading

PGDock is versioned as one unit: server, agent, UI, and the bundle's
configuration ship together under one tag (`v1.2.3`). Within a major
version an upgrade is "check out the new tag and run the installer".

## Before you start

- Read the release notes in [CHANGELOG.md](../CHANGELOG.md) for the tag.
- Check the last nightly metadata self-backup succeeded (Settings → Backup
  checks). It is your way back if anything goes wrong.
- Keep your copy of `deploy/compose/.env` and the backup key at hand.

## Upgrade the bundle (the control node)

```sh
cd pgdock
git fetch --tags
git checkout v1.2.3            # the new release
cd deploy/compose
./install.sh                   # keeps .env; rebuilds images; restarts what changed
```

What happens:

1. The images are rebuilt from the new tag.
2. `docker compose up` replaces the containers whose image or configuration
   changed. pgdock-server runs the metadata migrations (goose) on start,
   before it serves anything; a migration that fails leaves the old schema
   in place and the server refuses to start, so the old version can be
   started again.
3. The installer recreates the poolers so they read this version's
   configuration (their config files are bind-mounted, and a checkout
   replaces them, which compose doesn't notice). Connected clients
   reconnect (a second or two).
4. The bundled agent restarts with the new version. Running operations are
   resumed by the new server: every step is idempotent.
5. Migrations may queue one-off `apply_settings` operations (for example,
   00018 gives older shared projects their `temp_file_limit`); they finish
   in the background and show on Operations.

Check afterwards: `docker compose exec pgdock-server
pgdock-server -version` shows the new version, Nodes shows every agent
healthy, and Operations shows nothing failed.

## Remote nodes

Agents on other nodes must run the same **major** version as pgdock-server
and a minor version at least as new: upgrade the agents first, then the
server. pgdock-server sends no work to an agent of another major version or
an older minor (an older agent doesn't know the new work) and shows it on
the Nodes page; a newer minor or any patch version is fine. Replace the agent binary (or image) on each node and restart it; its
state directory keeps its identity, so it needs no new registration.

## PostgreSQL minor versions

Rebuild the images (`./install.sh` does it; on remote nodes `make
pg-image`, or pull them). Each instance then shows "18.x available" on its
node's page, and PGDock restarts the instances that are behind, one at a
time, in the weekly maintenance window (Admin → Nodes; default Sunday
02:00–06:00 UTC). The poolers hold clients for each restart. **Upgrade now**
on the same panel does one immediately. See [moves and Postgres
versions](moves.md#minor-upgrades-and-the-maintenance-window).

The shared cluster from `compose.yaml` has no agent, so it is not part of
the sweep. Recreate it yourself in a quiet moment; shared projects on it
are unavailable for the restart (usually under a minute):

```sh
docker compose pull shared-pg
docker compose up -d shared-pg
```

## PostgreSQL major versions

PGDock offers the majors in `PGDOCK_PG_VERSIONS` (default 17 and 18).
Projects move to a newer one with a major upgrade (Project Settings →
Compute → Postgres version), a logical-replication move that pauses writes
for a few seconds. See [moves and Postgres versions](moves.md#major-upgrades).

## Upgrading to V3

- **Shared clusters restart once.** Every Postgres instance now runs
  `wal_level=logical`, so moves can copy from it. Agent-run instances pick
  it up the next time they are recreated (a restart from the UI, or the
  maintenance window). Recreate the compose shared cluster after upgrading
  (`docker compose up -d shared-pg`) so that shared projects can move by
  logical replication. Until then they move by dump and restore.
- **pg_hba for moves.** The bundle's `shared-pg-hba.conf` gains one rule
  for move logins from its Docker network, and agents add a similar rule
  to every running instance when they start, and to each instance they
  start later (`PGDOCK_AGENT_MOVE_ALLOW`, default the private ranges).
  Upgrade and restart the agents on remote nodes before moving projects
  there. A move whose target can't log in fails at its first step and rolls
  back, changing nothing.
- **Moves pause webhooks and skip jobs.** While a project is moving or
  upgrading, webhook deliveries wait and scheduled job runs are skipped, as
  during a promotion in V2.
- New migrations 00022 (moves, the `moving` and `upgrading` statuses),
  00023 (Postgres releases, minor-upgrade history), 00024 and 00025 (HA:
  etcd members, HA members, failover history, SLA probes and
  availability).
- **HA** needs the new `pgdock-postgres` images (they now carry Patroni;
  `./install.sh` and `make pg-image` build them) on every node, the agents
  of V3, and the etcd cluster set up once (Admin → Nodes). Agents pull
  `gcr.io/etcd-development/etcd:v3.6.5` when asked to run a member; open
  ports 2379–2380 between the three etcd nodes. See [HA](ha.md).

## Rolling back

Check out the previous tag and run `./install.sh` again. If the new
version ran a metadata migration the old one does not understand, the old
server refuses to start; restore the metadata database from the
self-backup taken before the upgrade (see
[disaster recovery](disaster-recovery.md), step 2) and start the old
version.
