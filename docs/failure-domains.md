# Failure domains

A node's **failure domain** says what fails with it: a rack, a physical
host, a power feed. PGDock keeps the things that must survive one failure
in different domains (V3.1-M1):

| Group | Rule | When it can't be met |
| --- | --- | --- |
| An HA project's primary and standby | Different domains | Enabling HA is refused, naming the nodes and their domains. |
| The etcd cluster's three members | Three different domains | Setting up the cluster is refused. |
| A region's two pooler hosts | Different domains | Adding the second host warns (PGDock can't move pooler hosts). |

Shared projects have no pair, so domains don't affect them.

## Where a node's domain comes from

- **Recorded by you.** Platform → Nodes → the node → **Failure domain**
  (or when adding the node, or `PATCH /api/v1/nodes/{id}` with
  `{"failure_domain": "lagos-dc1-r3"}`). Use one name per rack or
  power feed, e.g. `lagos-dc1-r3`. An empty value clears it. This is how
  colocated (manual) nodes get one; it also overrides the placement group
  below.
- **A Hetzner spread placement group.** Servers PGDock creates go into
  the region's spread group, `pgdock-<region>`, which Hetzner keeps on
  different physical hosts. A spread group holds 10 servers. When it is full,
  the next server goes into `pgdock-<region>-2`, and so on. Only servers in
  the same group are known to be apart, so two servers in different groups
  count as possibly together. The node page shows the group.
  `PGDOCK_HETZNER_PLACEMENT_GROUP_ID`, if set, stays the home region's group.
- **Neither:** the node is its own domain, as it was before V3.1. Nothing
  changes for an install that never records a domain.

The Nodes list shows each node's domain.

## Checking what exists

Changing a domain never moves anything. Instead, PGDock checks every HA pair,
the etcd cluster and each region's pooler pair against the rule, and:

- shows a banner on Platform → Nodes listing the groups that share a domain;
- raises a `failure_domain` warning alert per group, which resolves once the
  group is apart again;
- returns them from `GET /api/v1/admin/failure-domains`.

To fix one, either correct the domains (if they were entered wrong), or move
the member:
- an HA member: turn HA off and on again (the standby is placed by the
  rule), switching over first if it's the primary that has to move;
- etcd: V3.1-M2's member replacement;
- a pooler host: add a host in another rack and remove the old one.

## Before relying on it

The labels are only as good as what was typed. On a real site, check them
against the racks: power off one rack (or pull its uplink) during the
[Lagos launch rehearsals](lagos-launch.md), and confirm that every HA project
keeps a running member.
