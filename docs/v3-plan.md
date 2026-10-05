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

### Upgrade path

Existing installs keep their single pooler host. Adding a standby is
documented as: provision a second host, run the pooler-host installer,
register it, create the floating IP, then point the database DNS name at
the floating IP.
