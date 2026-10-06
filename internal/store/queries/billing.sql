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

-- ---- Price books ----------------------------------------------------------

-- name: ListPriceBooks :many
-- tenant: platform - the admin's price books.
SELECT * FROM price_books ORDER BY version DESC;

-- name: GetPriceBook :one
-- tenant: platform - price books are platform-wide.
SELECT * FROM price_books WHERE version = @version;

-- name: CurrentPriceBook :one
-- tenant: platform - the published book in effect at @at.
SELECT * FROM price_books WHERE published_at IS NOT NULL AND effective_at <= @at
ORDER BY effective_at DESC, version DESC LIMIT 1;

-- name: NextPriceBook :one
-- tenant: platform - the published book that takes effect next, after @at.
SELECT * FROM price_books WHERE published_at IS NOT NULL AND effective_at > @at
ORDER BY effective_at, version LIMIT 1;

-- name: CountPublishedPriceBooks :one
-- tenant: platform - whether a first book exists.
SELECT count(*)::int FROM price_books WHERE published_at IS NOT NULL;

-- name: InsertPriceBook :one
-- tenant: platform - a new draft (or the first, published) book.
INSERT INTO price_books (version, effective_at, prices, notes, published_at, published_by)
VALUES ((SELECT coalesce(max(version), 0) + 1 FROM price_books), @effective_at, @prices, sqlc.narg(notes),
        sqlc.narg(published_at), sqlc.narg(published_by))
RETURNING *;

-- name: UpdatePriceBookDraft :one
-- tenant: platform - edits a draft (published books are frozen by trigger).
UPDATE price_books SET effective_at = @effective_at, prices = @prices, notes = sqlc.narg(notes)
WHERE version = @version AND published_at IS NULL RETURNING *;

-- name: PublishPriceBook :one
-- tenant: platform - publishes a draft.
UPDATE price_books SET published_at = now(), published_by = sqlc.narg(published_by)
WHERE version = @version AND published_at IS NULL RETURNING *;

-- name: DeletePriceBookDraft :execrows
-- tenant: platform - drops a draft.
DELETE FROM price_books WHERE version = @version AND published_at IS NULL;

-- ---- Billing accounts -----------------------------------------------------

-- name: EnsureBillingAccount :exec
-- tenant: system - an org the caller resolved gets an account on the current book.
INSERT INTO billing_accounts (org_id, price_book_version) VALUES (@org_id, @price_book_version)
ON CONFLICT (org_id) DO NOTHING;

-- name: EnsureAllBillingAccounts :execrows
-- tenant: system - every live org without an account gets one.
INSERT INTO billing_accounts (org_id, price_book_version)
SELECT id, @price_book_version FROM organizations WHERE status <> 'deleted'
ON CONFLICT (org_id) DO NOTHING;

-- name: GetBillingAccount :one
-- tenant: system - the account of an org the caller resolved.
SELECT * FROM billing_accounts WHERE org_id = @org_id;

-- name: LockBillingAccount :one
-- tenant: system - the account of an org the caller resolved, for a change.
SELECT * FROM billing_accounts WHERE org_id = @org_id FOR UPDATE;

-- name: ListBillingAccounts :many
-- tenant: system - invoicing, repricing and the admin's views.
SELECT b.*, o.name AS org_name, o.status AS org_status FROM billing_accounts b
JOIN organizations o ON o.id = b.org_id WHERE o.status <> 'deleted' ORDER BY o.name;

-- name: UpdateBillingDetails :one
-- tenant: system - an org's business details and spend controls.
UPDATE billing_accounts SET legal_name = sqlc.narg(legal_name), address = sqlc.narg(address), tin = sqlc.narg(tin),
  vat_registered = @vat_registered, deducts_wht = @deducts_wht, budget_minor = sqlc.narg(budget_minor),
  spend_cap_minor = sqlc.narg(spend_cap_minor), updated_at = now()
WHERE org_id = @org_id RETURNING *;

