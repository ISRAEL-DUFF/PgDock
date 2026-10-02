# PGDock — V2 Specification & Build Plan

*Builds on the V1 spec. Section references like "V1 §6.6" point to that document.*

|  |  |
| --- | --- |
| **Status** | Draft v2 — revised for organisations and multi-tenancy |
| **Scope** | Seven workflow features plus organisations, multi-tenant hardening, quotas, and usage recording (see §1.2) |
| **Builds on** | V1: shared/dedicated tiers, edge pooler, operations queue, backups, import, promotion, embedded React UI, OpenAPI contract |
| **Audience** | You, plus friends and colleagues running their own projects. **Designed as a public multi-tenant platform**; launches invite-only, with no billing. |

---

## 1. Overview

### 1.1 What V2 is for

V1 made PGDock a place to create, back up, and promote databases for one person. V2 turns it into a **multi-tenant platform** that friends and colleagues can use for their own projects, and makes it a place to **work** with databases day to day:

- Host other people's projects safely, each inside their own **organisation**.
- Bring collaborators onto an organisation or onto specific projects.
- Edit data and schema without leaving the browser.
- Script and automate PGDock from a terminal and from CI.
- Spin up throwaway copies of a database for development and testing.
- React to data changes and run recurring tasks without a separate server.
- Move projects back down to the shared tier when they no longer need dedicated resources.
- Keep backups in a bucket the organisation owns.
- Record usage per organisation from day one, so billing can be added later without guesswork.

### 1.2 V2 features

| # | Feature | Section |
| --- | --- | --- |
| 1 | Organisations, roles, and the platform admin boundary | §2 |
| 2 | Users, signup, invitations, and per-project access | §3 |
| 3 | Visual table editing (rows + schema) | §4 |
| 4 | Demotion: dedicated → shared | §5 |
| 5 | Backup storage targets (platform + bring-your-own per org) | §6 |
| 6 | CLI and API tokens | §7 |
| 7 | Database branching | §8 |
| 8 | Database webhooks and scheduled jobs | §9 |
| 9 | Multi-tenant hardening, quotas, and usage recording | §10 |

### 1.3 Deferred (not in V2)

**Billing and payments** (usage is recorded, but nothing is priced or charged); self-serve dedicated instances without approval; SSO/SAML; auth, auto-generated REST, storage, edge functions, realtime; automatic cloud VM provisioning; multiple Postgres major versions; logical-replication (zero-downtime) moves; HA for dedicated instances; a standby edge pooler; query insights. These stay on the roadmap (§15).

### 1.4 Design principles

Carried from V1:

- **Every long action is an operation** (V1 §6): queued, logged, streamed, resumable, audited.
- **Stable connection strings.** No V2 feature changes a project's URL, including the V1→V2 naming migration (§10.2).
- **The API is the product.** The UI and the new CLI both use the same OpenAPI-generated `/api/v1`.
- **Least privilege by default.** Editing, webhooks, and jobs run with the project's own permissions, never `pgdock_admin`'s.

New in V2:

- **Tenants are untrusted.** The shared tier is designed as if any tenant could be careless or hostile (§10.1).
- **The platform admin is not a tenant superuser.** Running the platform doesn't grant routine access to other organisations' data (§2.4).
- **Fail closed across tenants.** A resource in another organisation returns `404`, never `403`, so its existence doesn't leak.

---

## 2. Organisations & Permissions

### 2.1 Tenancy model

```
Platform (one PGDock installation, run by the platform admin)
 └── Organisations
      ├── Members (users with an org role)
      ├── Projects ── branches, webhooks, jobs, members with project roles
      ├── Storage targets (bring-your-own buckets)
      ├── Quotas & usage
      └── Audit log
```

- A **user** is a person with one login. A user can belong to **many organisations**.
- An **organisation** owns everything tenant-related: projects (and their branches, webhooks, jobs), org storage targets, quotas, usage records, and the org audit log. API tokens belong to a user but are **scoped to one organisation** (§7.2).
- Every user gets a **personal organisation** at signup (named "'s projects", renamable). It's an ordinary organisation: its owner can invite others into it. It can't be deleted while its owner's account exists, only emptied.
- Projects can be **transferred** between organisations by someone who is an owner of both (operation: re-tag ownership, move memberships, revoke tokens scoped to the old org).

### 2.2 Roles

| Layer | Roles | Stored on |
| --- | --- | --- |
| Platform | `platform_admin`, `user` | `users.platform_role` |
| Organisation | `owner`, `admin`, `member` | `org_members.role` |
| Project | `admin`, `developer`, `read_only` | `project_members.role` |

- **Org owners and admins** act as project `admin` on **every** project in the org implicitly.
- **Org members** see only projects they've been added to, with the project role they were given. If the org setting *Members can create projects* is on (default **on**), a member can create projects in the org and becomes that project's admin.
- **Org owner vs admin:** both manage members, projects, storage targets, and settings. Only owners can delete the org, transfer projects out, add or remove other owners, and change who holds the owner role. An org must always keep **at least one owner**.

### 2.3 Permission matrix (inside an organisation)

| Action | Org owner | Org admin | Project admin | Developer | Read-only |
| --- | --- | --- | --- | --- | --- |
| View project, metrics, operations | ✓ | ✓ | ✓ | ✓ | ✓ |
| Get personal DB credentials (§3.5) | ✓ | ✓ | ✓ | ✓ (read/write) | ✓ (read-only) |
| SQL console — read | ✓ | ✓ | ✓ | ✓ | ✓ |
| SQL console — write (if project toggle allows) | ✓ | ✓ | ✓ | ✓ | — |
| Table editor — rows and schema | ✓ | ✓ | ✓ | ✓ | — |
| Create / reset / delete branches | ✓ | ✓ | ✓ | ✓ | — |
| Manage webhooks and scheduled jobs | ✓ | ✓ | ✓ | ✓ | — |
| Create backup, restore into new project | ✓ | ✓ | ✓ | ✓ | — |
| Restore in place | ✓ | ✓ | ✓ | — | — |
| Rotate app password, settings/guardrails, extensions | ✓ | ✓ | ✓ | — | — |
| Manage project members | ✓ | ✓ | ✓ | — | — |
| Choose project's storage target, download backup key | ✓ | ✓ | ✓ | — | — |
| Promote (within allowance or by request) / demote | ✓ | ✓ | ✓ | — | — |
| Delete project | ✓ | ✓ | ✓ | — | — |
| Create projects, import | ✓ | ✓ | members if org setting allows |  |  |
| Manage org members and invitations | ✓ | ✓ | — | — | — |
| Org storage targets, org settings | ✓ | ✓ | — | — | — |
| View org usage and quotas, org audit log | ✓ | ✓ | — | — | — |
| See and revoke any token scoped to the org | ✓ | ✓ | — | — | — |
| Add/remove owners, delete org, transfer projects out | ✓ | — | — | — | — |

### 2.4 The platform admin

The platform admin runs the installation. Their powers are **platform-level only**:

| Can | Cannot (without break-glass) |
| --- | --- |
| Manage nodes, platform storage targets, platform settings, signup mode, terms text | Open the SQL console, table editor, or backups of any org's project |
| List organisations and users; see org names, member counts, project counts, statuses, sizes, usage, and aggregate metrics | See connection strings, personal credentials, webhook URLs/secrets, job SQL, or audit details inside an org |
| Assign quota plans and dedicated allowances; approve dedicated requests (§10.6) | Act as a member of an org they don't belong to |
| Suspend and reinstate organisations (§10.8); disable users |  |
| Invite users (when signup is invite-only) |  |

**Break-glass access.** For support or incident response, the platform admin can request access to one organisation:

1. Enter a reason and duration (maximum **4 hours**). Requires step-up reauth.
2. PGDock grants a temporary org-admin role on that org, recorded in a `break_glass_sessions` row.
3. **Every org owner is emailed immediately**, a banner is shown to everyone in the org while the session is active, and every action taken is written to the **org's** audit log flagged as break-glass, as well as to the platform audit log.
4. Any org owner can **end the session early**. It also ends automatically at expiry.

**Honesty note for the terms of use:** someone with root access to the servers can technically read any data. Break-glass is a **policy and audit control**, not a cryptographic one. The terms page (§10.10) says so plainly.

The platform admin can still belong to organisations normally (e.g. their own personal org) and has ordinary roles there.

### 2.5 Actors

Every request is made by one of:

- **Session** (browser), with step-up reauth for destructive actions.
- **API token** (CLI, CI, scripts), scoped to one org (§7.2).
- **System** (scheduler, quota enforcer, expiry jobs).

Audit rows record the actor kind, token ID where relevant, and a `break_glass` flag.

### 2.6 Enforcement

- **One `authz` package** with `Can(actor, action, resource)`. Every tenant resource resolves to an `org_id` before the check. A table-driven test covers the full matrix in §2.3 and the platform admin table in §2.4 against every API route, and CI fails if a route doesn't declare its action.
- **Defence in depth at the data layer.** Every `sqlc` query on a tenant table takes `org_id` as a parameter and filters by it, even when the handler has already checked permissions. A CI lint flags tenant-table queries without an `org_id` predicate.
- **No existence leaks.** Resources in organisations the actor can't see return `404`. Project IDs are UUIDs, so they can't be guessed sequentially.

### 2.7 Audit logs

- **Org audit log:** every action on the org and its resources, visible to org owners and admins.
- **Project audit log:** the project's slice of the org log, visible to project admins.
- **Platform audit log:** platform-level actions (quota changes, suspensions, approvals, break-glass start/end), visible to the platform admin. Break-glass actions appear in both.

---

## 3. Users, Signup & Access

### 3.1 Signup modes

A platform setting with three modes:

| Mode | Behaviour |
| --- | --- |
| **Invite-only** (default) | Accounts are created only by accepting an invitation, from the platform admin or from an org. |
| **Approval required** | Anyone can sign up; the account is inactive until the platform admin approves it. Optional email-domain allow-list. |
| **Open** | Anyone can sign up. Optional email-domain allow-list, per-IP rate limits, and an optional Cloudflare Turnstile challenge. |

In every mode:

