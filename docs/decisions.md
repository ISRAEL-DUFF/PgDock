# Implementation decisions

Choices made while building, where the code departs from or fills a gap in
the [V1 specification](../PGDock%20—%20V1%20Specification%20&%20Build%20Plan.md).
The spec's own decisions log is §16.

## M1 — Shared-tier provisioning

| # | Topic | Decision | Why |
| --- | --- | --- | --- |
| 1 | First node | The shared cluster is registered from `PGDOCK_SHARED_ADMIN_URL` at startup, as a node with one shared instance. Its `pgdock_admin` credential is stored encrypted in `nodes.pg_admin_secret`. | Node registration through agents is M4. This keeps M1 usable on a single VPS. It is idempotent, so it also picks up a changed admin password. |
| 2 | Schema adjustments (migration 00002) | `nodes.private_addr` is `text` (hostnames allowed). `nodes.agent_cert_fp` is nullable until an agent registers. `instances` gains `admin_host`/`admin_port`. `projects.storage_target_id` is nullable until M3. | Compose service names aren't IPs. The control plane and the poolers can reach a cluster at different addresses (a published port in dev). Storage targets come with backups. |
| 3 | Password handoff for the smoke test | The create and rotate operations carry the new password, encrypted with the master key and bound to the project and purpose, in `operations.params.secrets`. The queue scrubs `params.secrets` when the operation finishes (success or failure), and the API never returns it. | §5.3 says passwords are never stored reversibly. But §6.1 step 5 smoke-tests the pooler as the new role, which needs the plaintext, and an operation can run in another process or after a restart. The ciphertext exists only while the operation is pending. |
| 4 | Pooler file layout | pgdock-server writes `databases.ini` and `userlist.txt` into `PGDOCK_POOLER_CONFIG_DIR`. Each PgBouncer's static `pgbouncer.ini` sets `auth_file` to that `userlist.txt` and ends with `%include` of `databases.ini`. The directory is mounted, not single files. | Pool mode and ports differ per pooler; routes and users are shared. An atomic rename only shows through a directory mount. |
| 5 | Admin console user | The admin console user's SCRAM verifier uses a salt derived from the master key (`Keyring.Derive`), so it is identical across syncs. | Keeps the auth file byte-identical when nothing changed, without storing plaintext. |
| 6 | Delete disconnects with `KILL` | Delete (and create rollback) issue `KILL <db>` on both poolers, remove the route and `RELOAD`, then `pg_terminate_backend` and `DROP DATABASE ... WITH (FORCE)`. | §6.2 says `PAUSE`, but in session mode `PAUSE` waits for clients to disconnect on their own. `KILL` drops them, which is what a delete wants. `PAUSE`/`RESUME` remain for promotion (M5). |
| 7 | One operation per project | Rotate and delete are refused (409) while another operation on the project is queued or running, checked under a row lock. | Keeps flows from interleaving, e.g. rotating mid-delete. |
| 8 | Not yet enforced | The final backup on delete waits for backups (M3) and is logged as skipped. (Re-authentication for delete landed in M2.) | Milestone order. |
| 9 | Cluster hardening at registration | `CONNECT`/`TEMPORARY` on `postgres` and `template1` are revoked from `PUBLIC`. `log_statement` and non-SCRAM `pg_hba.conf` network rules are reported as warnings. | Project roles must reach only their own database. Logging and `pg_hba.conf` are the operator's to change; the isolation suite checks them. |
| 10 | Attempts include crashed ones | An attempt cut short by a worker crash counts toward the kind's attempt limit. | Keeps a crash-looping operation from retrying forever. |

## M2 — Auth, TLS & the web UI shell

| # | Topic | Decision | Why |
| --- | --- | --- | --- |
| 1 | Claiming a fresh install | The setup wizard needs a one-time code that pgdock-server prints in its log (or `PGDOCK_SETUP_CODE`). | A freshly started control plane on a public IP would otherwise belong to whoever loads it first. |
| 2 | TOTP before the account exists | `/setup/begin` returns the TOTP secret but creates nothing; `/setup/complete` creates the owner only with a valid code. | Spec §7.2 enforces 2FA at first login; this way there is never an owner without it. |
| 3 | TOTP replay | Each operator's last accepted time step is stored; a code is accepted once, and only for a later step. | A code seen by someone else (shoulder, logs) cannot be reused within its 90-second window. |
| 4 | CSRF | Double-submit token (`pgdock_csrf` cookie and `X-CSRF-Token` header), plus a required matching `Origin` when the browser sends one, on every mutating `/api` request, including login. Cookies use the `__Host-` prefix when Secure. | Spec §7.2; `SameSite=Strict` on the session cookie is a second line. |
| 5 | Re-auth scope | Required for `DELETE /projects/{id}` today, within 10 minutes of a password + TOTP check. The UI asks for both inside the typed-confirmation dialog. | Spec §7.2 lists delete, in-place restore, master key rotation, and node removal; the others arrive with their features. |
| 6 | Audit log | Every mutating API request is recorded with its outcome (`success`, `failure`, `denied`), including requests refused for CSRF, authentication, or re-auth. Details never include passwords or codes. | Spec §7.5; refusals are the interesting part of an audit trail. |
| 7 | Guardrail changes | `PATCH /projects/{id}/settings` queues `apply_settings`, which re-applies role settings, re-renders the pooler, and issues `RECONNECT <db>`. | `ALTER ROLE ... SET` only affects new backends, and PgBouncer would otherwise keep serving pooled connections with the old values. The integration test caught this. |
| 8 | Pooler TLS | pgdock-server obtains the DB hostname's certificate itself (certmagic, HTTP-01); Caddy forwards `/.well-known/acme-challenge/` for any non-UI host on :80 to it. The pair is written to the pooler dir and the poolers `RELOAD` (verified to swap certificates without a restart). Until a CA certificate exists, a self-signed one stands in; IP addresses and `localhost` stay self-signed. | The DB hostname is set in the wizard, after Caddy has started, and PgBouncer needs files on disk. Spec §3.3 allows HTTP-01 or DNS-01. |
| 9 | Clients must use TLS | Both poolers run `client_tls_sslmode = require`; connection strings default to `sslmode=require`. | Spec §3.3. |
| 10 | One uid for server and poolers | The pgdock-server image runs as uid 70, PgBouncer's uid, so the generated files (including the TLS key) stay 0640. | Avoids world-readable keys in the shared volume. |
| 11 | Session refresh in the UI | Sign-in and setup write the session state the server returns straight into the query cache. | Invalidating an unobserved query does not refetch, which sent freshly signed-in users back to `/login`. The browser test caught it. |
