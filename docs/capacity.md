# Capacity and costs

PGDock can add database nodes by itself when a region fills up. It can
also move projects off nodes, delete servers that stay empty, and work out
what each organisation costs to run (V3 §5).

Everything here is on **Platform → Capacity** and **Platform → Costs &
margins**.

## Providers

The provider is where new servers come from. There are two.

- **manual** (the default) is for machines PGDock can't create: the
  install's own node, and colocated servers. You register them under
  Nodes → Add node, as before. Proposals still say when to add a machine
  and what size it should be. Once you have registered it, choose
  **Mark added** on the proposal.
- **hetzner** creates servers in a Hetzner Cloud project. Each server
  boots with cloud-init, which does the following:
  - installs Docker, ufw, fail2ban and unattended upgrades;
  - turns off SSH passwords;
  - allows SSH from anywhere, and everything else only from the private
    network;
  - runs the agent with a one-time registration token.

  The agent registers by itself. A shared node then gets its shared
  cluster, and the node goes into service.

| Variable | What |
| --- | --- |
| `PGDOCK_CLOUD_PROVIDER` | `manual` or `hetzner` |
| `PGDOCK_REGION` | This installation's region name (default `eu-central`). New nodes are given it, and so are existing ones on upgrade. |
| `PGDOCK_HETZNER_TOKEN` (or `_FILE`) | A Hetzner Cloud API token with read and write access |
| `PGDOCK_HETZNER_LOCATION` | Where servers go (default `fsn1`) |
| `PGDOCK_HETZNER_NETWORK_ID` | The private network new servers join; use the network your other nodes and pgdock-server are on |
| `PGDOCK_HETZNER_PLACEMENT_GROUP_ID` | Optional: a spread placement group |
| `PGDOCK_HETZNER_SSH_KEYS` | Comma-separated names of SSH keys in the Hetzner project, for root |
| `PGDOCK_CLOUD_AGENT_IMAGE`, `PGDOCK_CLOUD_PG_IMAGE` | Images new servers can pull: the agent, and `pgdock-postgres` (with `{major}` for the Postgres version). Push your build to a registry; the bundle builds them locally only. |
| `PGDOCK_CLOUD_SERVER_URL` | How agents reach pgdock-server (default `PGDOCK_PUBLIC_URL`) |
| `PGDOCK_CLOUD_SERVER_CA_FILE` | The PEM of a private CA, if pgdock-server's certificate isn't publicly trusted |
| `PGDOCK_CLOUD_PRIVATE_CIDR` | The private network, default `10.0.0.0/16`. Instances accept logins from it, and the agent advertises its address on it. |

Servers PGDock creates carry the labels `pgdock=node`, `pgdock-node=<name>`
and `pgdock-region=<region>`. Don't delete them by hand while PGDock still
lists them: drain the node instead (below).

## Proposals

Every hour, and when you choose **Check now**, PGDock checks two
thresholds in each region.

- **Shared tier:** the region's shared disk is projected to pass 70%
  within 14 days. The projection is the disk now plus the past week's
  hourly growth, fitted with a line.
- **Dedicated tier:** no dedicated host has room for the largest instance
  size (vCPU, memory and disk). This check only runs in regions that
  already have a dedicated host.

When a threshold trips, PGDock records a **proposal**: the server type, the
location, the monthly price from the provider's catalog, and the reason. A
region and tier never have more than one open proposal at a time. A
proposal is provisioned by itself only when all of these hold:

- automatic provisioning is on;
- the provider can create servers;
- the nodes' monthly cost plus the new server's stays within the
  **monthly infrastructure budget**.

The budget is zero until you set one, so nothing is bought until then.
When the budget is in another currency than the servers, PGDock converts
at the current exchange rate (below).

Otherwise the proposal waits, and an alert fires. **Provision** starts the
server; **Reject** closes the proposal. Both the settings and approving a
proposal ask for your password and a code first, because they spend
money.

Provisioning is an operation with a log, and it runs these steps:

1. It creates the node with a one-time token.
2. It creates the server, with that token in the server's cloud-init.
3. It waits up to 20 minutes for the agent to register and answer.
4. On a shared node, it creates the shared cluster.
5. It puts the node into service.

