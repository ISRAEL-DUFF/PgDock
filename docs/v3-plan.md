# V3 build plan

How V3 ([the spec](../PGDock%20—%20V3%20Specification%20&%20Build%20Plan.md))
gets built. The spec says *what*; this file says how each milestone is
approached in this codebase, what is tested where, and what can only be
proven on real infrastructure. Decisions made while building go in
[decisions.md](decisions.md) under each milestone, as in V1 and V2.

## Ground rules

- **Branch.** All V3 work lands on `feature/pgdock3`. It merges into `main`
  only when all of V3 is built and tested; V2 stays in production meanwhile.
  `main` fixes are merged into `feature/pgdock3` as they land.
- **Nothing in V2 breaks.** Every V3 feature is additive or behind a
  setting. A V2 install upgraded to the V3 code with nothing configured
  behaves as V2 does: one pooler host, one region, no billing.
- **Milestones in spec order** (M17 → M27), one at a time, each finished
  with its "done when" test, docs, and CI green before the next.
- **Fakes for what we can't run here.** Hetzner, Flutterwave, iSpend,
  WhatsApp and outside-network probes are reached through interfaces with
  an in-repo fake used by tests. Each milestone notes what still needs a
  run against the real thing.

## M17 — Standby pooler and status page

### What exists

- Two PgBouncers (`session` :5432, `transaction` :6543) run next to
  pgdock-server and read `databases.ini` and `userlist.txt` from a shared
  directory that `pooler.Manager` writes atomically, then `RELOAD`s over
  the admin console (`internal/pooler`).
- Pause, resume, kill and reconnect go to every configured admin console.
- Agents run on database nodes over mTLS (`internal/agentsvc`).

### Design

**Pooler hosts are nodes.** A pooler host runs `pgdock-agent` with a new
node role `pooler`, the two PgBouncers, and keepalived. It registers like
any node (one-time token, pinned client certificate). Regions arrive in
M25; until then all pooler hosts form one pair.

**Config sync through the agent.** `pooler.Manager` renders once, then
pushes `databases.ini`, `userlist.txt` and the TLS pair to each pooler
host's agent (`PUT /v1/pooler/config`, body plus SHA-256). The agent writes
atomically, `RELOAD`s its PgBouncers, and answers with the hash it now
serves. The manager records each host's hash and reports a sync as complete
only when both match. The single-host layout (shared directory) stays as a
second sink, so V2 installs keep working unchanged.

**Admin commands go to both hosts.** `PAUSE`, `RESUME`, `KILL` and
`RECONNECT` are sent to all four PgBouncers. Only the active host has
clients; the standby's copy keeps its state consistent if the IP moves
mid-operation.

**Floating IP and keepalived.**
- keepalived on each host, in VRRP unicast mode (Hetzner networks have no
  multicast), with a check script that fails unless both local PgBouncers
  answer *and* the agent reports its config hash as current. A host with a
  stale config therefore can't win the address.
- On becoming master, keepalived's notify script asks the local agent to
  claim the floating IP. The agent calls the floating-IP provider: Hetzner's
  API (assign floating IP to this server) in production, `ip addr` on a
  shared network in tests.
- Failover target: under 10 seconds (VRRP advert 1s, 3 missed adverts, plus
  the API call). Existing client connections drop and reconnect.

**External arbiter in the control plane.** Every few seconds pgdock-server
checks each pooler host (agent health, PgBouncer reachability, config
hash) and which server the floating IP is assigned to (Hetzner API). If the
IP points at an unhealthy or stale host while the other is healthy and
current, which happens when keepalived is split, the control plane
reassigns it and raises an alert. Every reassignment, whoever made it, is
audited and logged as a pooler event.

**Status page as a separate program.** A new binary, `pgdock-status`, built
from this repo and run on other infrastructure (another provider):
- Probes from outside every minute: the dashboard (`/healthz`), the pooler
  floating IP on both ports (a real query as a dedicated low-privilege probe
  role through each pooler), and the shared tier (the same probe into a
  small probe database).
- Components that can't be probed from outside (backups, webhooks and jobs,
  dedicated instances) take their state from **signed heartbeats** that
  pgdock-server pushes. A missing heartbeat shows the component as
  "unknown", never "operational".
- Stores its own state (component history, incidents, subscribers) in a
  local SQLite file. It has no dependency on PGDock's database.
