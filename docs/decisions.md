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
| 8 | Not yet enforced | Re-authentication for delete waits for auth (M2). The final backup on delete waits for backups (M3) and is logged as skipped. | Milestone order. |
| 9 | Cluster hardening at registration | `CONNECT`/`TEMPORARY` on `postgres` and `template1` are revoked from `PUBLIC`. `log_statement` and non-SCRAM `pg_hba.conf` network rules are reported as warnings. | Project roles must reach only their own database. Logging and `pg_hba.conf` are the operator's to change; the isolation suite checks them. |
| 10 | Attempts include crashed ones | An attempt cut short by a worker crash counts toward the kind's attempt limit. | Keeps a crash-looping operation from retrying forever. |
