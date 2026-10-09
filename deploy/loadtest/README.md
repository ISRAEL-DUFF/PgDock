# The backend services load rig

V4.1 §13's gate: **1,000 projects with backend services at 2,000 data API
requests a second**, on servers shaped like production. It takes six
Hetzner Cloud servers for about an hour (≈ €2), and the result goes in
[docs/load-test.md](../../docs/load-test.md#v4-backend-services-load-test).
The development container can't run it: 1,000 project databases need
about 9 GB of disk, and one shared node can't keep them all busy.

| Server | Type | Runs |
| --- | --- | --- |
| node-1 | CCX23 (4 dedicated vCPU, 16 GB) | The normal install (`deploy/compose`): pgdock-server, the poolers and shared cluster 1 |
| node-2, node-3, node-4 | CCX23 | `--profile node`: an agent, then shared clusters 2–4 |
| edge | CCX33 (8 vCPU) | `--profile edge`: pgdock-edge on 443 |
| load | CCX33 | `--profile load`: `TestBackendLoad` from this checkout |

Put all six on one private network (10.0.0.0/16) in one location, and open
only 22 and 443 to the internet.

## 1. node-1: the install

Follow [docs/install.md](../../docs/install.md) on node-1. Before running
the installer, add to its `.env`:

```sh
PGDOCK_API_DOMAIN=api.example.com           # projects at <ref>.api.example.com
PGDOCK_EDGE_SECRET=$(openssl rand -hex 32)  # the same on the edge
```

Finish the setup wizard. The owner it creates is the platform admin the
load host signs in as: note the TOTP secret shown as text while enrolling.
Then set a backup target (an S3 bucket) in **Settings → Backups**: file
storage, and so the transform burst, uses it. Every shared cluster,
node-1's included, allows 500 connections.

## 2. node-2 to node-4: shared nodes

On each, install Docker, copy this directory with `loadtest.env.example`
as `.env`, and set `NODE_IP` to the server's private address. In the
dashboard, **Platform → Nodes → Add node** gives a one-time token: put it
in `PGDOCK_AGENT_TOKEN`, then:

```sh
docker compose --profile node up -d
```

When the node shows as healthy, **Create a shared cluster** on it (8 GB of
memory). New projects go to the shared cluster with the most free room, so
the 1,000 spread over the four.

## 3. edge

Get a wildcard certificate for `*.api.example.com` (DNS-01, or a
self-signed one: put its CA in `tls/ca.pem` and set
`PGDOCK_LOAD_EDGE_CA`), put `fullchain.pem` and `privkey.pem` in `tls/`,
then:

```sh
docker compose --profile edge up -d
```

No DNS is needed: the load host dials `EDGE_IP` for every project's name.

## 4. load: the run

Clone this repository on the load host, fill in `deploy/loadtest/.env`
(the admin's email, password and TOTP secret), and from
`deploy/loadtest`:

```sh
docker compose --profile load run --rm load
```

It signs in, makes 10 organisations of 100 projects each with backend
services (about 15 minutes), lifts their rate limits, warms every project
on the edge, runs `PGDOCK_LOAD_STEPS` and then the target rate, and ends
with the transform burst. The report is `tmp/load-report-backend.md` in the
checkout. While it runs, watch the nodes' client backends and the edge's
CPU (Platform → Nodes, or `docker stats`).

It fails if more than 0.1% of requests fail, under 95% of the target rate
is served, the p95 is over 250 ms (500 ms during the transform burst), or a
transform fails.

## 5. Record it and tear down

Add the report's tables to docs/load-test.md under a dated heading with
the server types, and a line in the GA gates list
([docs/backend-runbook.md](../../docs/backend-runbook.md#ga-gates)). Then
delete the six servers: the run leaves 1,000 projects behind on purpose.