From then on, new projects go to the node with the fewest projects, which
is the new one.

If any step fails, PGDock deletes the server and removes the node, the
proposal is marked failed, and a critical alert fires. The server's
`/var/log/cloud-init-output.log` usually says why.

The settings (Platform → Capacity → Settings) are:

- per tier: whether to propose at all, the threshold, the horizon, and the
  server type to add (or the minimum vCPU, memory and disk, leaving PGDock
  to pick the cheapest type that fits);
- the shared cluster's memory on a new shared node;
- the budget and its currency.

## Draining and rebalancing

- **Drain** (Capacity → Nodes) stops anything new from being placed on a
  node and moves its projects elsewhere in the region, one at a time, with
  zero-downtime moves. Each shared project goes to the shared cluster with
  the most free disk that runs its Postgres version and is open to its
  organisation. A dedicated project goes to the dedicated host with the
  most free vCPU that fits it.

  A drain skips three kinds of project, with the reason shown:
  - HA projects: switch the primary over, or turn HA off, first;
  - paused Free projects: resume them first;
  - archived Free projects: unarchive them first, since their roles live
    on that cluster.

  **Stop drain** puts the node back into service and drops the moves that
  haven't started.
- **Rebalancing** runs weekly, and when you choose **Plan rebalance**. If
  shared nodes in a region differ in disk use by more than 15%, it
  proposes moves from the fullest node to the emptiest, at most ten in a
  batch, choosing the largest projects that narrow the gap. Approve or
  reject the batch. With automatic rebalancing on, batches are approved by
  themselves during the maintenance window (Nodes → Maintenance window).
- **Empty nodes:** a node with no projects, dedicated instances, HA
  members, etcd member, or move copies is marked empty. The copies a move
  keeps are kept for 48 hours. A server from a provider is deleted after
  24 hours empty, and the node is removed. Manual machines are only
  flagged: decommission them yourself. Mark a node **Keep** (Capacity →
  Cost) to leave it alone.

## Cost attribution

Every day, PGDock divides the infrastructure's cost among the
organisations that used it (V3 §5.4):

| Cost | From | Divided by |
| --- | --- | --- |
| A node | Its monthly price ÷ the days in the month: the provider's catalog price for servers PGDock created; for manual nodes, what you enter (Capacity → Cost) | Each dedicated instance takes its share of the node's vCPUs; an HA standby's share is booked as **ha**. The rest goes to the shared cluster's projects: half by their share of storage (GB-hours), half by their share of client connection-hours. What nothing uses is **idle**. |
| Backup storage | The object storage price per GB-month | Each organisation's backup GB-hours |
| Data transfer | The price per GB | Each organisation's pooler transfer |
| Floating IPs, fixed overheads | Their monthly prices (Costs & margins → Cost settings), ÷ days | Nobody: **unallocated** |

Costs stay in the currency they're billed in. Hetzner bills in euros, and
so do the default storage, transfer and floating IP prices. Yesterday and
any earlier days of the month not yet attributed are attributed hourly.
**Recompute** attributes the whole month again. A node removed since is
left out of days recomputed after its removal.

## Margins and the FX view

**Costs & margins** shows a month at a time:

- **Cost by region and tier**, in the billing currency, in naira at
  today's rate, and in naira at the rate in effect on each day.
- **Margin by plan** and **by organisation**. Revenue is what the month
  earns: its usage, and its own plan fee, even though the invoice bills
  the next month's fee in advance. Margin is revenue minus the
  organisation's cost at today's rate.
- **Unit costs**: a shared GB-month, a dedicated vCPU-month and a backup
  GB-month, in the billing currency and in naira. They feed repricing:
  unit price = unit cost × (1 + target margin) × FX buffer (V3 §3.1).
- **Free tier cost**, and **FX erosion**: how much more the month's costs
  are at today's rate than at the rates when they were incurred. A rising
  figure means margins are slipping before the next repricing.
- A **CSV** for the accountant.

Record exchange rates (naira per unit) under **Exchange rates**. Costs in
a currency with no rate are left out of the naira totals, and the page
says so.
