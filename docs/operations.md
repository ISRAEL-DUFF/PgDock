# Operating PGDock

Day-to-day running of a PGDock install. Commands run in `deploy/compose`.

## Health at a glance

- **Alerts** (sidebar) lists what is wrong now; the same alerts go to your
  webhook and email (Settings → Alerts). Each is sent when it fires and when
  it resolves:

  | Alert | Severity | Fires when |
  | --- | --- | --- |
  | Backup failed | critical | A project's (or the metadata database's) latest backup failed |
  | Backup overdue | warning | A project older than 26 h has no successful backup in 26 h |
  | Restore test failed | critical | The latest weekly restore test failed |
  | Node disk | warning | A node's data disk is over 85% full |
  | Node unreachable | critical | A node's agent has not answered for 2+ minutes |
  | Project disk | warning | A project is larger than its disk warning (Settings → Guardrails) |
  | Pooler down | critical | A PgBouncer's admin console does not answer |
  | Isolation check | critical | The weekly tenant-isolation check found a problem |

- **Operations** shows every long action with its step log.
- **Metrics** (per project and per node) charts size, connections, TPS,
  cache hit ratio, CPU, memory, disk, and I/O; `/metrics` serves them to
  Prometheus.
- `docker compose ps` and `docker compose logs <service>` for the
  containers themselves.

## Backups

- Logical backups of every project run nightly (02:00 UTC plus up to 2 h of
  jitter); dedicated instances also take daily base backups and archive WAL
  continuously (point-in-time recovery for 7 days).
- Retention: 7 daily backups and 4 weekly ones; final backups of deleted
  projects are kept 30 days.
- Weekly, a random project's latest backup is restored into a scratch
  database and verified (Settings → Backup checks).
- The metadata database backs itself up nightly to `<prefix>/metadata/`.
- Restores: a project's Backups tab → **Restore**, into a new project or in
  place (in place takes a safety backup first and needs re-authentication).

## Security checks

Every shared cluster is checked weekly against the tenant-isolation
checklist (Settings → Tenant isolation, **Check now** to run it at once):
cluster settings and `pg_hba.conf`, every project's role and database, and
two throwaway tenants that try to reach each other. See
[security review](security-review.md).

## Capacity

- The shared cluster's tuning is in `compose.yaml` (`SHARED_PG_SHARED_BUFFERS`,
  `SHARED_PG_EFFECTIVE_CACHE_SIZE` in `.env` for other host sizes). Each
  shared project gets a pool of 5 server connections in transaction mode and
  up to 20 backends; the load test ([load test](load-test.md)) ran 150
  projects with ten busy ones on 4 vCPU.
- When a project outgrows the shared tier (sustained load, a large
  database), **promote** it (Settings → Promote to dedicated): same URL,
  a short write freeze.
- Add nodes on the Nodes page; new shared projects go to the least loaded
  shared cluster, dedicated instances to the least loaded dedicated node.

## Secrets

- `.env` holds `PGDOCK_MASTER_KEY`; keep a copy off the server.
- **Rotate the master key**: generate a new one, run the rotation, then
  switch over:

  ```sh
  new=$(docker compose exec -T pgdock-server pgdock-server -gen-master-key)
  old=$(grep '^PGDOCK_MASTER_KEY=' .env | cut -d= -f2-)
  docker compose stop pgdock-server
  docker compose run --rm -e PGDOCK_MASTER_KEY="$new" -e PGDOCK_MASTER_KEY_PREVIOUS="$old" \
    pgdock-server -rotate-master-key
  sed -i "s|^PGDOCK_MASTER_KEY=.*|PGDOCK_MASTER_KEY=$new|" .env
  docker compose up -d
  ```

  The rotation re-encrypts everything in one transaction and checks the new
  key alone opens it all; on any error nothing changes. Operators sign in
  again afterwards. Back up the new `.env`.
- The backup key never changes (old backups need it); keep it offline.
- `PGDOCK_CONSOLE_DISABLED=true` turns the SQL console and table browser
  off for every project.

## Upgrades and disaster recovery

- [Upgrading](upgrade.md)
- [Disaster recovery](disaster-recovery.md): rebuild the control node,
  restore the metadata, re-register agents, bring back project data.