- **Incidents** open automatically after sustained probe failures (default:
  3 consecutive minutes) and close automatically on recovery. The platform
  admin can also create, update and resolve incidents in PGDock
  (Admin → Incidents); pgdock-server pushes them to the status service with
  an HMAC signature.
- **Subscriptions** by email, with double opt-in, through the status
  service's own SMTP settings. Auto-subscribing paying orgs waits for
  billing (M20).
- **90 days of uptime history** per component, by minute, rolled up daily.
- The page is server-rendered HTML with no JavaScript required, plus a
  JSON API and an RSS feed.

### Data model (M17 part of spec §9)

`incidents` and `incident_updates` in the metadata DB, plus pooler-host
state (assigned hash, last check, floating-IP holder) on `nodes` and a
`pooler_events` log. The status service keeps its own copy of incidents.

### Testing

- **Unit:** keepalived config rendering, hash comparison, arbiter decisions
  (a table of host states and the expected action), status-page
  aggregation and incident rules.
- **Integration (Docker):** two pooler-host containers, each running the
  agent, both PgBouncers and keepalived, sharing a floating address on a
  Docker network. A fake Hetzner API stands in for the floating-IP
  assignment.
- **Done-when test:** with a client running queries through the floating
  address, `docker kill` the active pooler host. Queries succeed again
  within 10 seconds through the same address. A `pgdock-status` container
  on a separate network marks the edge pooler down and then back up, and
  opens and resolves an incident by itself.
- **Not provable here:** real Hetzner floating-IP reassignment timings and
  placement groups, and probing from a different provider. These need a
  run on two real Hetzner servers before V3 ships; the install docs will
  include the steps.

### As built

Where the build differs from the design above (details in
docs/decisions.md, M17):
- keepalived runs in its own container beside the pooler-host container
  (PgBouncers and the agent), so only keepalived needs `NET_ADMIN`.
- A configuration change succeeds when at least one pooler host takes it.
  A host that misses it is stale, fails keepalived's check, and is pushed
  to again until it catches up.
- The done-when test runs pgdock-status on the host's network, outside the
  Docker network the pooler hosts share; Docker doesn't route between
  bridge networks.

### Upgrade path

Existing installs keep their single pooler host. Adding a standby is
documented as: provision a second host, run the pooler-host installer,
register it, create the floating IP, then point the database DNS name at
the floating IP.

## M18 — Logical-replication moves and Postgres versions

### What exists

- Promotion (shared → dedicated, V1 §6.6) and demotion (dedicated →
  shared, V2 §5) copy with `pg_dump | pg_restore` on the target node's
  agent during a write freeze: roles go `NOLOGIN`, the pooler `PAUSE`s the
  database, sessions end, the copy runs, `pgverify` compares every table's
  rows and every sequence, the route switches and the pooler resumes. The
  source is kept read-only for 48 hours (`retired_databases`).
- Instances run `wal_level=replica` (WAL-G archiving) and one Postgres
  version, 18: `InsertInstance` hard-codes it and the agent has one image
  (`pgdock-postgres:18-walg3.0.9`).

### Design

**One engine, `internal/logical`.** A move copies one project's database
from its instance to another instance and switches the route, used by
promotion, demotion, node moves and major upgrades. Steps (V3 §2.3):

1. **Preflight** on the source and target: `wal_level = logical` on the
   source, a free replication slot and WAL sender, no large objects, no
   unlogged tables, extensions available on the target (at the target's
   version), the target's version not older than the source's. Tables
   without a primary key or replica identity are listed and get
   `REPLICA IDENTITY FULL` for the move. A failed check names itself; the
   caller falls back to dump/restore (promotion, demotion, node moves) or
   refuses (a major upgrade has no dump/restore path worth a long freeze,
   so it says why and stops).
2. **Target**: database, owner and members' roles with the same SCRAM
   verifiers, extensions, then `pg_dump --schema-only` restored with
   owners and grants (the existing copy, schema only).
3. **DDL block** on the source: an event trigger, owned by the superuser
   in PGDock's `pgdock` schema, that refuses DDL with "this project is
   being moved" while the move runs.
4. **Replication**: a publication for all tables on the source; a
   short-lived login there (`LOGIN REPLICATION BYPASSRLS`, `SELECT` on
   every table, dropped afterwards) that the target's subscription
   connects as, at the source's node-local address. Postgres copies the
   data, then streams changes.
