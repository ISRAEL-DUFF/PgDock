# Backend services runbook

What to do when part of backend services (V4) misbehaves. Each entry says
how it shows, what PGDock already does on its own, and what is left to
you. How the pieces work is in [backend services](backend-services.md);
the incident process is in [incidents](incidents.md). Every failure below
has a test that injects it (named in each entry).

Checks used throughout:

- **An edge is up:** `curl -s https://<edge address>/healthz` (any host
  that isn't a project) answers `{"status":"ok"}` once it has loaded its
  configuration, and `503 starting` before.
- **Edges are reporting:** each edge sends usage and request logs every
  few seconds. In the metadata database:
  `SELECT edge, max(received_at) FROM edge_reports GROUP BY edge ORDER BY 2;`
  An edge more than a minute behind isn't reaching pgdock-server.
- **A project's requests:** Project → API → Logs (7 days), or
  `GET /api/v1/projects/{id}/services/logs`; every error carries a
  `request_id` that is also in the edge's own log.

## An edge process dies or is killed

*Shows as:* connections to that address fail; behind a load balancer,
its health check (`/healthz`) fails and traffic moves to the others.

*On its own:* nothing is lost that was committed. A request in flight
when the process died gets a dropped connection and no partial effect:
an upload that was streaming leaves no object row and its bytes (if any
reached the store) are removed by the storage sweep; a write either
committed or didn't. Realtime clients on that process lose their
WebSocket; the SDKs reconnect (to another edge, through the load
balancer) and rejoin their channels, and live queries refetch. The other
edges drop the dead one's presence within 35 seconds, because it never
said goodbye. Up to a few seconds of that edge's request counts and logs
are lost (usage is under-counted, never over-counted).

*You:* restart it (it is stateless). If it keeps dying, read its last
log lines (out of memory during image transforms is the usual one: give
it more memory, or fewer CPUs per process, since renders run one per two
CPUs).

*Tested by:* `TestEdgeCrashMidUpload`, `TestRealtimeNodeLoss`.

## pgdock-server is down or unreachable from the edges

*Shows as:* the edges' logs say `edge configuration feed` with an error;
the dashboard is down; the edges keep serving.

*On its own:* each edge keeps serving from the configuration it has and
holds its reports to send later. Changes made meanwhile (revoked keys,
new origins) don't reach the edges until pgdock-server is back. Auth
emails and SMS codes are queued by pgdock-server, so sign-up and
code sign-in wait.

*You:* bring pgdock-server back (its own runbook:
[operations](operations.md), [disaster recovery](disaster-recovery.md)).
**Don't restart edges while it is down:** an edge starts with no
configuration and answers `503 starting` until it can read the feed.
If a key must be revoked during the outage, the edges must be restarted
once pgdock-server is back anyway; there is no offline revocation.

## A project's database can't be reached

*Shows as:* that project's requests answer `503 database_unavailable`;
other projects are fine.

*On its own:* nothing: the edge retries each new request.

*You:* treat it as a database incident (the project's node, its pooler,
HA failover if it has HA). If the edge's log says `remaining connection
slots are reserved`, the shared node is out of connections; see
*Capacity* below.

## SMS provider outage

*Shows as:* pgdock-server logs `platform SMS provider failed; trying the
next` with the provider's error; `message_sends` records which provider
sent each code (`SELECT provider, status, count(*) FROM message_sends
WHERE created_at > now() - interval '1 hour' GROUP BY 1, 2`).

*On its own:* with both Termii and Africa's Talking configured, a code
goes by Termii and, if Termii fails, by Africa's Talking at once. A
provider that failed is tried last for a minute. If both fail, the code
waits in the outbox and is retried after 15 s, 30 s, 1 minute and 2
minutes, then recorded as `failed`; nothing is billed for a code that wasn't sent.
Codes are billed at the provider's reported cost plus margin, so a
fallback that costs more shows on invoices. Password, OAuth and email
sign-in carry on.

*You:* if only one provider is configured, add the other
(`PGDOCK_AFRICASTALKING_*` or `PGDOCK_TERMII_*`, [backend
services](backend-services.md#platform-sms-and-whatsapp-operators)) and
restart pgdock-server. For a long outage of both, post a status page
notice and suggest WhatsApp codes to projects that offer them. Check the
provider's balance first: an empty account fails like an outage.

*Tested by:* `TestSMSProviderOutage`, `TestFailover`.

## WhatsApp outage

*Shows as:* WhatsApp codes `failed` in Project → Auth → Phone; the WhatsApp
Cloud API's status page.

*On its own:* codes are retried as above. There is no second WhatsApp
provider: a project that offers SMS too lets its users pick it.

*You:* a status page notice; nothing to switch.

## Codes pumped to expensive numbers (SMS pumping)

*Shows as:* the project's admins get "Unusual SMS and WhatsApp codes"
emails; a project's message spend jumps.

*On its own:* the country allow-list (Nigeria only by default), per-number
and per-IP limits, the daily cap per project and captcha (if the project
turned it on) stop most of it; the spend cap slows everything else.

*You:* if it continues, lower the project's daily cap or turn off phone
sign-in for it (Project → Auth → Phone), and ask its owner to turn on
captcha. Credit pumped charges at your discretion.

## Realtime is degraded

*Shows as:* clients reconnecting or getting `resync`; the edge's log says
`realtime listen` or `realtime outbox` errors for a project.

*On its own:* an edge process lost: see above. The session pooler
unreachable: the edge retries its `LISTEN` connection with backoff (up to
30 s) and, once back, tells every subscriber to `resync`, so changes
missed meanwhile are refetched rather than lost. A subscriber past the
change rate gets `resync` too.

*You:* check the region's session pooler (`PGDOCK_EDGE_SESSION_ADDR`)
and the project's database.

## Image transforms overload an edge

*Shows as:* `503 busy` on renders, high CPU on an edge.

*On its own:* renders run one per two CPUs per process and queue;
cached renders are served from the store without decoding; the plan's
monthly transform allowance and the spend cap stop new renders.

*You:* add edge processes, or put the CDN in front of
`/storage/v1/render/public/…` (cached renders are cacheable).

## An organisation at its spend cap complains

*Shows as:* `429 spend_cap_rate_limited` and `429 spend_cap_reached` in
the project's logs; the organisation was emailed when it reached the cap.

*On its own:* data and storage requests get a quarter of the usual rate
limits, new image transforms pause, new realtime connections are
refused; sign-in and open connections carry on. Raising the cap (or the
next month) lifts it within seconds.

*You:* point them at Billing → **Spend controls**; nothing to do on the
platform.

## A key leaked

- **A project's secret key:** the project creates a new one, deploys it,
  and revokes the old one (Project → Settings → API, or `pgdock keys
  create` and `pgdock keys revoke`). Edges refuse a revoked key within a
  second or two.
- **A project's token signing key:** `pgdock auth rotate-key <project>`
  signs new tokens with a new key; tokens signed with the old key keep
  working until they expire unless the old key is revoked too, which signs
  everyone out.
- **`PGDOCK_EDGE_SECRET`:** whoever has it can read every project's edge
  configuration from the feed, database logins and signing keys
  included. Set a new value on pgdock-server and every edge and restart
  them together. The database logins' passwords are derived from
  pgdock-server's keyring, so they don't change with the secret: if the
  feed may have been read, treat it as a security incident
  ([incidents](incidents.md)) and rotate the projects' signing keys
  (`pgdock auth rotate-key`) and keys.

## Capacity

Each active project database holds at least one server connection on its
shared node while it gets requests (the pooler keeps idle ones for 30
seconds), and up to the pool size under concurrency. The load test
([load test](load-test.md#v4-backend-services-load-test)) found a 500
`max_connections` shared node saturated by about 300 projects in active
use. Watch `SELECT count(*) FROM pg_stat_activity WHERE backend_type =
'client backend'` on each shared node, and add a node (Platform →
Capacity) before active projects reach about 250 per node. Edges are
CPU-bound: the load test served 500 requests a second at a 15 ms p95 on
a 4-vCPU host shared with everything else.

## GA gates

V4.1 §13: backend services' general availability is announced on
evidence. Each gate has a script or test and a place where its result is
recorded; GA waits on every row having a dated result there. Update this
list whenever one changes.

**As of 2026-10-09: GA is not announced.** Every engineering part is
done; what blocks it is operator work on real servers and with outside
people.

| Gate | Engineering part | Result recorded in | Status (2026-10-09) |
| --- | --- | --- | --- |
| 20 GB move (M18) | `make test-move`; `.github/workflows/move-20gb.yml` on a runner with ≥ 80 GB | [moves](moves.md#checking-the-20-gb-target) | Not run at 20 GB; 1 GB passes in CI |
| 1,000 projects at 2,000 requests/s (V4 §13) | `TestBackendLoad` with `PGDOCK_LOAD_TARGET`; the six-server rig in `deploy/loadtest/` | [load test](load-test.md#v4-backend-services-load-test) | Run in the development container only (300 projects on one node; see its findings) |
| Payment sandboxes (M21) | `make test-payments-sandbox`; the steps in [payments](payments.md#sandbox-rehearsal) | [payments](payments.md#sandbox-rehearsal) | Not run: needs sandbox keys |
| Floating-IP failover (M17) | `scripts/rehearse-pooler-failover.sh` | [edge poolers](edge-poolers.md#tested-and-not-yet) | Not run on Hetzner; 3.1 s measured against a paused dev Postgres |
| Accountant's answers (M27) | — | [billing audit](billing-audit.md#for-the-accountant) | Pending |
| Lagos failure domains (V3.1) | The region launch checks refuse to open a region whose etcd isn't in three domains | [Lagos launch](lagos-launch.md) | Waiting on the facility to confirm racks or feeds |
| Penetration test (V4 §13) | A staging region per [the scope](pentest-scope.md) | [pen-test tracking](pentest-scope.md#tracking) | Not engaged |