-- name: SetBillingPlan :one
-- tenant: system - applies a plan change to an org the caller resolved.
UPDATE billing_accounts SET plan = @plan, term = @term, term_ends_at = sqlc.narg(term_ends_at),
  payment_terms_days = @payment_terms_days, updated_at = now()
WHERE org_id = @org_id RETURNING *;

-- name: SetBillingPriceBook :exec
-- tenant: system - repricing moves an org to a version.
UPDATE billing_accounts SET price_book_version = @price_book_version, updated_at = now() WHERE org_id = @org_id;

-- name: SetBillingAdmin :one
-- tenant: system - the admin's settings for an org (grandfathering, mode, terms).
UPDATE billing_accounts SET grandfathered = @grandfathered, mode = @mode, payment_terms_days = @payment_terms_days,
  price_book_version = @price_book_version, updated_at = now()
WHERE org_id = @org_id RETURNING *;

-- name: RepriceAccounts :many
-- tenant: system - moves orgs to @version on its effective date, except
-- grandfathered ones and annual terms still running.
UPDATE billing_accounts SET price_book_version = @version, updated_at = now()
WHERE price_book_version < @version AND NOT grandfathered
  AND NOT (term = 'annual' AND term_ends_at > @at::timestamptz)
RETURNING org_id;

-- name: SetOrgQuotaPlan :exec
-- tenant: system - a plan change sets the org's limits.
UPDATE organizations o SET plan_id = qp.id FROM quota_plans qp
WHERE o.id = @org_id AND qp.name = @quota_plan::text;

-- name: OrgQuotaPlanName :one
-- tenant: system - the org's quota plan name.
SELECT q.name FROM organizations o JOIN quota_plans q ON q.id = o.plan_id WHERE o.id = @org_id;

-- ---- Contacts -------------------------------------------------------------

-- name: ListBillingContacts :many
-- tenant: system - contacts of an org the caller resolved.
SELECT * FROM billing_contacts WHERE org_id = @org_id ORDER BY email;

-- name: AddBillingContact :one
-- tenant: system - a contact of an org the caller resolved.
INSERT INTO billing_contacts (org_id, email, name) VALUES (@org_id, @email, sqlc.narg(name))
ON CONFLICT (org_id, email) DO UPDATE SET name = EXCLUDED.name RETURNING *;

-- name: DeleteBillingContact :execrows
-- tenant: system - a contact of an org the caller resolved.
DELETE FROM billing_contacts WHERE org_id = @org_id AND email = @email;

-- name: BillingRecipients :many
-- tenant: system - who gets an org's billing email: its contacts, else
-- its owners and billing members.
SELECT c.email::text AS email FROM billing_contacts c WHERE c.org_id = @org_id
UNION
SELECT u.email::text FROM org_members m JOIN users u ON u.id = m.user_id
WHERE m.org_id = @org_id AND m.role IN ('owner', 'billing') AND u.disabled_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM billing_contacts WHERE org_id = @org_id);

-- ---- Plan changes ---------------------------------------------------------

-- name: InsertPlanChange :one
-- tenant: system - a change for an org the caller resolved.
INSERT INTO billing_plan_changes (org_id, from_plan, to_plan, from_term, to_term, effective_at, requested_by, applied, lines)
VALUES (@org_id, @from_plan, @to_plan, @from_term, @to_term, @effective_at, sqlc.narg(requested_by), @applied, @lines)
RETURNING *;

-- name: PendingPlanChange :one
-- tenant: system - an org's scheduled change.
SELECT * FROM billing_plan_changes WHERE org_id = @org_id AND NOT applied AND NOT cancelled
ORDER BY requested_at DESC LIMIT 1;

-- name: CancelPendingPlanChanges :execrows
-- tenant: system - a new choice replaces a scheduled one.
UPDATE billing_plan_changes SET cancelled = true WHERE org_id = @org_id AND NOT applied AND NOT cancelled;