- **Email verification is mandatory** before the account can do anything. **SMTP is therefore required** in V2 (it was optional in V1). The setup wizard and the platform settings page enforce a working SMTP test.
- **TOTP is mandatory**, enrolled before first use, with **10 single-use recovery codes** shown once.
- The user must accept the current **terms of use and privacy notice** (§10.10). The accepted version and timestamp are recorded.

### 3.2 Invitations

- **Org invitation:** an org owner or admin enters an email, an org role, and optionally project memberships with project roles. Project admins can invite to their project only (the invitee joins the org as a `member` with that project role).
- **Platform invitation:** the platform admin invites someone to the platform (in invite-only mode). They get an account and a personal org, but no access to anyone else's org.
- Invitations are single-use, valid for **7 days**, delivered by email with a copyable link as a fallback, and revocable before acceptance.
- If the email already belongs to a user, accepting adds the membership. Users also see pending invitations in the app.

### 3.3 Managing members

- **Org → Members:** members with org role, project memberships, 2FA status, and last activity; invite, change role, remove; transfer ownership.
- **Project → Members:** project members with role; invite; change role; remove. Org owners and admins are listed as implicit admins.
- Users can **leave** an organisation themselves, unless they are its last owner.

### 3.4 Removing access

Removing a user from a project, from an org, or disabling a user takes effect immediately:

1. Delete the membership(s) or mark the user disabled.
2. Revoke the user's sessions (for org removal: only their ability to act in that org) and every API token scoped to that org.
3. Drop the user's **personal database roles** on the affected projects (§3.5) after terminating their connections.
4. Write audit entries.

### 3.5 Personal database credentials

Each member gets **their own database login** per project, so removing a member never requires changing the app's password.

- On first request ("Get my credentials"), PGDock creates a login role named `<db>_u_<6 random chars>` (opaque, see §10.2), sets a random password, writes its SCRAM verifier to the pooler auth file, and shows the password once. The member can rotate it at any time.
- **Project admins and developers** (and org owners/admins) get membership in the project owner role (`GRANT <db>_owner TO <db>_u_…`).
- **Read-only** members get membership in a per-project group role `<db>_ro`, which PGDock maintains:

  ```sql
  CREATE ROLE p_7f3k9x2m4q_ro NOLOGIN;
  GRANT CONNECT ON DATABASE p_7f3k9x2m4q TO p_7f3k9x2m4q_ro;
  GRANT USAGE ON SCHEMA public TO p_7f3k9x2m4q_ro;
  GRANT SELECT ON ALL TABLES IN SCHEMA public TO p_7f3k9x2m4q_ro;
  ALTER DEFAULT PRIVILEGES FOR ROLE p_7f3k9x2m4q_owner IN SCHEMA public
    GRANT SELECT ON TABLES TO p_7f3k9x2m4q_ro;
  ```

  The same grants apply to every non-system schema the project uses, and are re-applied when a schema is created through the table editor.
- Personal roles count against the project's connection limit and inherit its guardrails.
- Personal roles are carried across **promotion, demotion, restore, branch reset, and project transfer**: each operation recreates them with the same SCRAM verifiers.

The app password stays separate and works exactly as in V1.

### 3.6 Account security

- **Password reset** by emailed single-use link (valid 1 hour); resetting revokes all sessions.
- **Lost TOTP:** use a recovery code; with none left, the platform admin can reset 2FA after out-of-band identity confirmation (audited, and the user is emailed).
- **Sessions page:** list active sessions with device and IP; revoke any.
- **Account deletion:** blocked while the user is the last owner of any org that still has projects. Otherwise, memberships are removed, tokens revoked, personal DB roles dropped, and the personal org (if empty) deleted.

---

## 4. Visual Table Editing

V1's table browser is read-only. V2 adds **row editing** and **schema editing** as two distinct capabilities with different safety rules. Both are disabled when the project's console read-only toggle is on (V1 §8.5), and both execute as the project owner role via `SET ROLE` (or the read-only group role for read-only members viewing data).

### 4.1 Grid improvements (all roles)

- **Filtering:** per-column filters (equals, contains, range, is null, in list) compiled to parameterised `WHERE` clauses. Never string-concatenated.
- **Sorting** by any column; keyset pagination when sorting by a unique key, offset pagination otherwise (with a warning on large offsets).
- **Column display:** type-aware rendering for JSON/JSONB (collapsible), arrays, timestamps (shown in the user's time zone with UTC on hover), enums, UUIDs, and booleans. `bytea` shows size only.
- **Foreign keys:** clicking a foreign-key value opens the referenced row in a side panel.
- **Export** filtered results to CSV or JSON (capped at 100,000 rows; larger exports go through the SQL console or CLI).

### 4.2 Row editing

**Eligibility.** A table is editable only if it has a **primary key**. Views, materialised views, partitioned parent tables, and tables without a primary key are read-only, with an explanation in the UI.

**Editing UX.** Inline cell editing, an "edit row" side panel for wide rows, "add row", and multi-select delete. Pending edits are staged and highlighted; nothing is written until **Save**, which shows a summary ("3 updates, 1 insert, 2 deletes") and can be discarded.

**Type-aware inputs.** Text, numbers, boolean toggle, date/time pickers, UUID (with "generate"), enum dropdown, JSON editor with validation, array editor, and a **foreign-key picker** that searches the referenced table. Columns with defaults show the default and can be left empty on insert. Generated and identity columns are read-only.

**Conflict detection.** Each row loads with its `xmin` system column. Updates and deletes are written as:

```sql
UPDATE public.posts SET title = $1
WHERE id = $2 AND xmin::text = $3
RETURNING xmin::text, *;
```

If zero rows are affected, someone else changed or deleted the row since it was loaded. The UI shows a **conflict** with the current values and lets the user re-apply or discard. No silent overwrites.

**Atomicity.** One Save runs in **one transaction**. If any statement fails (constraint violation, conflict, trigger error), the whole batch rolls back and the failing row is highlighted with the Postgres error message.

**Guardrails.** `statement_timeout` 30s and `lock_timeout` 5s per save. Batch size limited to 500 changed rows per save.

### 4.3 Schema editing

Supported actions:

- Create, rename, and drop **tables** (with column definitions, primary key, and comments).
- Add, rename, drop **columns**; change type, default, nullability; add `CHECK` and `UNIQUE` constraints.
- Add and drop **foreign keys** with `ON DELETE`/`ON UPDATE` actions.
- Create and drop **indexes** (btree, gin, gist, brin; unique; partial with a `WHERE` clause; multi-column).
- Create and drop **schemas**; create **enum types** and add enum values.

**Every schema change previews its SQL** before anything runs. The preview panel shows:

1. The exact DDL to be executed.
2. **Risk notes** generated by a rules engine, for example:
   - `ALTER COLUMN … TYPE` that rewrites the table → "Rewrites the table and blocks writes. Estimated size: 2.3 GB."
   - `SET NOT NULL` on a large table → "Scans the whole table under an exclusive lock. Consider adding a `CHECK … NOT VALID` constraint and validating it separately."
   - `ADD COLUMN` with a volatile default → "Rewrites the table."
   - `CREATE INDEX` → generated as `CREATE INDEX CONCURRENTLY` by default (run outside a transaction) to avoid blocking writes.
   - Dropping a column or table → requires typing the object name.
3. Three choices: **Run**, **Copy SQL**, or **Save as migration**.

**Save as migration** downloads (or copies) a migration file in the chosen format: plain SQL, `goose` (`-- +goose Up` / `Down`), or `dbmate`. PGDock generates a best-effort `Down` section where the reverse is unambiguous (e.g. drop the added column) and leaves a `TODO` comment where it isn't (e.g. a dropped column's data). The project remembers each user's preferred format.

**Execution.** DDL runs with `lock_timeout` 5s so it fails fast rather than queuing behind long transactions and blocking everyone. Non-concurrent statements run in a transaction; concurrent index builds run alone. Every executed schema change is written to the audit log **with its SQL**, unlike console queries, because schema history is valuable and rarely contains secrets.

**After a schema change**, PGDock re-applies the read-only group role grants (§3.4) for any new tables or schemas.

### 4.4 Out of scope for V2

Editing views, functions, triggers, RLS policies, and partitioning through the visual editor; schema diffing between projects; automatic migration history tracking. These all remain available through the SQL console.

---

## 5. Demotion (Dedicated → Shared)

### 5.1 When to use it

A project that was promoted but no longer needs dedicated resources (a startup that paused, a demo that finished, a project now in maintenance mode) can move back to the shared tier to free a node or reduce cost. Like promotion, demotion **keeps the connection string and password unchanged**.

### 5.2 Eligibility checks

Demotion is blocked, with a clear explanation, if any of these fail:

| Check | Rule |
| --- | --- |
| Size | Database size ≤ the org's **per-project shared storage limit** (§10.3), and the org's total shared storage stays within quota after the move. |
| Extensions | Every installed extension is on the shared-tier allow-list (V1 §7.4). PostGIS, TimescaleDB, `pg_cron`, `postgres_fdw`, etc. block demotion. |
| Roles | No roles beyond the project owner, the `_ro` group, and members' personal roles. Custom roles created in SQL block demotion until removed. |
| Allowance | Demotion releases the project's share of the org's dedicated allowance (§10.6) once the old container is destroyed. |
| Connections | Peak connections over the last 7 days ≤ the shared-tier connection limit, or the user acknowledges a warning. |
| Settings | Non-default database-level settings the shared tier doesn't allow (e.g. a raised `statement_timeout`) are listed as **warnings**; the user confirms they will be reset to shared defaults. |
| Capacity | At least one eligible shared cluster has room for the database plus 20% headroom. If the org has its own shared cluster (§10.5), the project goes there. |

The checks run as a preflight (API: `POST /projects/:id/demote/preflight`) and again at the start of the operation.

### 5.3 Flow

Mirrors promotion (V1 §6.6) in reverse:

