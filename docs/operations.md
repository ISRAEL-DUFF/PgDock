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
  | Isolation check | critical | The nightly tenant-isolation check found a problem |

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

### Storage targets and project keys

Backups go to a **storage target**, an S3 bucket and prefix. **Platform
targets** (Settings → Platform storage targets) are yours to run; exactly
one is the default, used by every project that hasn't chosen another, and
backup storage on them counts against each organisation's
`backup_storage_mb` quota (manual backups beyond it are refused; nightly
ones still run). **Org targets** (Organisation → Backup storage, owners
and admins) are buckets an organisation brings itself: its projects can
use them, the storage doesn't count against its quota, and neither other
organisations nor you see them (the admin organisation list only counts
the projects using one). Adding or editing a target writes, reads, lists
and deletes a test object under its prefix first; credentials are sealed
with the master key and never shown again.

A project admin picks the target under **Backups → Storage**. New backups
go there; existing ones stay where they are and stay restorable, or can
be copied over (verified by checksum, originals deleted only if asked).
For a dedicated project the switch restarts the instance with WAL-G
pointed at the new target and takes a base backup at once; the previous
archive stays restorable for the 7-day recovery window, then goes.

**Use a project key** gives a project its own backup key: new backups are
OpenPGP messages to it, and on the dedicated tier WAL-G uses it too.
**Download key…** needs re-authentication, is audited, and gives a file
with a README on restoring with gpg and pg_restore alone
([disaster recovery](disaster-recovery.md#restoring-a-project-without-pgdock)).
A target can't be deleted while a project uses it; one that still holds
backups is deleted only when you accept that they become unrestorable. A
project whose backups are on its organisation's target moves to platform
storage before it can be transferred to another organisation.

## Security checks

Every shared cluster is checked nightly against the tenant-isolation
checklist (Settings → Tenant isolation, **Check now** to run it at once):
cluster settings and `pg_hba.conf`, every project's role and database, and
two throwaway tenants that try to reach each other, discover other
projects' names, get round their quotas, or log in while suspended. See
[security review](security-review.md).

## Users and organisations

Every user has a personal organisation and can be invited into others.
Organisation owners and admins manage members and see every project in
it; members see only the projects they are added to, as admin, developer,
or read-only. Each member gets their own database login per project
(Members → **Get my credentials**): `<db>_u_<id>`, read/write for admins
and developers, read-only for read-only members. Removing someone from a
project or organisation drops their logins and ends their connections at
once; the app's own `<db>_owner` password is never shared, so it never
needs rotating when people leave.

As platform admin you manage accounts (Users: approve, disable, reset
two-factor after checking who they are), email, sign-up, and the terms.
You do not see into other people's organisations through the UI. The
audit logs are per project, per organisation (Audit log), and for the
platform (Platform audit).

Projects created before V2 are in the platform admin's personal
organisation, unchanged: same URLs, passwords, and backups.

## Quotas, storage locks and usage

Each organisation has a plan (Organisations → the org → Plan), with
per-key overrides; Plans lists and edits them. Creating projects, backups,
restores and console queries past a limit fails with the limit named.
Owners and admins see their use on **Usage & quotas**, with hourly
storage and a CSV export; you see every org's on the admin usage API.

Shared-tier projects are measured every minute:

- **90%** of the project's storage limit: a warning email and banner.
- **100%**: read-only by default. Apps can still delete in a read-write
  transaction (`BEGIN READ WRITE`), and the SQL console works.
- **120%** (or 100% with the node's disk 95% full): apps cannot log in.
  The console and table browser still work; delete data there, then
  **Reclaim space** (Settings → Storage) to give the space back.

Locks lift at the next check once the project is under the limit.
Statements running over 10 minutes are cancelled and transactions idle
over 5 minutes are ended; the project's Metrics page lists them.

Dedicated instances beyond an org's allowance become requests (Dedicated
requests) for you to approve or reject. An org can be given its own
shared cluster (Organisations → the org → Shared cluster): its new
projects go only there, and nobody else's do.

## Suspension, break-glass and deletion

**Suspend** (Organisations → the org, with a reason) stops the org's
projects accepting connections and its backups, and makes the org
read-only for its members; **Reinstate** undoes it. **Outbound access**
is recorded now and enforced when outbound features ship.

To look inside an org (a support case), open a **break-glass** session
with a reason and a length (up to 4 hours). You act as an org admin; its
owners and admins are emailed, every action is in its audit log marked
break-glass, and they see a banner. End it when you are done.

Owners can delete an organisation (Organisation → Delete). It is
read-only for 7 days and can be cancelled; then each project gets a final
backup, kept for 30 days, and is deleted.

## Opaque names

New projects' databases and roles are named `p_<random>`, so other
tenants cannot learn them. Projects from before V2 are renamed on the
server side and keep their old name as an alias, so their URLs keep
working; their role name stays visible until an admin uses **Switch to
opaque credentials** (Settings), which issues new URLs and keeps the old
ones working for a grace period.

## The table editor

Project members edit data and schema under **Tables** (developers and
above; read-only members browse and export). Rows: filter, sort,
double-click to edit, then **Save**; everything in one save commits or
none of it, and if someone changed a row since you loaded it you see a
conflict instead of overwriting their change. Tables need a primary key
to be editable. **Structure**: every change shows its SQL and risk notes
first, such as "Rewrites the table and blocks writes" for a type change,
and runs with a 5-second lock timeout, so it fails fast rather than
queueing behind a long transaction. Indexes build concurrently. Schema
changes are in the project's audit log with their SQL. **Save as
migration** exports the change as plain SQL, goose or dbmate, to apply
to other environments.

## API tokens and the CLI

Members use the `pgdock` CLI ([CLI guide](cli.md)) and API tokens for
scripts and CI. Tokens act in one organisation with read, write or admin
scope, optionally restricted to some projects, and expire within a year.
Set a lower maximum under **Settings → API tokens**. Organisation owners
and admins revoke their members' tokens under **Organisation → API
tokens**; suspending an organisation disables its tokens, and removing
someone revokes theirs. You can't see other people's tokens, and a
platform admin's own tokens can't manage the platform: use the browser
for that.

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
