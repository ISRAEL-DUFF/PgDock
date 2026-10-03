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

- **Dedicated instances**: `install.sh` rebuilds `pgdock-postgres:18-walg3.0.9`
  from the latest `postgres:18`. Then, per project, Overview → Instance →
  **Restart**: the instance is recreated from the new image on the same
  volume (about the time of a normal restart). Do it one project at a time
  in a quiet moment.
- **The shared cluster**: pull the new image and recreate it in a
  maintenance window; every shared project is unavailable for the restart
  (usually under a minute):

  ```sh
  docker compose pull shared-pg
  docker compose up -d shared-pg
  ```

## PostgreSQL major versions

V1 runs PostgreSQL 18 everywhere. Major upgrades arrive in V1.1, using the
promotion machinery to move a project into an instance on a newer version.

## Rolling back

Check out the previous tag and run `./install.sh` again. If the new
version ran a metadata migration the old one does not understand, the old
server refuses to start; restore the metadata database from the
self-backup taken before the upgrade (see
[disaster recovery](disaster-recovery.md), step 2) and start the old
version.