1. The user picks a target shared cluster (default: most free capacity). The UI shows estimated downtime from database size.
2. Create the database and roles on the shared cluster, with the **same SCRAM verifiers** for the owner role and every member's personal role. Apply the shared-tier hardening from V1 §6.1.
3. **Freeze:** `ALLOW_CONNECTIONS false` on the dedicated instance, `PAUSE` the pooler route, terminate sessions.
4. `pg_dump -Fc` from dedicated → `pg_restore` into shared.
5. **Verify** row counts and sequence values.
6. Switch the pooler route to the shared cluster, `RELOAD`, `RESUME`.
7. Take an immediate logical backup on the shared tier.
8. **Stop** the dedicated container but keep its volume for **48 hours** as a rollback option, then destroy it automatically.

**Rollback** before step 6: re-enable connections on the dedicated instance, resume the route, and drop the new shared database.

### 5.4 Backups after demotion

- The project switches from WAL-G continuous backups to nightly logical backups.
- **The point-in-time recovery window ends at demotion.** Existing WAL-G base backups and WAL stay restorable until their normal retention expires, and remain visible in the backups tab labelled "dedicated (pre-demotion)".
- The UI states this clearly in the demotion confirmation.

### 5.5 Settings changes

The console read-only toggle returns to the shared-tier default (**off**) only if the user ticks a box; otherwise it keeps its current value. Guardrails (connection limit, timeouts, pool size) reset to shared-tier defaults, shown in the confirmation step.

---

## 6. Backup Storage Targets

### 6.1 Two kinds of target

- **Platform targets** (managed by the platform admin): available to every org. Exactly one is the platform default, and every project uses it unless told otherwise. Backup storage on platform targets **counts against the org's backup quota** (§10.3).
- **Org targets** (managed by org owners and admins): a bucket the organisation brings itself, usable by any project in that org. Storage there **doesn't count** against the backup quota, because the org pays for it. This is how a team or startup keeps its backups in an account it controls.

Adding or editing a target runs a **live test**: write, read, list, and delete a small object under the target's prefix. The target can't be saved until the test passes. Credentials are encrypted with the master key and never shown again. Org targets are invisible to other orgs and to the platform admin (who sees only that a project uses "an org target").

### 6.2 Choosing a project's target

**Project → Backups → Storage** lets a project admin pick the platform default or any of the org's targets. On switching:

- **New backups** go to the new target from that point.
- **Existing backups** stay where they are and remain restorable, because each `backups` row records its own `storage_target_id`.
- Optionally, **copy existing backups** to the new target as an operation. Copies are verified by checksum; originals are deleted only if the user chooses that explicitly.

For dedicated projects, switching reconfigures WAL-G and takes a fresh base backup immediately, so the new target holds a complete, restorable chain.

### 6.3 Per-project backup encryption keys

So an organisation can decrypt backups in its own bucket without PGDock's master key:

- An optional **per-project backup key**, generated when a project admin enables it. New backups for that project are encrypted with it.
- The project admin can **download the key** (with step-up reauth), together with a short README on decrypting and restoring with standard tools.
- Existing backups keep the key they were encrypted with; each `backups` row records which key, so restores always work.
- Key downloads are audited.

### 6.4 Deleting targets

A target can't be deleted while any project uses it for new backups, or while it holds unexpired backups, unless the deleter explicitly accepts that those backups become unrestorable from PGDock. Deleting an org (§10.10) never touches org targets: their contents remain the org's property.

---

## 7. CLI and API Tokens

### 7.1 The `pgdock` CLI

A Go binary built from the same monorepo (`cmd/cli`) using the OpenAPI-generated client, released alongside the server for `linux`, `darwin`, and `windows` on `amd64` and `arm64`.

**Configuration** lives in `~/.config/pgdock/config.toml` with named **contexts**, so one CLI can talk to more than one PGDock instance:

```toml
current = "home"
[contexts.home]
server = "https://pgdock.yourdomain.com"
org    = "acme"            # org the token is scoped to
token  = "pgd_…"           # or token_cmd = "pass show pgdock/home"
```

The token can also come from the `PGDOCK_TOKEN` environment variable, which takes priority (for CI).

**Login.** `pgdock login --server https://…` uses a **device-authorisation flow**: the CLI prints a short code and opens the browser; the user approves it in the web UI (session + TOTP already established), **choosing the organisation and scopes**; PGDock issues a token scoped to that org and returns it to the CLI. Logging into another org creates another context. Pasting a token manually is also supported.

**Commands (V2)**

```
pgdock login | logout | context list | context use <name> | whoami
pgdock orgs list | create <name>                       # orgs you belong to
pgdock org members | invite <email> --role <r> | remove <email> | usage | quotas

pgdock projects list | info <p> | create <name> [--tier] | delete <p> --confirm <p>
pgdock connect <p> [--pooled|--session] [--psql]      # print URL with personal creds, or launch psql
pgdock creds <p> [--rotate]                            # personal DB credentials (§3.4)

pgdock sql <p> -c "select …" | -f file.sql [--json|--csv]

pgdock branch list <p> | create <p> <name> [--from backup|live] [--schema-only] [--ttl 72h] [--env]
pgdock branch reset <branch> | delete <branch>

pgdock backup list <p> | create <p> | restore <p> --backup <id> [--into <new-name>]
pgdock promote <p> --node <n> | demote <p> [--node <n>]

pgdock webhooks list|create|delete|deliveries|replay …
pgdock jobs list|create|pause|resume|run|history …

pgdock members list <p> | invite <p> <email> --role <r> | remove <p> <email>   # project members
pgdock tokens list | create --name … --scopes … [--project <p>] [--expires 90d] | revoke <id>

pgdock operations get <id> [--follow]
```

**Behaviour conventions**

- Every command supports `--json` for scripting; human output is the default.
- Long operations stream progress by default (`--no-wait` returns the operation ID immediately).
- Destructive commands require `--confirm <name>` matching the target, the CLI equivalent of the UI's typed confirmation.
- Exit codes: `0` success, `1` error, `2` usage error, `3` operation failed, `4` permission denied.
- `--env` on `branch create` prints `DATABASE_URL=…` lines for direct use in CI (`pgdock branch create … --env >> $GITHUB_ENV`).

### 7.2 API tokens

**Format.** `pgd_` + 32 random bytes, base62-encoded. The prefix makes tokens easy to detect in secret scanners. PGDock shows the token **once** and stores only its SHA-256 hash (tokens are high-entropy, so a slow hash isn't needed).

**Properties**

| Field | Notes |
| --- | --- |
| Name | Free text, e.g. "laptop", "GitHub Actions — blog". |
| Scopes | `read` (view everything the user can view in the org), `write` (create/modify: branches, rows, webhooks, jobs, backups), `admin` (destructive and settings actions: delete, promote/demote, restore in place, members, storage). Scopes are additive; `admin` requires `write`. |
| Organisation | **Required.** A token acts in exactly one org. A user who belongs to several orgs creates one token per org. |
| Project restriction | Optional list of projects within that org. A restricted token can't see or act on anything else, even if its user can. |
| Expiry | Required. Default 90 days, maximum 1 year. The platform admin can set a platform-wide maximum. |
| Last used | Timestamp and IP, updated at most once a minute. |

**Rules**

- A token can never exceed its user's permissions in its org, and loses permissions immediately when the user does (including on removal from the org).
- Step-up reauth (browser) doesn't apply to tokens; instead, destructive actions require the `admin` scope **and** an explicit `confirm` field in the request body matching the target name.
- Bearer-token requests skip CSRF checks (no cookies involved) and are rate-limited per token.
- Users manage their own tokens under **Account → Tokens**. Org owners and admins can see and revoke **any token scoped to their org**. The platform admin can't see tokens, but suspending an org (§10.8) disables all of its tokens.
- An email (if SMTP is configured) goes to the user when a token is created and 7 days before it expires.

---

## 8. Database Branching

### 8.1 What a branch is

A **branch** is a new project on the **shared tier** that starts as a copy of a parent project. It has its own database, credentials, and pooler route, and appears in the UI under its parent. Typical uses: trying a risky migration, a feature-branch environment, a per-pull-request database in CI, or debugging against realistic data.

### 8.2 Creating a branch

Options:

| Option | Choices | Default |
| --- | --- | --- |
| Source | **Latest backup** (no load on the parent; data as of last backup) or **Live** (fresh `pg_dump` of the parent now) | Latest backup |
| Contents | **Full** (schema + data) or **Schema only** | Full |
| Lifetime | Keep until deleted, or auto-delete after a TTL (1h–30d) | 7 days |
| Name | Free text; database name becomes `<parent-db>__<branch-slug>_<rand>` | — |

Flow:

1. Check the parent's size against the org's per-project shared storage limit, and the org's branch and total-storage quotas (§10.3). Branches always live on the shared tier (on the org's own shared cluster if it has one), even when the parent is dedicated.
2. Create the branch project and roles as in V1 §6.1. The branch gets **its own app password** (shown once) and members' personal roles are created on demand as usual.
3. Restore from the chosen source (backup restore or `pg_dump | pg_restore` from the parent, schema-only if selected), then apply the branch's extensions from the parent's list.
4. **Webhooks and scheduled jobs are not copied** to the branch (a copy firing real webhooks or jobs would be surprising and possibly harmful). The UI notes this.
5. Show the connection strings.

Branch creation uses the same machinery as V1's restore-into-new-project, so a branch from a backup is effectively a named, parented, auto-expiring restore.

### 8.3 Reset

**Reset from parent** re-creates the branch's data from a fresh source (latest backup or live) while keeping the branch's **database name, URL, app password, and members' personal credentials unchanged**. CI pipelines and local `.env` files keep working.

Flow: pause the branch's route → drop and recreate the database with the same roles and verifiers → restore → resume.

### 8.4 Limits and housekeeping

