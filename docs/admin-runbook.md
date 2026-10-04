# Platform admin runbook

The tasks of whoever runs a PGDock installation for other people, in the
order they come up. [Operations](operations.md) explains each feature;
this page is what to do, and when. Incidents have their own page:
[incident process](incidents.md).

## Before inviting anyone

0. **Have two platform admins.** Setup makes one. Invite a second person you
   trust, then Users → **Make admin** (your password and a code). Otherwise
   losing one phone and its recovery codes leaves nobody able to manage the
   platform (the server-side recovery below still works, but it needs a
   shell on the server).
1. **Install and harden** ([install](install.md)): TLS on the poolers,
   off-host backups, the backup key exported and stored apart from the
   server, SMTP working (sign-up, invitations and every alert need it).
2. **Check isolation** (Settings → Tenant isolation → Run now). Every check
   must pass; it then runs nightly and alerts on failure.
3. **Read the terms** (Platform → Terms) and replace the default text with
   your own ([terms template](terms-template.md)): who you are, how to reach
   you, how long you take to tell people about incidents. Publishing a new
   version makes everyone accept it again.
4. **Sign-up mode** (Platform → Sign-up): keep **invite-only** for the beta.
5. **Plans** (Platform → Plans): the defaults are *Personal* (new personal
   organisations), *Team*, and *Unlimited* (yours). Lower them if your
   servers are small: storage and connections per project matter most.
6. **Outbound traffic** (V2 §10.7): webhooks and HTTP jobs reach only
   public `https://` addresses. If your servers can reach private networks
   or a metadata service on unusual ranges, add them to
   `PGDOCK_OUTBOUND_BLOCK`. Allow-list internal hosts for an organisation
   only when you trust it (Organisations → the org → Outbound).
7. **An external review.** Before anyone outside your own projects relies
   on it, have someone else look at tenant isolation, or pay for a short
   penetration test ([security review](security-review.md)).

## The invite-only beta

Two or three friends' organisations for two weeks, before inviting more
people (V2 §14 M16):

- Invite people (Platform → Users → Invite); each gets a personal
  organisation on the *Personal* plan. Move a team to *Team* (Organisations
  → the org → Plan) when they ask for more.
- Watch **Alerts**, **Usage** (Platform → Usage) and the platform audit log
  daily. Storage locks, reaped statements and dead-lettered webhooks show
  where people hit limits.
- Ask the testers to try a restore of their own (Backups → Restore into a
  new project) and to download one backup as a file (owners: Backups →
  Download), so they know their data can leave.
- Note every question they ask: each is a missing sentence in the
  [user guide](user-guide.md).
- At the end: fix what broke, then tag the release.

## Daily

- **Alerts.** Each alert names the project or node; [operations](operations.md#health-at-a-glance)
  lists what fires when. A failed backup or restore test comes first.
- **Requests** (Platform → Dedicated requests): approve within the node
  capacity you have, or reject with a reason.

## Weekly

- **Capacity** (Nodes, and the [capacity notes](operations.md#capacity)):
  add a shared node before one passes about 200 projects, its disk 70%,
  or its peak client backends 60% of `max_connections`. The
  [load check](load-test.md#v2-load-check) peaked at 311 of 500 backends
  with 150 busy projects on each of two nodes.
- **Restore test** results (Settings → Backup checks).
- **Dependencies:** `govulncheck` and `npm audit` run on every push in CI;
  upgrade when they report something reachable ([upgrades](upgrade.md)).

## Accounts

| Situation | Do |
| --- | --- |
| Someone lost their authenticator and recovery codes | Confirm who they are out of band (a call, not the email that asks), then Users → the user → **Reset two-factor**. It is audited and they are emailed. |
| A colleague should help run the platform | They need an active account with two-factor set up. Users → **Make admin**. It asks for your password and a code, signs them out, emails them, and is audited. **Remove admin** reverses it; the last admin can't be removed. |
| Nobody can sign in as a platform admin | On the server: `docker compose exec pgdock-server pgdock-server admin list`, then `promote <email>`, `reset-2fa <email>` or `reset-password <email>` (prints a one-hour link when email is down). Audited as done on the server. See [operations](operations.md#platform-admins-and-recovery). |
| Someone leaves a team | The team's owners remove them; it's immediate. You don't need to do anything. |
| An account is abused or compromised | Users → **Disable**: sessions, tokens and database logins end at once. |
| A user wants their account deleted | They do it from Account; it is refused while they're the last owner of an organisation with projects. |

## Organisations

| Situation | Do |
| --- | --- |
| A team needs more | Plan, or a single override (Organisations → the org → Limits). |
| A team needs isolation from others | Give it its own shared cluster (Organisations → the org → Shared cluster). |
| Abuse through webhooks or jobs | **Outbound off** (Organisations → the org → Outbound): its databases keep working. The per-host counters show where the traffic went. |
| Abuse, non-payment, or a security incident | **Suspend** with a reason: apps can't connect, background work stops, a final backup is taken, owners are emailed. **Reinstate** undoes it. |
| A support case needs a look inside | **Break-glass** (Organisations → the org): a reason, at most 4 hours, step-up auth. Owners are emailed and see a banner; everything is in their audit log. End it as soon as you're done. |

## Backups and keys

- The **backup key** (Settings → Backup key) decrypts every backup on
  platform targets. Keep its export offline; without it, a lost server
  means lost backups.
- The **master key** (`PGDOCK_MASTER_KEY`) seals credentials; rotate it with
  `pgdock-server -rotate-master-key` ([operations](operations.md#secrets)).
- A full rebuild of the control node: [disaster recovery](disaster-recovery.md).

## Releases

Upgrade with [upgrades](upgrade.md): agents on other nodes first (an agent
on an older minor version than the server is refused work), then the server
and its bundled agent, one node at a time. Read the changelog's *Security* notes before the rest.
