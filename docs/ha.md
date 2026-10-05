# High availability for dedicated projects

An HA dedicated project runs a **primary and a streaming standby on
different nodes**, managed by [Patroni](https://patroni.readthedocs.io)
with its state in a three-member **etcd** cluster (V3 §2.2). If the
primary's node fails, the standby takes over and pgdock-server moves the
project's pooler route to it: clients keep the same URL. In the tests,
writes come back about **17–20 seconds** after the primary's node dies
(the target is under 60). HA projects carry the **99.9% SLA**, measured
from two vantage points (§2.7).

## Setting up: the etcd cluster

Once per platform, Admin → Nodes → **etcd cluster for HA**: pick three
nodes with agents, ideally the control node and two others. PGDock starts
an etcd member on each (`gcr.io/etcd-development/etcd:v3.6.5`, pulled by
the agent; `PGDOCK_AGENT_ETCD_IMAGE` overrides it). Clients and peers use
TLS with certificates from PGDock's own etcd CA (separate from the agent
CA; its key is sealed with the master key). The panel shows each member's
health; HA needs a quorum (two of three).

Members are reached at the node's address (`PGDOCK_AGENT_PUBLISH`, on
ports 2379 and 2380) or, on a single Docker host, by name on the agent's
network. Open 2379–2380 between the three nodes on the private network.

API: `GET/POST /api/v1/admin/etcd`.

## Turning HA on

Project Settings → Compute → **High availability → Enable HA…**, `pgdock ha
enable <p> [--node <id>] [--sync]`, or `POST /api/v1/projects/{id}/ha`. It
needs a second node that takes dedicated instances. The operation:

1. creates a replication role and the project's Patroni credentials;
2. restarts the instance under Patroni on the same data. Clients on the
   pooled URL wait (about 4 seconds in the tests); session-mode clients
   reconnect;
3. starts a standby on the other node from the newest WAL-G base backup
   (falling back to `pg_basebackup`) and waits until it streams;
4. adds the SLA probe login and starts measuring availability.

If the standby can't be built, it is removed and the project keeps
running as before.

**Synchronous replication** (`--sync`, or the switch on the card) makes
every commit wait for the standby: nothing acknowledged is lost on
failover, at the cost of write latency. Without it, replication is
asynchronous, and Patroni won't promote a standby more than 1 MB behind.

## Failover

Patroni promotes the standby when the primary's leader lease (20 s)
expires. pgdock-server checks every HA project's members every second:

- when the primary's Patroni API stops answering, the poolers **pause** the
  project's clients, so they wait rather than fail;
- when Patroni has promoted the standby, the pooler route points at it and
  the poolers **resume**;
- the old primary, when its node comes back, rejoins as the standby (with
  `pg_rewind`).

Each change is a **failover event** with how long writes were out. The
project's HA card (and `pgdock ha status <p>`) shows each member's role,
state and lag, the history, and the month's availability.

Containers have no watchdog device, so fencing is Patroni's leader lease
alone: a primary that can't renew its lease demotes itself, and the
poolers only ever follow the member holding the lease.

## Switchover

**Switch over** on the card, `pgdock ha switchover <p> [--to <member>]`, or
`POST /api/v1/projects/{id}/switchover`: the poolers pause, Patroni hands
the primary role to the standby (the old primary stops cleanly first, so
nothing is lost), the route moves, and the poolers resume. Writes paused
about 2 seconds in the tests. The maintenance window uses the same path for
HA projects: it restarts the standby onto a new minor release, switches
over to it, then restarts the old primary.

## Backups

WAL-G archives from whichever member is primary: Postgres only runs the
archive command there, and both members use the project's archive, so
point-in-time recovery spans failovers (WAL-G follows the timeline
change). Base backups run on the current primary's node.

## Turning HA off

**Turn off** on the card, `pgdock ha disable <p>`, or
`DELETE /api/v1/projects/{id}/ha`: the standby is removed and synchronous
replication goes off. The primary stays under Patroni with one member, so
turning HA on again doesn't restart it.

While HA is on, moving the project to another node, demoting it, a major
upgrade, and stopping or restarting it from the Instance panel are refused
(turn HA off first, or use a switchover).

## Availability (the SLA)

Each HA project has a probe login (`<database>_sla`) that can only connect
and run `SELECT 1`. Every minute pgdock-server connects through the
project's **pooled URL** and runs it. With a status page configured
(`PGDOCK_STATUS_URL`), pgdock-server also gives pgdock-status the probe
targets, which it probes from outside every interval, and collects its
per-minute results. Both directions are signed with the push secret.

A minute is **unavailable when every vantage point that probed in it
failed**. With no status page, that is pgdock-server's probe alone.
Minutes while the project is busy with an operation it asked for (a
restore, a move) or its organisation is suspended are excluded. The
project's HA card shows the month so far; `GET /api/v1/projects/{id}/ha`
has the minutes.

## Not yet

- Replacing an etcd member (a dead etcd node) is manual: the cluster is set
  up once, and PGDock doesn't yet re-run it while HA projects use it.
- Excluding maintenance announced 72 hours ahead (V3 §2.7) from the
  availability record.
- Placement groups at the provider (M24) and per-region etcd clusters
  (M25).
