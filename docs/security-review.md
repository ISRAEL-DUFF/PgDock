# Security review (spec §7, v1.0.0)

Each item of spec §7, how PGDock enforces it, and what checks it
automatically. "CI" means `.github/workflows/ci.yml` on every push (and
weekly); "live" means pgdock-server's own weekly `isolation_check` operation
on every shared cluster, whose failure raises an alert.

## 7.1 Tenant isolation (shared tier)

Every item below is enforced when a project is created and verified by
`internal/isocheck`, which runs in CI (`test/isolation`) and weekly against
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