- **One level deep** in V2: a branch can't have branches of its own.
- **Branch quota per org** (from the org's plan, §10.3), and branches count toward the org's total shared storage. Branch-hours are recorded as usage (§10.9).
- Branches **count toward shared-node capacity** and appear in node metrics.
- **No backups** for branches by default (they're disposable); a project admin can turn nightly backups on for a branch.
- An hourly job deletes expired branches. A branch's TTL can be extended from the UI or CLI before it expires; 24 hours before expiry, the creator gets an email (if SMTP is set up).
- **Deleting a parent** requires deleting or detaching its branches first. **Detaching** turns a branch into an ordinary standalone project.
- Branches cannot be promoted. To keep a branch long-term, detach it first; it then behaves like any shared project and can be promoted.

### 8.5 Data sensitivity

Branches copy real data. V2 doesn't include data masking (see §17 open questions); instead, the create dialog defaults to **schema-only** for any project marked as **"contains sensitive data"** (a new project setting, which org owners/admins can also make the default for new projects in the org), and full-data branches of such projects require the project admin role.

---

## 9. Database Webhooks and Scheduled Jobs

### 9.1 Database webhooks

**What it does.** When rows in chosen tables are inserted, updated, or deleted, PGDock sends an HTTP POST to a URL with the change.

**Configuration** (UI, CLI, or API):

| Field | Notes |
| --- | --- |
| Name |  |
| Table(s) | One or more tables in the project. |
| Events | Any of `INSERT`, `UPDATE`, `DELETE`. |
| Columns (optional) | For `UPDATE`, fire only when one of these columns changed. |
| URL | `https://` required (plain `http://` allowed only to an explicitly allow-listed host, for local testing). |
| Headers | Optional static headers (e.g. an auth header for the receiver); stored encrypted. |
| Signing secret | Generated by PGDock, shown once, rotatable. |
| Enabled | Pause and resume without deleting. |

**Mechanics: transactional outbox**

1. Enabling the first webhook on a project installs a `pgdock` schema in the project database, **owned by `pgdock_admin`**, containing:
   - `pgdock.webhook_outbox (id bigserial, webhook_id uuid, table_name text, op text, old_row jsonb, new_row jsonb, created_at timestamptz)`.
   - A `SECURITY DEFINER` trigger function, owned by `pgdock_admin`, that inserts into the outbox and calls `pg_notify('pgdock_webhooks', '')`. Its `search_path` is pinned to prevent hijacking.
2. For each configured table, PGDock creates an `AFTER INSERT/UPDATE/DELETE … FOR EACH ROW` trigger calling that function, with a `WHEN` clause for the column filter.
3. Because the outbox insert happens **inside the same transaction** as the change, rolled-back changes never produce webhooks, and committed changes always do.
4. **Delivery worker** (in `pgdock-server`): for each project with active webhooks, holds one `LISTEN pgdock_webhooks` connection to wake up immediately, plus a **5-second poll** as a fallback. It reads outbox rows in order with `FOR UPDATE SKIP LOCKED`, delivers them, records the result, and deletes delivered rows.
5. The project role has no write access to the `pgdock` schema. It can, as the database and table owner, drop the triggers on its own tables; PGDock detects missing triggers during a periodic check and flags the webhook as **broken** in the UI rather than silently re-creating them.

**Payload**

```json
{
  "id": "evt_…",
  "webhook": "orders-to-slack",
  "project": "blog_k2f9",
  "table": "public.orders",
  "type": "UPDATE",
  "record": { … new row … },
  "old_record": { … old row … },
  "committed_at": "2026-10-01T12:00:00Z"
}
```

Rows larger than **256 KB** serialised are sent with `record` omitted and `"truncated": true` plus the primary key, so the receiver can fetch the row itself.

**Signing.** Each request carries `PGDock-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "t.body">` and `PGDock-Event-Id`. The docs include a verification snippet for Go and Node, with a 5-minute timestamp tolerance against replays.

**Delivery guarantees**

- **At-least-once.** Receivers should de-duplicate on `PGDock-Event-Id`.
- **Ordered per webhook** in commit order; a failing event blocks later events for that webhook only (not other webhooks) until it succeeds or is dead-lettered.
- **Retries** on network errors, timeouts (10s), and `5xx`/`429`, with exponential backoff and jitter over about **24 hours**. `4xx` other than `429` fails immediately to the dead-letter list.
- **Dead letters:** visible in the UI with the response, and **replayable** individually or in bulk.
- After 50 consecutive failures, the webhook is **auto-paused** and the project admins and org owners are alerted.

**Delivery log.** Each attempt is recorded (status code, latency, response body truncated to 4 KB) and kept 7 days. The UI shows a per-webhook log with filters and a **Send test event** button.

**Outbox safety.** If the outbox for a project grows beyond 100,000 rows (e.g. the receiver is down for a long time), PGDock alerts. Paused webhooks stop enqueuing (their triggers are disabled), so a paused webhook can't fill the outbox.

**Rate limits.** Deliveries are limited per org (from the org's plan, §10.3 and §10.7). When an org hits its limit, deliveries queue rather than drop, and the org is notified if the backlog persists.

**SSRF protection.** PGDock must not become a way to reach internal services. Before each delivery, the worker resolves the hostname and **refuses** private, loopback, link-local, and cloud-metadata addresses (RFC 1918, `127.0.0.0/8`, `169.254.0.0/16`, `::1`, `fc00::/7`, and the node network ranges), connects to the **resolved IP it checked** (preventing DNS rebinding), and does not follow redirects. Only the **platform admin** can allow-list specific internal hosts, and only per org. Tenants can't.

### 9.2 Scheduled jobs

**What it does.** Runs a SQL statement, or calls an HTTP endpoint, on a schedule.

**Why not `pg_cron`.** `pg_cron` must be loaded cluster-wide and runs jobs from a single database, which doesn't fit the shared tier's per-project isolation. PGDock's own scheduler works identically on both tiers, runs with each project's own permissions, and needs no extension.

**Configuration**

| Field | Notes |
| --- | --- |
| Name |  |
| Schedule | Standard 5-field cron expression with a **time zone** (default: the creator's). The UI shows the next five run times in plain language. Minimum interval: 1 minute. |
| Type | **SQL** (a statement or script) or **HTTP** (method, URL, headers, optional body). |
| Timeout | Default 5 min for SQL, 30s for HTTP; maximum 1 hour. |
| Overlap | **Skip** if the previous run is still going (default), or **queue** one run. |
| Enabled | Pause and resume. |

**Execution**

- The scheduler runs inside `pgdock-server`, holding a Postgres **advisory lock** on the metadata DB so only one instance ever schedules (future-proofing for more than one server).
- **SQL jobs** connect through the admin connection and run `SET ROLE <project_owner>`, then execute the SQL in a transaction with the job's timeout as `statement_timeout`. On the shared tier, the shared-tier guardrails still apply.
- **HTTP jobs** use the same SSRF protection, signing, and per-org rate limits as webhooks (with a `PGDock-Job` header instead of `PGDock-Event-Id`).
- The number of jobs per org and the minimum interval come from the org's plan (§10.3).
- **Missed runs** (e.g. PGDock was down) are **not** caught up; the next scheduled time runs normally. The history shows the gap.
- Jobs on a project that is promoting, demoting, restoring, or resetting are **skipped** and recorded as such.

**History.** Each run records start, duration, status, rows affected (SQL) or status code (HTTP), and the error message if any. Kept for 30 days or the last 1,000 runs per job. **Run now** triggers a manual run. Three consecutive failures send an alert.

**Example uses:** nightly cleanup (`DELETE FROM sessions WHERE expires_at < now()`), refreshing a materialised view, recalculating aggregates, pinging an app endpoint to send daily digests.

### 9.3 Interaction with other features

| Event | Webhooks | Scheduled jobs |
| --- | --- | --- |
| Promotion / demotion | Outbox and triggers move with the dump; delivery pauses during the freeze and resumes after. | Skipped during the operation. |
| Restore into new project / branch | **Not** carried over; triggers are removed from the copy. | Not carried over. |
| Restore in place | Triggers re-installed from PGDock's config; outbox cleared (events from the restored past aren't replayed). | Continue. |
| Project deletion | Deleted with the project. | Deleted with the project. |

---

## 10. Multi-Tenant Hardening, Quotas & Usage

### 10.1 Threat model

V1 assumed every database on the shared tier belonged to you. In V2, other people's projects share clusters, nodes, the pooler, and outbound network access. PGDock assumes any tenant may be **careless** (runaway queries, a full disk) or **hostile** (probing other tenants, abusing outbound requests).

**Goals**

- No tenant can read, modify, or infer the contents of another tenant's data.
- No tenant can learn meaningful information about other tenants (project names, org names, member identities) from inside Postgres.
- No tenant can exhaust shared resources (disk, connections, CPU time) badly enough to take others down, and degradation is detected and contained quickly.
- PGDock's outbound network can't be used to attack internal services or flood external ones.
- The platform admin's access to tenant data is exceptional, visible, and audited (§2.4).

**Out of scope for V2:** a malicious platform admin with root on the servers; timing and cache side channels between tenants on the same cluster; network-level DDoS (handled at the Hetzner/Cloudflare layer). Tenants that need protection beyond the shared tier use a per-org shared cluster (§10.5) or a dedicated instance.

### 10.2 Opaque database and role names

Any Postgres role can list every database name in `pg_database` and every role name in `pg_roles`, and this can't be revoked without breaking ordinary clients. With V1's slug-based names (`acme_payroll_k2f9`), a tenant could read other people's project names.

**V2 naming**

- New databases are named `p_<10 random base32 chars>` (e.g. `p_7f3k9x2m4q`). Roles follow: `<db>_owner`, `<db>_ro`, `<db>_u_<6 chars>`.
- Friendly names, slugs, and descriptions live **only** in PGDock's metadata. Nothing descriptive is written into Postgres: no `COMMENT ON DATABASE`, no meaningful `application_name` from PGDock's own connections.
- Connection strings use the opaque name. The UI and CLI always show the friendly name alongside it.

**Migrating V1 projects without changing their URLs.** PgBouncer's `[databases]` entries can map a client-facing database name to a different backend database. The V2 migration renames each V1 backend **database** to an opaque name and adds a pooler alias, so the old name in existing connection strings still routes correctly:

```ini
; client still connects to "blog_k2f9"; the backend database is now opaque
blog_k2f9 = host=10.0.0.5 port=5432 dbname=p_7f3k9x2m4q
```

Each rename runs as an operation with a brief pause of that project's route, a smoke test through the pooler, and automatic rollback on failure.

**Role names are a different matter.** The role name is the username in the connection string, so renaming a V1 project's roles would change its URL. V1 projects therefore **keep their role names by default**, which means other tenants can see those names in `pg_roles`. These are your own projects, so the exposure is limited to you. Each V1 project offers a **Switch to opaque credentials** action that creates opaque roles (same permissions, new password), shows the new connection strings, keeps the old roles working for a grace period you choose (default 7 days), then drops them. New projects are fully opaque from the start, and never get a friendly pooler alias: PgBouncer returns different errors for an unknown database and a failed login, so friendly aliases would let tenants guess other projects' names.

**Activity visibility.** By default, any role can see other sessions' database name, role name, and some metadata in `pg_stat_activity` (but not their query text). With opaque names this reveals little. PGDock additionally **revokes `SELECT` on `pg_stat_activity` from `PUBLIC`** on shared clusters **if** the client-compatibility suite passes (psql, pgAdmin, DBeaver, TablePlus, Prisma, Drizzle, GORM, pgx, SQLAlchemy, node-postgres); tenants see their own sessions in the PGDock UI instead. If the suite fails, opaque names alone are the mitigation and the result is documented.

### 10.3 Quotas and plans

Every org has a **quota plan**. The platform admin defines plan templates and assigns them per org, and can override individual limits for one org.

**Default plan templates**

| Limit | Personal | Team | Unlimited |
| --- | --- | --- | --- |
| Projects (excluding branches) | 10 | 25 | — |
| Branches (total) | 10 | 25 | — |
| Total shared-tier storage | 5 GB | 25 GB | — |
| Per-project shared storage | 2 GB | 5 GB | — |
| Connections per project (backend) | 20 | 30 | — |
| Dedicated allowance (§10.6) | none (by request) | by request | as configured |
| Backup storage on platform targets | 10 GB | 50 GB | — |
| Webhook deliveries per minute | 60 | 300 | — |
| Scheduled jobs (total) / minimum interval | 20 / 5 min | 50 / 1 min | — |
| HTTP job runs per hour | 120 | 600 | — |
| Concurrent SQL console queries | 2 | 5 | — |
| Operations in flight (backups, branches, imports) | 3 | 5 | — |

New personal orgs get **Personal**. The platform admin's own org gets **Unlimited**. The defaults are editable.

**Enforcement**

- **Creation-time limits** (projects, branches, jobs, webhooks, targets): the API refuses with `409 quota_exceeded`, naming the limit, the current usage, and the maximum. The UI shows usage bars before the user hits a limit.
- **Rate limits** (webhooks, HTTP jobs, console queries, operations): token-bucket per org, enforced in `pgdock-server`. Excess work queues where that's safe (deliveries) and is refused where it isn't (console queries).
- **Storage limits** are enforced continuously (§10.4).

### 10.4 Enforced storage and resource limits on the shared tier

Postgres has no per-database disk quota and lets any role change most of its own session settings, so role-level defaults alone are **soft**. V2 adds controls a tenant can't switch off.

**Storage**

1. The metrics collector measures every project's size every 5 minutes.
2. At **90%** of the per-project limit (or org total), org owners and project admins are emailed and the UI shows a warning.
3. At **100%: soft lock.** PGDock sets `ALTER DATABASE … SET default_transaction_read_only = on` and terminates idle sessions so reconnecting clients pick it up. Writes fail with a read-only error, but a tenant can still explicitly open a read-write transaction to delete data.
4. At **120%** (or if the node's disk drops below its safety threshold): **hard lock.** PGDock revokes `CONNECT` from all of the project's login roles and terminates their sessions. The app is offline, but the **PGDock SQL console and table editor still work** (they connect as `pgdock_admin` and `SET ROLE`), so the team can delete data.
5. Deleting rows doesn't shrink the files until they're vacuumed, so over-quota projects get a **Reclaim space** action that shows the largest tables and runs `VACUUM FULL` on a chosen table (with a warning that it locks the table) or `pg_repack` where installed.
6. Once the size is back under 100%, locks lift automatically.

**Temporary files.** `temp_file_limit` is set per project role by PGDock (default 2 GB on shared). It's a superuser-only setting, so a tenant can't raise it, and it stops a single query from filling the disk with sort or hash files.

**Long-running statements.** Because `statement_timeout` is user-settable, PGDock runs a **reaper** every 15 seconds on each shared cluster that cancels (`pg_cancel_backend`) any tenant statement running longer than the hard limit (default **10 minutes**) and terminates sessions idle in a transaction longer than **5 minutes**. Reaped statements are logged for the project and shown in its activity view. Dedicated instances are exempt.

**Connections.** Per-role `CONNECTION LIMIT` (superuser-set) plus PgBouncer `max_db_connections` per project, so a tenant can't open more backend connections by creating extra roles.

**Known limit.** Settings like `work_mem` are user-settable, so a determined tenant can still use a lot of memory or CPU and slow others down. PGDock limits the blast radius (a memory cgroup on each shared cluster so the node survives; the reaper; per-org metrics that make the offender obvious) and responds with suspension or by moving the org to its own cluster. Full performance isolation is what dedicated instances are for.

**Forbidden capabilities** (from V1 §7.1, now enforced by a nightly audit across all shared clusters): no untrusted languages, no `dblink`/`postgres_fdw`/`file_fdw`, no predefined roles that grant cross-database or filesystem access, no `SECURITY DEFINER` functions owned by anything other than tenant roles or the fixed PGDock webhook function. Drift is alerted to the platform admin.

### 10.5 Per-org shared clusters

The platform admin can give an organisation **its own shared cluster**: a regular PGDock shared-tier Postgres instance (V1 shared tier, same hardening) on any node, tagged with that org's ID. The org's new projects and branches are placed there, and no other org's projects ever are. Existing projects move with a per-project demote-style operation.

Use it for a colleague's company that wants stronger isolation from other tenants, or to contain a noisy org, without the cost of a dedicated instance per project.

### 10.6 Dedicated allowance and requests

Dedicated instances consume your node capacity, so they're controlled:

- Each org has a **dedicated allowance**: a number of instances and a resource budget (total vCPU, RAM, and disk). The default plans have none.
- Promoting (or creating a dedicated project) **within** the allowance runs immediately.
- **Beyond** the allowance, promotion creates a **dedicated request** with the requested profile and a reason. The platform admin approves or rejects it in the admin console; both sides are emailed. Approval runs the promotion as an operation and, optionally, raises the org's allowance.
- Dedicated instance-hours are recorded as usage (§10.9).

### 10.7 Outbound network controls

Webhooks and HTTP jobs are the only ways tenants can make PGDock send traffic:

- SSRF protection (§9.1) applies to both.
- Per-org rate limits and concurrency caps (§10.3).
- Per-org counters of destination hosts and request volumes, kept 30 days for abuse investigation (hosts and counts only, no payloads).
- The platform admin can **disable outbound traffic** for an org without suspending its databases.

### 10.8 Suspension

For abuse, non-payment in the future, or a security incident, the platform admin can **suspend** an organisation with a reason:

- All of the org's pooler routes are disabled and its login roles lose `CONNECT`, so apps can't connect.
- Webhooks, scheduled jobs, branch expiry, and scheduled backups pause (one final backup is taken at suspension). Tokens scoped to the org stop working.
- Org members can still log in to see the suspension notice and reason, and export their data if the platform admin allows it.
- Data is retained. Reinstating reverses every step. Suspension and reinstatement are recorded in both audit logs and emailed to org owners.

### 10.9 Usage recording

No billing in V2, but usage is recorded from day one so billing in V3 is a pricing exercise, not a data-archaeology project.

| Metric | Unit | Granularity |
| --- | --- | --- |
| Shared-tier storage | GB-hours (per project) | Hourly sample |
| Dedicated instance | vCPU-hours, GB-RAM-hours, GB-disk-hours (per instance) | Hourly |
| Branches | Branch-hours and branch GB-hours | Hourly |
| Backup storage on platform targets | GB-hours | Daily |
| Webhook deliveries | Count (attempts and successes) | Hourly |
| Scheduled job runs | Count (SQL and HTTP) | Hourly |
| Data transfer through the pooler | GB (from PgBouncer stats) | Hourly |

- Stored as `usage_records (org_id, project_id, metric, period_start, quantity)`. Hourly rows are rolled up to daily after 90 days and kept indefinitely.
- **Org → Usage** shows current-month totals and trends to org owners and admins, with CSV export. The platform admin sees per-org totals across the platform.
- Records are append-only and include the org's plan at the time, so historical usage can be priced correctly later.

### 10.10 Terms, data handling, and org deletion

**Terms and privacy.** The platform admin writes a terms-of-use page and a privacy notice in platform settings (versioned). Users accept the current version at signup, and again when a new version is published. The default template includes: what the platform admin can and can't access (§2.4), backup and retention behaviour, the absence of uptime guarantees, acceptable use, and how incidents are communicated.

**Data protection.** Tenants may store personal data in their projects. If any of that data is about people in Nigeria, the Nigeria Data Protection Act may impose obligations on whoever operates the platform. Get advice before opening the platform beyond friends and colleagues; PGDock's part is providing the controls below.

**Data export.** Org owners can export any project as a `pg_dump` file through the UI or CLI at any time (rate-limited to protect nodes).

**Incident process.** A short runbook in the docs: detection, containment (suspend, revoke, isolate), notification of affected org owners by email within a stated time, and a written follow-up.

**Org deletion.**

1. Only an org owner can delete; requires typing the org name and step-up reauth. All projects must be deleted, or the owner confirms deleting them all.
2. Every project gets a final backup; the org enters a **7-day grace period** during which an owner can cancel.
3. After the grace period: databases, dedicated instances, branches, webhooks, jobs, memberships, tokens, and org storage-target credentials are removed.
4. Final backups on **platform** targets are kept **30 days**, then purged. Backups on **org** targets are left untouched (they belong to the org).
5. Usage records are kept (without project names) for future billing reconciliation; audit logs are kept for 1 year.

---

## 11. Data Model Changes

New migrations on the metadata DB (all additive; no V1 data rewritten).

```sql
-- §2–3 Users and organisations
ALTER TABLE operators RENAME TO users;
ALTER TABLE users RENAME COLUMN role TO platform_role;
ALTER TABLE users DROP CONSTRAINT operators_role_check;
UPDATE users SET platform_role = 'platform_admin' WHERE platform_role = 'owner';
ALTER TABLE users
  ADD CONSTRAINT users_platform_role_check CHECK (platform_role IN ('platform_admin','user')),
  ADD COLUMN name             text,
  ADD COLUMN email_verified_at timestamptz,
  ADD COLUMN approved_at      timestamptz,     -- for 'approval required' signup mode
  ADD COLUMN recovery_codes   bytea;           -- encrypted, hashed codes

CREATE TABLE quota_plans (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text UNIQUE NOT NULL,             -- Personal | Team | Unlimited | custom
  limits     jsonb NOT NULL,                   -- keys per §10.3; null = unlimited
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE organizations (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name             text NOT NULL,
  slug             citext UNIQUE NOT NULL,
  personal_owner_id uuid UNIQUE REFERENCES users(id),  -- set for personal orgs
  plan_id          uuid NOT NULL REFERENCES quota_plans(id),
  limit_overrides  jsonb NOT NULL DEFAULT '{}',
  dedicated_allowance jsonb NOT NULL DEFAULT '{}',     -- instances, vcpu, mem_mb, disk_gb
  settings         jsonb NOT NULL DEFAULT '{}',        -- members_can_create_projects, sensitive_by_default, …
  status           text NOT NULL DEFAULT 'active',     -- active|suspended|deleting|deleted
  suspended_reason text,
  outbound_disabled boolean NOT NULL DEFAULT false,
  delete_after     timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE org_members (
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('owner','admin','member')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, user_id)
);

-- Ownership columns on existing tables (V1 data migrated into the platform admin's personal org)
ALTER TABLE projects  ADD COLUMN org_id uuid REFERENCES organizations(id);
ALTER TABLE projects  ADD COLUMN backend_db_name text;        -- opaque name; db_name stays the client-facing name
ALTER TABLE instances ADD COLUMN org_id uuid REFERENCES organizations(id);  -- per-org shared clusters (§10.5)
-- after backfill: ALTER TABLE projects ALTER COLUMN org_id SET NOT NULL;
CREATE INDEX ON projects (org_id);

CREATE TABLE project_members (
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role        text NOT NULL CHECK (role IN ('admin','developer','read_only')),
  added_by    uuid REFERENCES users(id),
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id)
);

CREATE TABLE invitations (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email        citext NOT NULL,
  token_hash   text UNIQUE NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('platform','org')),
  org_id       uuid REFERENCES organizations(id) ON DELETE CASCADE,
  org_role     text,
  project_roles jsonb NOT NULL DEFAULT '[]',   -- [{project_id, role}]
  invited_by   uuid NOT NULL REFERENCES users(id),
  expires_at   timestamptz NOT NULL,
  accepted_at  timestamptz, revoked_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE project_db_users (           -- personal DB credentials (§3.4)
  project_id     uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_name      text NOT NULL,
  scram_verifier text NOT NULL,
  access         text NOT NULL CHECK (access IN ('read_write','read_only')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  rotated_at     timestamptz,
  PRIMARY KEY (project_id, user_id)
);

-- §7 API tokens and device login
CREATE TABLE api_tokens (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name         text NOT NULL,
  token_hash   text UNIQUE NOT NULL,      -- sha256
  prefix       text NOT NULL,             -- first 8 chars, for display
  scopes       text[] NOT NULL,
  project_ids  uuid[],                    -- NULL = all projects the user can access in org_id
  expires_at   timestamptz NOT NULL,
  last_used_at timestamptz, last_used_ip inet,
  revoked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE device_auth_requests (
  device_code_hash text PRIMARY KEY,
  user_code    text UNIQUE NOT NULL,
  requested_scopes text[] NOT NULL,
  org_id       uuid REFERENCES organizations(id),
  approved_by  uuid REFERENCES users(id),
  token_id     uuid REFERENCES api_tokens(id),
  expires_at   timestamptz NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);

-- §6 Backup storage and keys
ALTER TABLE storage_targets ADD COLUMN org_id uuid REFERENCES organizations(id);  -- NULL = platform target
ALTER TABLE backups ADD COLUMN storage_target_id uuid REFERENCES storage_targets(id);
ALTER TABLE backups ADD COLUMN encryption_key_id uuid;
CREATE TABLE backup_keys (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid REFERENCES projects(id),  -- NULL = instance key
  key_enc     bytea NOT NULL,                -- encrypted with master key
  created_at  timestamptz NOT NULL DEFAULT now(),
  retired_at  timestamptz
);
ALTER TABLE projects ADD COLUMN backup_key_id uuid REFERENCES backup_keys(id);

-- §8 Branching
ALTER TABLE projects
  ADD COLUMN parent_project_id uuid REFERENCES projects(id),
  ADD COLUMN branch_source     text CHECK (branch_source IN ('backup','live')),
  ADD COLUMN branch_schema_only boolean,
  ADD COLUMN expires_at        timestamptz,
  ADD COLUMN sensitive_data    boolean NOT NULL DEFAULT false;

-- §9 Webhooks
CREATE TABLE webhooks (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id    uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name          text NOT NULL,
  tables        text[] NOT NULL,
  events        text[] NOT NULL,
  columns       text[],
  url           text NOT NULL,
  headers_enc   bytea,
  secret_enc    bytea NOT NULL,
  enabled       boolean NOT NULL DEFAULT true,
  status        text NOT NULL DEFAULT 'healthy', -- healthy|failing|paused|broken
  consecutive_failures int NOT NULL DEFAULT 0,
  created_by    uuid REFERENCES users(id),
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
  id           bigserial PRIMARY KEY,
  webhook_id   uuid NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
  event_id     text NOT NULL,
  attempt      int NOT NULL,
  status_code  int, latency_ms int,
  response_snippet text,
  error        text,
  dead_lettered boolean NOT NULL DEFAULT false,
  payload      jsonb,                     -- kept for dead letters / replay
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON webhook_deliveries (webhook_id, created_at DESC);

-- §9 Scheduled jobs
CREATE TABLE scheduled_jobs (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name        text NOT NULL,
  cron        text NOT NULL,
  timezone    text NOT NULL,
  kind        text NOT NULL CHECK (kind IN ('sql','http')),
  spec_enc    bytea NOT NULL,             -- SQL text or HTTP request (headers may hold secrets)
  timeout_s   int NOT NULL,
  overlap     text NOT NULL DEFAULT 'skip',
  enabled     boolean NOT NULL DEFAULT true,
  next_run_at timestamptz,
  created_by  uuid REFERENCES users(id),
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE job_runs (
  id          bigserial PRIMARY KEY,
  job_id      uuid NOT NULL REFERENCES scheduled_jobs(id) ON DELETE CASCADE,
  scheduled_for timestamptz NOT NULL,
  started_at  timestamptz, finished_at timestamptz,
  status      text NOT NULL,              -- succeeded|failed|timed_out|skipped
  rows_affected bigint, status_code int,
  error       text,
  trigger     text NOT NULL DEFAULT 'schedule' -- schedule|manual
);
CREATE INDEX ON job_runs (job_id, scheduled_for DESC);

-- §2.4 Break-glass, §10.6 dedicated requests
CREATE TABLE break_glass_sessions (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  admin_id    uuid NOT NULL REFERENCES users(id),
  reason      text NOT NULL,
  starts_at   timestamptz NOT NULL DEFAULT now(),
  expires_at  timestamptz NOT NULL,
  ended_at    timestamptz, ended_by uuid REFERENCES users(id)
);

CREATE TABLE dedicated_requests (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES organizations(id),
  project_id  uuid NOT NULL REFERENCES projects(id),
  requested_by uuid NOT NULL REFERENCES users(id),
  profile     jsonb NOT NULL,                 -- node preference, vcpu, mem, disk
  reason      text,
  status      text NOT NULL DEFAULT 'pending', -- pending|approved|rejected|cancelled
  decided_by  uuid REFERENCES users(id), decided_at timestamptz, decision_note text,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- §10.9 Usage
CREATE TABLE usage_records (
  org_id       uuid NOT NULL REFERENCES organizations(id),
  project_id   uuid,                           -- nullable: kept after project deletion
  metric       text NOT NULL,
  period_start timestamptz NOT NULL,
  granularity  text NOT NULL,                  -- hour | day
  quantity     numeric NOT NULL,
  plan_id      uuid NOT NULL,
  PRIMARY KEY (org_id, metric, granularity, period_start, project_id)
);

-- §3 Account flows, §10.10 terms
CREATE TABLE email_tokens (                   -- verification and password reset
  token_hash  text PRIMARY KEY,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  purpose     text NOT NULL CHECK (purpose IN ('verify','reset')),
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz
);

CREATE TABLE terms_versions (
  version      int PRIMARY KEY,
  terms_md     text NOT NULL,
  privacy_md   text NOT NULL,
  published_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE terms_acceptances (
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  version     int  NOT NULL REFERENCES terms_versions(version),
  accepted_at timestamptz NOT NULL DEFAULT now(),
  ip          inet,
  PRIMARY KEY (user_id, version)
);

-- Audit scope and actor
ALTER TABLE audit_log RENAME COLUMN operator_id TO user_id;
ALTER TABLE audit_log
  ADD COLUMN actor_kind  text NOT NULL DEFAULT 'session',  -- session|token|system
  ADD COLUMN token_id    uuid,
  ADD COLUMN org_id      uuid,                              -- NULL = platform-level event
  ADD COLUMN project_id  uuid,
  ADD COLUMN break_glass boolean NOT NULL DEFAULT false;
CREATE INDEX ON audit_log (org_id, created_at DESC);
CREATE INDEX ON audit_log (project_id, created_at DESC);
```

**Operation kinds added:** `demote`, `branch_create`, `branch_reset`, `branch_delete`, `copy_backups`, `create_db_user`, `drop_db_user`, `webhook_install`, `webhook_uninstall`, `rename_opaque`, `transfer_project`, `move_to_org_cluster`, `suspend_org`, `reinstate_org`, `delete_org`, `export_project`, `reclaim_space`.

**V1 data migration.** A one-time migration creates the platform admin's personal org (plan: Unlimited), assigns every existing project, instance, and storage target to it, converts V1 `owner` operators to `platform_admin` users with an org `owner` membership, and schedules `rename_opaque` operations for every V1 project (§10.2).

Other V1 tables that reference `operators(id)` (`sessions`, `projects.created_by`, `operations.created_by`) follow the rename automatically, since `ALTER TABLE … RENAME` keeps foreign keys. `sessions.operator_id` is renamed to `user_id` in the same migration.

---

## 12. API Additions

All under `/api/v1`, documented in `openapi.yaml`, and used by both the UI and CLI.

**Routing convention.** Project-level routes keep their V1 shape (`/projects/:id/...`), because project IDs are UUIDs and authz resolves each project's org before checking access; projects in orgs the caller can't see return `404`. Organisation-level routes live under `/orgs/:org`. List endpoints that span orgs (e.g. `GET /projects`) take an `org` query parameter and, with a token, are restricted to the token's org.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/auth/signup` · `/auth/verify-email` · `/auth/password-reset` · `/auth/password-reset/confirm` | Account flows (public, rate-limited) |
| GET/PATCH/DELETE | `/me` · `/me/sessions[/:id]` · `/me/recovery-codes` | Account, sessions, recovery codes |
| POST | `/me/terms/accept` | Accept current terms version |
| GET/POST | `/orgs` | Orgs I belong to / create an org |
| GET/PATCH/DELETE | `/orgs/:org` | Org details, settings / delete (owner, reauth) |
| GET/POST | `/orgs/:org/members` | List / invite |
| PATCH/DELETE | `/orgs/:org/members/:user` | Change org role / remove |
| POST | `/orgs/:org/leave` · `/orgs/:org/transfer-ownership` | Leave / transfer |
| GET/DELETE | `/orgs/:org/invitations[/:id]` | Pending invitations / revoke |
| GET | `/orgs/:org/usage` · `/orgs/:org/quotas` | Usage (`?from=&to=&metric=`, CSV) / limits and current usage |
| GET | `/orgs/:org/audit` | Org audit log |
| GET/DELETE | `/orgs/:org/tokens[/:id]` | Org owners/admins: tokens scoped to the org |
| POST | `/orgs/:org/break-glass/:id/end` | Org owner ends an active break-glass session |
| GET/POST | `/projects/:id/members` | Project members / invite |
| PATCH/DELETE | `/projects/:id/members/:user` | Change project role / remove |
| POST | `/projects/:id/transfer` | Move to another org (owner of both) |
| POST | `/projects/:id/export` | Create a downloadable dump → `202` |
| POST | `/projects/:id/reclaim-space` | Vacuum / repack a table → `202` |
| POST | `/invitations/accept` | Accept (public, token-gated) |
| POST | `/projects/:id/credentials` | Create or rotate personal DB credentials |
| GET | `/projects/:id/tables/:schema/:table/rows` | Extended: filters, sort, `xmin` |
| POST | `/projects/:id/tables/:schema/:table/changes` | Apply a batch of row inserts/updates/deletes |
| POST | `/projects/:id/schema/preview` | Turn a schema-change request into DDL + risk notes |
| POST | `/projects/:id/schema/apply` | Execute previewed DDL |
| POST | `/projects/:id/schema/migration` | Render DDL as a migration file |
| POST | `/projects/:id/demote/preflight` | Eligibility checks |
| POST | `/projects/:id/demote` | Start demotion → `202` |
| POST | `/projects/:id/promote` | Extended: within allowance → `202`; beyond → creates a dedicated request (`201`) |
| GET/POST/PATCH/DELETE | `/orgs/:org/storage-targets[/:id]` | Org targets (bring-your-own) |
| POST | `/storage-targets/test` | Live test before saving |
| PUT | `/projects/:id/storage-target` | Switch target (`copy_existing` option) |
| POST | `/projects/:id/backup-key` | Enable per-project key |
| GET | `/projects/:id/backup-key/download` | Download key (reauth) |
| GET/POST/DELETE | `/tokens[/:id]` | Manage own tokens |
| POST | `/auth/device` · `/auth/device/token` · `/auth/device/approve` | CLI device login |
| GET/POST | `/projects/:id/branches` | List / create → `202` |
| POST | `/projects/:id/reset` · `/projects/:id/detach` | Branch reset → `202` / detach |
| PATCH | `/projects/:id` | Extended: `expires_at`, `sensitive_data` |
| GET/POST/PATCH/DELETE | `/projects/:id/webhooks[/:wid]` | Manage webhooks |
| POST | `/projects/:id/webhooks/:wid/test` · `/rotate-secret` | Test event / rotate secret |
| GET | `/projects/:id/webhooks/:wid/deliveries` | Delivery log |
| POST | `/projects/:id/webhooks/:wid/replay` | Replay dead letters (`ids` or `all`) |
| GET/POST/PATCH/DELETE | `/projects/:id/jobs[/:jid]` | Manage scheduled jobs |
| POST | `/projects/:id/jobs/:jid/run` | Run now |
| GET | `/projects/:id/jobs/:jid/runs` | History |

### Platform admin API (`/api/v1/admin`, platform admins only)

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/admin/orgs` · `/admin/orgs/:org` | All orgs with plan, usage, status (no tenant content) |
| PATCH | `/admin/orgs/:org` | Assign plan, overrides, dedicated allowance, outbound toggle |
| POST | `/admin/orgs/:org/suspend` · `/reinstate` | Suspend / reinstate (reason required) |
| POST | `/admin/orgs/:org/cluster` | Assign or create a per-org shared cluster |
| POST | `/admin/orgs/:org/break-glass` | Start break-glass (reason, duration, reauth) |
| GET/POST/PATCH | `/admin/plans[/:id]` | Quota plan templates |
| GET/PATCH | `/admin/users` · `/admin/users/:id` | List, approve, disable, reset 2FA |
| POST | `/admin/invitations` | Platform invitations |
| GET/POST | `/admin/dedicated-requests[/:id/approve\|reject]` | Dedicated requests |
| GET/POST/PATCH/DELETE | `/admin/storage-targets[/:id]` | Platform targets |
| GET/PUT | `/admin/settings/*` | Signup mode, SMTP, terms versions, limits |
| GET | `/admin/audit` · `/admin/usage` | Platform audit log / usage across orgs |

V1's node and settings routes move under `/admin` with redirects from the old paths for one release.

---

## 13. Web UI Additions

| Area | Additions |
| --- | --- |
| Global header | **Org switcher** (remembers last org; personal org first), pending-invitations indicator, account menu. |
| Public pages | Sign up (per signup mode), verify email, log in, reset password, accept invitation, terms acceptance. |
| Org → Projects | The projects list scoped to the selected org, with quota usage bars. |
| Org → Members | Members, roles, project memberships, invitations, transfer ownership. |
| Org → Storage | Org storage targets (bring-your-own) with live test. |
| Org → Usage & quotas | Current-month usage per metric, trends, limits, CSV export; dedicated requests status. |
| Org → Audit | Org audit log; break-glass entries highlighted. |
| Org → Settings | Name, slug, members-can-create-projects, sensitive-by-default, delete org. |
| Account | Profile, password, 2FA and recovery codes, sessions, tokens (per org), device-login approval. |
| Admin console (`/admin`) | Orgs (plans, overrides, allowance, suspend, per-org cluster, break-glass), users (approve, disable, reset 2FA), plans, dedicated requests, nodes, platform storage, signup mode, SMTP, terms versions, platform audit, platform usage. |
| Banners | Suspension notice; active break-glass session (with "end session" for owners); soft/hard storage lock with **Reclaim space**. |
| Projects list | Branches nested under parents (collapsible), TTL countdown, "sensitive data" badge. Members see only their projects. |
| Project → Tables | Filters, sort, FK side panel, export; edit mode with staged changes and Save summary; conflict dialog; schema editor with DDL preview, risk notes, Run / Copy / Save as migration. |
| Project → Members | Members list, invite, role change, remove; "Get my credentials" panel. |
| Project → Branches | List, create dialog (source, contents, TTL), reset, detach, delete, extend TTL. |
| Project → Webhooks | List with health status; create/edit form; delivery log; dead letters with replay; test event; rotate secret. |
| Project → Jobs | List with next run in plain language; create/edit form with cron helper and SQL/HTTP editors; history; run now. |
| Project → Backups | Storage target selector (platform default or org targets); copy-existing option; per-project key enable/download; export project. |
| Project → Settings | Promote (within allowance, or request), demote (preflight checklist), sensitive-data toggle, transfer to another org. |
| Project → Audit | Project-scoped audit log for admins. |

---

## 14. Build Plan

Milestones continue numbering from V1 (M0–M7). Estimates assume one engineer working roughly full time. Total: about **14 weeks**.

### M8 — Organisations, users & access (Weeks 1–3)

- Data migration: `operators` → `users`, organisations, the platform admin's personal org, ownership backfill (§11).
- Signup modes, email verification, password reset, recovery codes, terms acceptance; SMTP made mandatory in setup.
- Org and project roles; invitations (org and platform); member management; leave, transfer ownership, project transfer.
- `authz` package with the full matrix (§2.3, §2.4), table-driven tests over every route, route-declaration CI check; `org_id` predicates on every tenant query plus the lint.
- Personal database credentials (§3.5) and `_ro` group roles, carried through promotion, restore, and import.
- Org, project, and platform audit logs; org switcher, account pages, org pages.

**Done when:** two users in two orgs each see only their own org's projects through UI and API (other org's IDs return `404`); an invited member with read-only project access gets working read-only credentials; removing them revokes everything immediately; and V1 projects appear in your personal org unchanged.

### M9 — Tenancy hardening, quotas & usage (Weeks 4–5)

- Opaque naming for new projects; `rename_opaque` database migration with pooler aliases for V1 projects; **Switch to opaque credentials** action with grace period; `pg_stat_activity` compatibility suite and restriction (§10.2).
- Quota plans, assignment, overrides, creation-time checks, per-org rate limiters (§10.3).
- Storage enforcement (warn / soft lock / hard lock / reclaim space), `temp_file_limit`, statement and idle-transaction reaper, `max_db_connections` per project (§10.4).
- Per-org shared clusters; dedicated allowance and requests (§10.5–10.6).
- Suspension, outbound disable, break-glass, platform admin console (§2.4, §10.7–10.8).
- Usage recording and the Usage page (§10.9); terms versions; org deletion with grace period (§10.10).
- **Isolation suite v2:** everything from V1 plus metadata-leak checks (no tenant-descriptive names visible in `pg_database`, `pg_roles`, `pg_stat_activity`), quota bypass attempts (raising `statement_timeout`, extra roles, temp files), and suspended-org checks.

**Done when:** a test tenant that fills its database is soft-locked at 100% and hard-locked at 120% while its console still works; a 30-minute query is reaped at 10 minutes; another tenant can't discover the name of any project created in V2 or switched to opaque credentials; and the Usage page shows correct hourly storage for a week of test data.

*These two milestones come first: every later feature has to respect orgs, quotas, and rate limits from the start.*

### M10 — API tokens & CLI (Weeks 6–7)

- Org-scoped tokens with scopes, project restriction, expiry; bearer middleware; per-token and per-org rate limits; token pages for users and org admins.
- Device-authorisation login with org selection.
- `cmd/cli` with contexts (server + org), `--json`, streaming, confirm flags, exit codes; commands for orgs, projects, connect, creds, sql, backup, promote, members, tokens, operations.

**Done when:** a CI job with a project-restricted `write` token can run SQL on its project, gets `404` for a project in another org, and is refused when it tries to delete its own project.

### M11 — Visual table editing (Weeks 8–9)

As before: grid filters/sort/FK panel/export; row editing with `xmin` conflict detection and transactional save; schema editor with DDL preview, risk notes, `CONCURRENTLY` indexes, `lock_timeout`, migration export; audit with SQL. Console concurrency limits from the org's plan apply.

**Done when:** two sessions editing the same row produce a conflict; a large type change shows the rewrite warning first; an added column exports as a goose migration that applies cleanly elsewhere.

### M12 — Backup storage targets (Week 10, first half)

Platform and org targets with live test; project target selection; copy-existing; per-project keys and download; WAL-G reconfiguration for dedicated; backup quota accounting for platform targets.

**Done when:** an org's project backs up to the org's own bucket and can be restored with standard tools using only the downloaded key, and that storage doesn't count toward the org's backup quota.

### M13 — Branching (Week 10 second half – Week 11)

Branch create/reset/detach/delete with stable credentials; TTL expiry and emails; org branch quotas and storage accounting; branch-hours usage; sensitive-data defaults; CLI `branch` commands with `--env`.

**Done when:** a GitHub Actions workflow creates a branch per pull request and the branch is gone after its TTL; a reset leaves `DATABASE_URL` unchanged; creating an 11th branch on the Personal plan returns `quota_exceeded`.

### M14 — Demotion (Week 12, first half)

Preflight (including org storage quota and allowance release), demotion flow, rollback, 48-hour retention, placement on the org's own cluster where set, UI wizard, CLI. CI test with a live writer.

**Done when:** a promoted project demotes with its URL and every member's credentials still working, and the org's dedicated allowance is released.

### M15 — Webhooks & scheduled jobs (Week 12 second half – Week 13)

As before (outbox, delivery worker, signing, retries, dead letters, scheduler, SQL and HTTP jobs), plus per-org rate limits, per-org outbound counters, the platform-admin-only internal allow-list, and outbound disable.

**Done when:** a committed insert reaches a receiver within one second with a valid signature; a rolled-back insert never does; a receiver down for an hour receives every event in order afterwards; a webhook to `http://169.254.169.254` is refused; and an org over its delivery rate sees deliveries queue, not drop.

### M16 — Hardening & invite-only launch (Week 14)

- Security review of all new surfaces: authz matrix, org scoping, opaque naming, quota enforcement, tokens, SSRF, break-glass, the webhook `SECURITY DEFINER` function, personal roles across tier moves.
- **Recommended before inviting anyone outside your own projects:** a short external security review or penetration test focused on tenant isolation, since other people's data will now depend on it.
- Failure injection: kill the quota enforcer mid-lock, the reaper, the delivery worker, the scheduler; revoke a member mid-session; suspend an org mid-backup.
- Load check: 30 orgs, 300 projects on two shared nodes; 20 projects with webhooks at 50 events/s; 100 jobs per minute; 50 concurrent branches.
- Docs: user guide (orgs, members, roles), CLI reference, webhooks, jobs, branching in CI, BYO buckets, platform admin runbook, incident process, terms template.
- **Invite-only beta:** two or three friends' orgs for two weeks before inviting more people. Tag **v2.0.0** after the beta.

### Timeline summary

| Week | Milestone | Usable outcome |
| --- | --- | --- |
| 1–3 | M8 Orgs, users & access | Friends and colleagues get their own orgs |
| 4–5 | M9 Tenancy, quotas & usage | Safe to host other people's projects |
| 6–7 | M10 Tokens & CLI | Scripting and CI access |
| 8–9 | M11 Table editing | Edit data and schema in the browser |
| 10 | M12 Backup storage | Orgs own their backups |
| 10–11 | M13 Branching | Throwaway DBs per feature / PR |
| 12 | M14 Demotion | Move projects back to shared |
| 12–13 | M15 Webhooks & jobs | React to changes, recurring tasks |
| 14 | M16 Hardening & beta | Invite-only launch, then v2.0.0 |

**Order rationale:** tenancy first (everything else depends on orgs, permissions, and quotas), the CLI next (every later feature ships with its commands), then features by how often they'll be used. M12, M13, and M14 are independent and can be reordered freely. **Don't invite other people until M9 is done**, even informally: before that, the shared tier is only safe for your own projects.

---

## 15. Roadmap After V2

Carried forward, unprioritised: **billing and payments** (pricing on top of §10.9 usage records); SSO/SAML for orgs; self-serve dedicated instances; auth, auto-generated REST, storage, edge functions, realtime (with a Supabase-compatible API as the leading option); automatic VM provisioning (Hetzner first); multiple Postgres major versions and major upgrades; zero-downtime moves with logical replication; HA for dedicated instances; standby edge pooler; query insights; data masking for branches; branches of branches.

---

## 16. Risks & Mitigations

| Risk | Impact | Mitigation |
| --- | --- | --- |
| **Cross-tenant data leak** through a missed org check | One org sees another's data — the most serious failure V2 can have | Single `Can()` entry point; `org_id` predicate on every tenant query with a CI lint; table-driven tests on every route; `404` for foreign resources; external review before the beta. |
| **Metadata leakage inside Postgres** | Tenants learn other tenants' project names | Opaque names (§10.2), nothing descriptive written into Postgres, `pg_stat_activity` restriction where compatible, isolation suite v2. |
| **Noisy or hostile tenant degrades the shared tier** | Slow or unavailable databases for others | Enforced storage locks, `temp_file_limit`, statement reaper, connection caps, memory cgroups, per-org metrics; suspension and per-org clusters. Full isolation only on dedicated (stated in the terms). |
| **Platform admin trust** | Tenants worry about platform admin access | Break-glass with owner notification and dual audit; honest terms about root access. |
| **Open signup abuse** (if enabled later) | Spam accounts, resource abuse | Invite-only default; verification, TOTP, Turnstile, per-IP limits, domain allow-list, approval mode, Personal-plan limits. |
| **Legal and data-protection exposure** | Obligations for hosting others' personal data | Terms and privacy versions with recorded acceptance, export and deletion controls, incident runbook; get advice before opening beyond friends and colleagues. |
| **V1 → V2 rename migration breaks a V1 app** | Outage for your existing projects | Only databases are renamed, behind pooler aliases; role renames are opt-in with a grace period; each rename is a separate operation with smoke test and automatic rollback; run on one project first. |
| **SSRF** through webhooks or HTTP jobs | Access to internal services or node metadata | Resolve-and-pin, private range blocklist, no redirects, explicit allow-list only. Covered by tests. |
| **Schema edits locking production tables** | App outage | DDL preview with risk notes, `lock_timeout` 5s, `CONCURRENTLY` by default, read-only toggle on by default for dedicated projects. |
| **Branches filling shared disk** | Shared-cluster outage | Org branch and storage quotas, TTL by default, branches counted in node capacity, storage locks, disk alerts. |
| **Webhook outbox growth** when a receiver is down | Disk growth in the project DB | Outbox size alert, auto-pause after repeated failures, paused webhooks stop enqueuing. |
| **Heavy scheduled SQL on the shared tier** | Noisy neighbour | Shared-tier guardrails still apply, job timeouts, minimum 1-minute interval, per-project job history to spot offenders. |
| **Leaked API tokens** | Unauthorised access | Mandatory expiry, scopes, project restriction, `pgd_` prefix for secret scanners, creation emails, last-used tracking, instant revocation. |
| **Personal roles lost during tier moves** | Members' credentials break | Every tier-move and reset operation recreates personal roles from `project_db_users`; tested in the promotion and demotion CI suites. |
| **`SECURITY DEFINER` trigger function abuse** | Privilege escalation inside a project DB | Owned by `pgdock_admin`, pinned `search_path`, does only one fixed insert + notify, no dynamic SQL. |

---

## 17. Open Questions

1. **Default plan limits.** Are the Personal and Team numbers in §10.3 sensible for the friends and colleagues you have in mind, especially 2 GB per project and 5 GB total on Personal?
2. **Members creating projects.** Should org members be able to create projects by default, or only owners and admins?
3. **Break-glass approval.** Is notify-and-audit enough, or should break-glass require an org owner to approve before it starts (slower in an incident, stronger for trust)?
4. **Per-org shared clusters in V2.** Do you expect to need these soon, or can §10.5 move to the roadmap to save about two days?
5. **Data masking for branches.** Is schema-only-by-default for sensitive projects enough, or do you want simple column masking rules in V2?
6. **Migration formats.** Which format do you use today (goose, dbmate, plain SQL, golang-migrate, Atlas)? The exporter can start with that one.
7. **Token expiry.** Is a 1-year maximum right, or should the platform admin be able to allow non-expiring tokens for long-lived servers?