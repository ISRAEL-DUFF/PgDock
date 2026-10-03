# Security review (spec §7, v1.0.0; V2 surfaces, M16)

Each item of spec §7, how PGDock enforces it, and what checks it
automatically. "CI" means `.github/workflows/ci.yml` on every push (and
weekly); "live" means pgdock-server's own nightly `isolation_check` operation
on every shared cluster, whose failure raises an alert.

## 7.1 Tenant isolation (shared tier)

Every item below is enforced when a project is created and verified by
`internal/isocheck`, which runs in CI (`test/isolation`) and nightly against
live clusters (`isolation_check`, Settings → Tenant isolation). A failed
check raises the critical `isolation_check_failed` alert.
`TestLiveIsolationCheck` breaks each item on purpose and requires the check
to report it.

| Checklist item | Enforced by | Verified by |
| --- | --- | --- |
| `CONNECT` on each database revoked from `PUBLIC` | `ensureDatabase`: `REVOKE ALL … FROM PUBLIC`, `GRANT CONNECT, TEMPORARY` to the owner only; `postgres` and `template1` revoked at cluster registration | `isocheck.Roles` (every project) and `Cluster` (maintenance databases); `TestTenantIsolation` "cannot connect to B's database", "public has no access"; live probe: tenant A connects to B, `postgres`, `template1` |
| `CREATE` on `public` revoked from `PUBLIC` | `hardenDatabase` | `isocheck.Database` on every project database; isolation suite |
| Project roles `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS` | `ensureRole` spells every attribute out on `CREATE`/`ALTER ROLE`; console logins are `NOINHERIT` and the same | `isocheck.Roles` (owner and `<db>_console`); "role has no dangerous attributes" |
| Not members of `pg_read_all_data`, `pg_write_all_data`, `pg_read_server_files`, `pg_execute_server_program`, or other privileged roles | Roles are never granted; the console login is granted only its project's owner (`WITH INHERIT FALSE`) | `isocheck.Roles` (membership in any `pg_*` or privileged role); "not a member of predefined roles"; live probe |
| Untrusted languages and `plpython3u`, `plperlu`, `dblink`, `postgres_fdw`, `file_fdw`, `adminpack` never available | Project roles cannot create untrusted extensions; the extension API allows only the §7.4 list for the tier (`postgres_fdw` only on dedicated) | `isocheck.Database` (none installed in any project DB); live probe tries each `CREATE EXTENSION`; `TestExtensionsAndMetrics` (API refuses `dblink`, `plpython3u`) |
| `pg_hba.conf` accepts only the pooler and control-plane addresses, `scram-sha-256`, `hostssl` where applicable | Bundle: `deploy/compose/shared-pg-hba.conf` names the fixed addresses of pgdock-server, the agent, and both poolers; agent-run clusters and instances: `pgdock-hba.sh` writes `PGDOCK_AGENT_DB_ALLOW` (default: private ranges) at initdb | `isocheck.Cluster`: every network rule must be SCRAM (or `reject`/`cert`), name explicit addresses (no `all`, `samenet`, `0.0.0.0/0`), and use `hostssl` outside private ranges; `TestHBAFinding`; `TestClusterConfiguration` |
| `log_statement` off; only `log_min_duration_statement` set | `log_statement=none`, `log_min_duration_statement=5s` in the bundle and agent-run clusters | `isocheck.Cluster` (`log_statement`, `log_duration`, `log_min_duration_statement` not 0) |
| Isolation suite: A cannot connect to B, list B's tables, read B's data through any predefined role, create objects in B, or read server files | — | `test/isolation` (CI) and `isocheck.Probes` (live, two throwaway tenants created with the real create steps): connections, `SET ROLE`, `GRANT`, `ALTER ROLE`, `DROP DATABASE`, `pg_terminate_backend`, `pg_read_file`, `pg_ls_dir`, `COPY … PROGRAM`, `COPY FROM` file, `lo_import`, C functions, superuser-only settings, `pg_stat_activity` query text |

The SQL console adds an escape route the spec did not list (`RESET ROLE`
on an admin session). It signs in as a per-project login that inherits
nothing, so `RESET ROLE` leaves no privileges; `TestSQLConsole` tries
`RESET ROLE`, `SET ROLE` to the admin and to a neighbour, `SET SESSION
AUTHORIZATION`, `set_config('role')`, `pg_authid`, `CREATE ROLE`, and
`COPY … PROGRAM`.

## 7.2 Operator authentication