-- name: DuePlanChanges :many
-- tenant: system - scheduled changes whose time has come.
SELECT * FROM billing_plan_changes WHERE NOT applied AND NOT cancelled AND effective_at <= @at ORDER BY effective_at;

-- name: MarkPlanChangeApplied :exec
-- tenant: system - a scheduled change took effect.
UPDATE billing_plan_changes SET applied = true, lines = @lines WHERE id = @id;

-- name: PlanChangesIn :many
-- tenant: system - an org's applied changes in a billing period.
SELECT * FROM billing_plan_changes WHERE org_id = @org_id AND applied
  AND effective_at >= @from_ts AND effective_at < @to_ts ORDER BY effective_at, requested_at;

-- ---- Rating ---------------------------------------------------------------

-- name: OrgUsageByDay :many
-- tenant: system - rating an org the caller resolved: each project's
-- metrics per UTC day in [@from_ts, @to_ts).
SELECT project_id, metric, date_trunc('day', period_start AT TIME ZONE 'UTC')::timestamp AS day,
       sum(quantity)::numeric AS quantity
FROM usage_records
WHERE org_id = @org_id AND period_start >= @from_ts AND period_start < @to_ts
GROUP BY project_id, metric, day ORDER BY day, project_id, metric;

-- name: ProjectNamesByID :many
-- tenant: system - names for invoice lines (deleted projects included).
SELECT id, name FROM projects WHERE id = ANY(@ids::uuid[]);

-- name: PlanChangesFrom :many
-- tenant: system - an org's applied changes from @from_ts on.
SELECT * FROM billing_plan_changes WHERE org_id = @org_id AND applied AND effective_at >= @from_ts
ORDER BY effective_at, requested_at;

-- name: ScheduledChangeBy :one
-- tenant: system - the scheduled change in effect at @at.
SELECT * FROM billing_plan_changes WHERE org_id = @org_id AND NOT applied AND NOT cancelled AND effective_at <= @at
ORDER BY requested_at DESC LIMIT 1;

-- ---- Invoices -------------------------------------------------------------

-- name: GetOrgInvoiceForPeriod :one
-- tenant: system - an org's invoice for a month.
SELECT * FROM invoices WHERE org_id = @org_id AND period_start = @period_start;

-- name: InsertDraftInvoice :one
-- tenant: system - a draft for an org the caller resolved.
INSERT INTO invoices (org_id, period_start, period_end, status, subtotal_minor, vat_minor, total_minor,
                      wht_expected_minor, vat_rate, price_book_version)
VALUES (@org_id, @period_start, @period_end, 'draft', @subtotal_minor, @vat_minor, @total_minor,
        @wht_expected_minor, @vat_rate, @price_book_version)
RETURNING *;

-- name: UpdateDraftInvoice :one
-- tenant: system - re-rating a draft.
UPDATE invoices SET subtotal_minor = @subtotal_minor, vat_minor = @vat_minor, total_minor = @total_minor,
  wht_expected_minor = @wht_expected_minor, vat_rate = @vat_rate, price_book_version = @price_book_version
WHERE id = @id AND status = 'draft' RETURNING *;

-- name: DeleteDraftInvoice :execrows
-- tenant: system - a draft with nothing left to bill.
DELETE FROM invoices WHERE id = @id AND status = 'draft';

-- name: DeleteInvoiceLines :exec
-- tenant: system - replacing a draft's lines.
DELETE FROM invoice_lines WHERE invoice_id = @invoice_id
  AND EXISTS (SELECT 1 FROM invoices WHERE id = @invoice_id AND status = 'draft');

-- name: InsertInvoiceLine :exec
-- tenant: system - a draft's line.
INSERT INTO invoice_lines (invoice_id, kind, description, project_id, metric, quantity, unit_price_minor, amount_minor, revenue_account)
VALUES (@invoice_id, @kind, @description, sqlc.narg(project_id), sqlc.narg(metric), @quantity, @unit_price_minor, @amount_minor, @revenue_account);

-- name: InvoiceLines :many
-- tenant: system - lines of an invoice the caller resolved.
SELECT * FROM invoice_lines WHERE invoice_id = @invoice_id ORDER BY id;

