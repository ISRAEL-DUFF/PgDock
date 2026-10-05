-- name: InsertLedgerEntry :one
-- tenant: system - the ledger's posting function (rows carry org_id).
INSERT INTO ledger_entries (txn_id, org_id, account, direction, amount_minor, source_type, source_id, idempotency_key, memo, created_by)
VALUES (@txn_id, sqlc.narg(org_id), @account, @direction, @amount_minor, @source_type, @source_id, @idempotency_key, sqlc.narg(memo), sqlc.narg(created_by))
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING id;

-- name: CountLedgerTxn :one
-- tenant: system - the ledger's posting function.
SELECT count(*)::int FROM ledger_entries WHERE txn_id = @txn_id;

-- name: LedgerBalance :one
-- tenant: system - balances of an org the caller resolved (debits minus
-- credits); with no org, the account across all of them.
SELECT (coalesce(sum(amount_minor) FILTER (WHERE direction = 'debit'), 0) -
        coalesce(sum(amount_minor) FILTER (WHERE direction = 'credit'), 0))::bigint AS balance
FROM ledger_entries WHERE (sqlc.narg(org_id)::uuid IS NULL OR org_id = sqlc.narg(org_id)) AND account = @account;

-- name: LedgerAccountTotals :many
-- tenant: system - the ledger check and finance views.
SELECT account, (coalesce(sum(amount_minor) FILTER (WHERE direction = 'debit'), 0) -
        coalesce(sum(amount_minor) FILTER (WHERE direction = 'credit'), 0))::bigint AS balance
FROM ledger_entries WHERE org_id IS NOT DISTINCT FROM sqlc.narg(org_id) OR sqlc.narg(org_id)::uuid IS NULL
GROUP BY account ORDER BY account;

-- name: UnbalancedLedgerTxns :many
-- tenant: system - the ledger's invariant check.
SELECT txn_id, sum(amount_minor) FILTER (WHERE direction = 'debit')::bigint AS debits,
       sum(amount_minor) FILTER (WHERE direction = 'credit')::bigint AS credits
FROM ledger_entries GROUP BY txn_id
HAVING coalesce(sum(amount_minor) FILTER (WHERE direction = 'debit'), 0) <> coalesce(sum(amount_minor) FILTER (WHERE direction = 'credit'), 0);

-- name: LedgerTotals :one
-- tenant: system - the ledger's invariant check.
SELECT coalesce(sum(amount_minor) FILTER (WHERE direction = 'debit'), 0)::bigint AS debits,
       coalesce(sum(amount_minor) FILTER (WHERE direction = 'credit'), 0)::bigint AS credits,
       count(DISTINCT txn_id)::int AS txns
FROM ledger_entries;

-- name: OrgLedger :many
-- tenant: system - the ledger of an org the request already authorized.
SELECT * FROM ledger_entries WHERE org_id = @org_id ORDER BY id DESC LIMIT @lim;