| Item | Implementation | Test |
| --- | --- | --- |
| argon2id password hashes | `internal/auth/password.go` | `TestPasswordHash`, `TestPasswordPolicy` |
| TOTP required, enrolled at first login | The setup wizard creates the owner only after a valid TOTP code; every sign-in needs one | `TestSetupFlow`, `TestTOTPVectors`, `TestVerifyTOTP`, `TestTOTPReplayAndChallengeExpiry` |
| Server-side sessions, `HttpOnly`, `Secure`, `SameSite=Strict`, 12 h idle timeout | `sessions` table; `__Host-` cookies; idle 12 h, at most 7 days | `TestSessionCookieFlags`, `TestReauthAndIdleTimeout`, `TestLoginSessionLogout` |
| CSRF double-submit on mutations | `guard` checks the header against the cookie and the Origin | `TestAuthFlowOverHTTP` |
| Re-authentication for delete, in-place restore, master key rotation, node removal | `reauthRequired` (10 min window); master key rotation is a host CLI (`-rotate-master-key`), which needs the key | `TestReauthAndIdleTimeout`, backups and nodes integration tests |
| Rate limiting and lockout | 10 sign-ins per 5 min per address; account lockout after 5 failures for 15 min | `TestLimiter`, `TestLoginRateLimit`, `TestLockout`, `TestWrongTOTPCountsTowardLockout` |
| Single operator in V1, `operators.role` kept | Setup creates one `owner` | `TestSetupFlow` |
| API tokens (V2 §7.2): hashed, scoped to one org, scopes, project restriction, expiry | `internal/tokens`; the guard's bearer path skips cookies and CSRF; `authz.ScopeFor` per action; platform and session-only routes refuse tokens | `TestRestrictedWriteTokenInCI`, `TestAPITokens`, `TestCLIJobWithRestrictedWriteToken` |
| Tokens follow their user and org | Revoked on removal or disable; disabled while the org is suspended | `TestAPITokens` |
| Token rate limits | 600/min per token, 1,200/min per org | `TestTokenRateLimit` |
| CLI device login | 10-minute codes, approval in a signed-in browser, scopes never widened, the token sealed until collected once | `TestDeviceLogin`, `TestCLIDeviceLogin` |

## 7.3 Secrets

| Item | Implementation | Test |
| --- | --- | --- |
| Secrets encrypted at rest with `PGDOCK_MASTER_KEY` (TOTP secrets, storage credentials, backup key, agent CA key, cluster and instance admin passwords, alert secrets, operation password hand-offs) | AES-256-GCM with per-secret associated data (`internal/crypto`) | `internal/crypto` tests |
| Master key rotation by CLI | `pgdock-server -rotate-master-key` re-encrypts everything in one transaction, then proves the new key alone opens it all | `TestMasterKeyRotation` (including a wrong old key rolling back) |
| Backups encrypted before upload | Per-object file keys wrapped by the backup key (`internal/backupfmt`); WAL-G uses an OpenPGP key derived from it | backup integration tests (`TestBackupDeleteRestoreVerify` checks the object is ciphertext) |
| The operator is made to export the backup key | Setup wizard step 4; a banner until it is confirmed | e2e "fresh install" |

Not stored at all: project passwords (only SCRAM verifiers; a password is
shown once), SQL console query text (audit entries only).

## 7.4 Extension allow-list

Enforced by `POST /projects/{id}/extensions` (per tier, and only extensions
the server has); verified by `TestExtensionsAndMetrics` and, for the
escape-prone ones, by the isolation checks above.

## 7.5 Audit log

Every mutating API call writes a row with operator, action, target, IP, user
agent, time, and outcome (`guard` in `internal/api/security.go`). The table
is append-only in the database itself: triggers refuse `UPDATE`, `DELETE`,
and `TRUNCATE` (`TestAuditLogIsAppendOnly`). It is filterable at `/audit`.

## Transport and network

- Clients reach databases only through PgBouncer with TLS required
  (`client_tls_sslmode = require`); certificates from ACME or self-signed.
- Agents use mTLS with a private CA; the server pins each agent's
  certificate fingerprint and node ID; no work is sent to an agent of
  another major version (`nodes.Compatible`, spec §11.3).
- Webhook alerts can be signed (`X-PGDock-Signature: t=…,v1=HMAC-SHA256`).
- `/metrics` needs a session or `PGDOCK_METRICS_TOKEN`.

## V2 review (M16)

The surfaces V2 added, how each is enforced, what tests it, and what the
review found and fixed.