-- name: GetInvoice :one
-- tenant: system - an invoice by id (admin, or after the org check).
SELECT * FROM invoices WHERE id = @id;

-- name: GetOrgInvoice :one
-- tenant: org - an invoice of the org in the request.
SELECT * FROM invoices WHERE id = @id AND org_id = @org_id;

-- name: LockInvoice :one
-- tenant: system - an invoice for a change.
SELECT * FROM invoices WHERE id = @id FOR UPDATE;

-- name: ListOrgInvoices :many
-- tenant: org - the org's issued invoices (drafts are the admin's).
SELECT * FROM invoices WHERE org_id = @org_id AND status <> 'draft' ORDER BY period_start DESC, created_at DESC LIMIT @lim;

-- name: ListInvoices :many
-- tenant: system - the admin's invoices.
SELECT i.*, o.name AS org_name FROM invoices i JOIN organizations o ON o.id = i.org_id
WHERE (sqlc.narg(status)::text IS NULL OR i.status = sqlc.narg(status))
  AND (sqlc.narg(period_start)::date IS NULL OR i.period_start = sqlc.narg(period_start))
ORDER BY i.period_start DESC, o.name LIMIT @lim;

-- name: DraftsForPeriod :many
-- tenant: system - a month's drafts, for issuing.
SELECT * FROM invoices WHERE status = 'draft' AND period_start = @period_start ORDER BY created_at;

-- name: SetInvoiceHold :one
-- tenant: system - the admin holds or releases a draft.
UPDATE invoices SET held = @held, hold_reason = sqlc.narg(hold_reason)
WHERE id = @id AND status = 'draft' RETURNING *;

-- name: NextBillingNumber :one
-- tenant: system - the next number of a kind in a year; never reused.
INSERT INTO billing_sequences (kind, year, last) VALUES (@kind, @year, 1)
ON CONFLICT (kind, year) DO UPDATE SET last = billing_sequences.last + 1
RETURNING last;

-- name: IssueInvoice :one
-- tenant: system - a draft becomes an issued invoice.
UPDATE invoices SET status = @status, number = @number, issued_at = @issued_at, due_at = @due_at,
  bill_to = @bill_to, seller = @seller, paid_at = sqlc.narg(paid_at)
WHERE id = @id AND status = 'draft' RETURNING *;

-- ---- Credit notes ---------------------------------------------------------

-- name: InsertCreditNote :one
-- tenant: system - a credit note on an invoice the caller resolved.
INSERT INTO credit_notes (invoice_id, org_id, number, amount_minor, vat_minor, reason, issued_by)
VALUES (@invoice_id, @org_id, @number, @amount_minor, @vat_minor, @reason, sqlc.narg(issued_by))
RETURNING *;

-- name: InvoiceCreditNotes :many
-- tenant: system - credit notes of an invoice the caller resolved.
SELECT * FROM credit_notes WHERE invoice_id = @invoice_id ORDER BY issued_at;

-- name: GetCreditNote :one
-- tenant: system - a credit note by id.
SELECT * FROM credit_notes WHERE id = @id;

-- ---- Spend controls -------------------------------------------------------

-- name: UpdateForecast :exec
-- tenant: system - the hourly forecast of an org the caller resolved.
UPDATE billing_accounts SET forecast_minor = @forecast_minor, forecast_at = @forecast_at, capped = @capped,
  budget_alerted = @budget_alerted, budget_month = @budget_month
WHERE org_id = @org_id;

-- name: OrgSpendCapped :one
-- tenant: system - whether an org has reached its spend cap (V3 §3.10).
SELECT coalesce((SELECT capped FROM billing_accounts WHERE org_id = @org_id), false)::bool;

-- name: OrgDunningState :one
-- tenant: system - an org's dunning state, for the creation checks.
SELECT coalesce((SELECT dunning_state FROM billing_accounts WHERE org_id = @org_id), 'ok')::text;