5. **Catch up**: every table `ready` in `pg_subscription_rel`, then slot
   lag (`pg_current_wal_lsn() − confirmed_flush_lsn`) under 1 MB for 30
   seconds. Progress goes to the operation log.
6. **Cutover**: the same freeze as today (roles `NOLOGIN`, pooler `PAUSE`,
   sessions end). Then the source's WAL position is read and the engine
   waits for the slot to confirm it. Sequences are copied with `setval`
   plus a margin of 1,000. Row counts are compared on a sample of tables:
   the 20 smallest by estimate, plus any changed during the freeze. Then
   the route switches and the pooler resumes. The freeze is timed and
   logged; the target is under 5 s whatever the database's size.
7. **Cleanup**: drop the subscription (which drops the slot), the
   publication, the login and the DDL block. The source stays read-only
   for 48 hours, as now.

Before the cutover, any failure rolls back: the target goes and the
source's publication, slot, login and DDL block are removed. The source
was never written to. After the cutover there is nothing to undo.

**`wal_level=logical` everywhere new.** Dedicated instances and
agent-run shared clusters start with `wal_level=logical` (a superset of
`replica`, so WAL-G is unaffected) and room for 10 slots. A cluster still
on `replica` fails preflight with how to fix it, and dump/restore runs.
The dev and test shared clusters get it in Compose.

**Postgres versions.** `PGDOCK_PG_VERSIONS` (default `17,18`) lists the
supported majors; the newest is the default. A project picks a version at
creation. A shared project goes to a shared cluster of that version, and a
dedicated instance runs that version's image. The agent's image becomes a
template, `pgdock-postgres:{major}-walg3.0.9`, with one image built per
version. `AddSharedCluster` takes a version, and the dev environment gains
a Postgres 17 shared cluster for tests.

**Major upgrade** (`POST /projects/:id/upgrade`) is a logical move to an
instance of the newer version. For a dedicated project that is a new
instance on the same node, or another node with room. For a shared project
it is a shared cluster of the target version. The preflight adds:
- extensions not available at the target version;
- objects using features removed since the source version, found from
  the catalog. The list is short and versioned, e.g. `abstime`-style types,
  removed operators, `WITH OIDS` remnants and `pg_stat_statements` column
  renames in views.

**Node moves** (`POST /projects/:id/move`, platform admin): a shared
project to the shared cluster on another node (same version), or a
dedicated project to a new instance on another node. A logical move,
falling back to dump/restore.

**Minor upgrades in a maintenance window.** A platform setting holds the
weekly window: a day, an hour (UTC) and a length. In the window, a sweep
restarts each dedicated instance whose image's minor version is behind the
newest pulled for its major, one at a time. This is the V1 §11.3 rolling
restart, automated: pull, recreate the container on the same volume, wait
until it is healthy, check `server_version_num`. Shared clusters are
restarted the same way, with the pooler paused for their databases during
the restart. HA switchover instead of restart arrives with M19.

### Data model

`operations` gain kinds `logical_move` and `major_upgrade` and
`minor_upgrade`. `projects.pg_version` is not needed: a project's version
is its instance's. A `moves` table records each logical move: source and
target instance, mode (logical or dump), phase, lag, freeze duration and
fallback reason. It feeds the operation log, the UI's progress and the
done-when check. `settings` holds the maintenance window.

### Testing

- **Unit:** preflight checks against fixtures, sequence margin, sample
  selection, deprecated-feature scan, maintenance window maths.
- **Integration:** logical node move of a shared project between two
  shared clusters (the dev one and an agent-run one), with a writer
  committing numbered rows throughout. Every acknowledged commit must be
  on the target, and the logged freeze must be under 5 s. Then a promotion
  and a demotion through the engine, and the fallbacks: a large object,
  and a source on `wal_level=replica`. Then a 17 → 18 upgrade of a
  dedicated project and of a shared project.
- **Done-when:** the same move with a 20 GB database
  (`PGDOCK_TEST_MOVE_GB=20`, `make test-move`). CI runs it at 1 GB; the
  20 GB run is documented with its measured freeze.
- **Not provable here:** moves between real nodes over a private network,
  and multi-hour initial copies of production-sized databases.