| Surface | Enforced by | Verified by |
| --- | --- | --- |
| Permission matrix (V2 §2.3, §2.4) | One `authz.Can`; every route declares an action (`routeRules`), resolved to an organisation before the check; foreign resources answer 404 | `TestEveryRouteDeclaresAnAction`, `TestPermissionMatrix` (every route × every role, platform admin and outsider), `TestMembersCanCreateProjectsSetting` |
| Org scoping in the data layer (V2 §2.6) | Every query on a tenant table filters by `org_id`, or says why it is system-wide (`-- tenant: system`) | `TestTenantQueriesAreScoped` (CI lint), `TestTwoOrgsSeeOnlyTheirOwn` |
| Opaque names (V2 §10.2) | `p_<10 base32>` databases and roles; nothing descriptive written into Postgres; PGDock's own sessions use generic `application_name`s | `TestOtherTenantsCannotDiscoverProjectNames` |
| Quotas and resource limits (V2 §10.3–10.4) | Plans checked before every create; soft and hard storage locks; `temp_file_limit` and statement reaper on the shared tier; a soft lock applies to SQL jobs too | `TestQuotasAndDedicatedRequests`, `TestStorageLimitLocks`, `TestReaperEndsLongStatements`, `TestJobSQLCannotEscalate` |
| API tokens (V2 §7.2) | Hashed; one org; scopes; project restriction; expiry; refused for disabled or unverified users and suspended orgs; a removed member's tokens fail the membership check | `TestAPITokens`, `TestRestrictedWriteTokenInCI`, `TestTokenRateLimit`, `TestDeviceLogin` |
| Outbound requests (V2 §9.1, §10.7) | `internal/outbound`: https only (http to allow-listed hosts); every resolved address checked (IPv4-mapped, NAT64 and 6to4 addresses as the IPv4 address they embed); link-local and metadata never, even allow-listed; the request connects to the checked address, without proxies or redirects; per-org rate limits and counters; outbound off per org | `internal/outbound` tests, `TestWebhookDelivery`, `TestScheduledJobs` |
| Break-glass (V2 §2.4) | Platform admin only, step-up auth, at most 4 h, owners emailed, flagged in both audit logs, any owner can end it | `TestSuspensionAndBreakGlass` |
| Webhook trigger function (V2 §9.1) | `pgdock.webhook_enqueue()` is `SECURITY DEFINER` with `search_path = pg_catalog, pg_temp`, in a superuser-owned schema with all access revoked from `PUBLIC` (so tenants can't call it or attach it to triggers); a `pgdock` schema anything else owns is dropped first; the worker delivers an outbox row only for one of its own project's webhooks | `TestWebhookDelivery`, `TestWebhooksAcrossCopies` |
| Personal roles across tier moves (V2 §3.5) | Member logins and the read-only role recreated with the same verifiers on promotion, demotion, restore and import | `TestMemberLoginsSurviveRestoreAndPromotion`, `TestDemotionLiveWriter` |
| Running tenant SQL | The console, table editor, schema editor and SQL jobs sign in as the project's console login (`NOINHERIT`, may only `SET ROLE` to the project's roles), never an admin login | `TestSQLConsole`, `TestJobSQLCannotEscalate` |
| Copying a tenant's database | A restore runs the dump's functions (CHECK constraints, generated columns). Branches, restores, imports and the restore test restore as the console login with `--role` the owner; promotion and demotion through a short-lived login that inherits the project's roles but is no superuser (extensions created first, PGDock's webhook schema copied on its own) | `TestCopiesDoNotRunAsSuperuser` |

### Found and fixed

- **SQL jobs ran on the superuser's connection** (critical). A job ran as
  `SET LOCAL ROLE <owner>` on an admin connection, so `RESET ROLE` in its
  SQL made it the cluster superuser. Jobs now run through the console
  login.
- **Copies ran the tenant's functions as the superuser** (critical). Every
  restore signed in as the admin and only `SET ROLE` to the owner (or, for
  promotion, demotion and the restore test, not even that), so a function
  in a CHECK constraint could `RESET ROLE` during a branch, restore,
  import, restore test, promotion or demotion. The Postgres documentation
  is explicit that restoring a dump runs code of the source's choosing; an
  import's source is the tenant's own server. Fixed as in the table.
- **NAT64 and 6to4 addresses** reached internal and metadata addresses
  (`64:ff9b::a9fe:a9fe` is 169.254.169.254 on a NAT64 network). They are
  now checked as the IPv4 address they embed.

### Before inviting people outside your own projects

Have someone outside the project review tenant isolation, or pay for a short
penetration test of it (V2 §14 M16): other people's data now depends on it,
and this review found two critical bugs. Start with the copy paths and
anything that runs tenant SQL.

## Dependency audit

CI's `audit` job runs `govulncheck` (Go modules and the standard library
code actually reachable) and `npm audit --audit-level=high` for the web UI
and the e2e harness on every push. At the v1.0.0 review: npm reported 0
vulnerabilities in both; the Go toolchain is 1.25.13.

## Known limitations (accepted for V1)

- Inside one host's Docker network the shared cluster is reached without
  TLS; across hosts, nodes should use a private network (the hba check
  requires `hostssl` for public addresses).
- Operators with shell access to a node can read container environments
  (instance admin passwords, S3 credentials, the WAL-G key), as they can
  read the data itself.

Sign-off: every §7.1 item is enforced at create and verified automatically
in CI and weekly on live clusters; §7.2–7.5 items are implemented and
covered by the tests named above.
