-- The revenue dashboard (V3 §7.2).

-- name: UpsertMRRSnapshot :exec
-- tenant: system - the daily snapshot of every organisation's recurring revenue.
INSERT INTO mrr_snapshots (month, org_id, plan, term, mrr_minor) VALUES (@month, @org_id, @plan, @term, @mrr_minor)
ON CONFLICT (month, org_id) DO UPDATE SET plan = EXCLUDED.plan, term = EXCLUDED.term, mrr_minor = EXCLUDED.mrr_minor, updated_at = now();

-- name: MRRSnapshots :many
-- tenant: system - the platform admin's revenue dashboard, across organisations.
SELECT month, org_id, plan, mrr_minor FROM mrr_snapshots WHERE month >= @from_month AND month <= @to_month ORDER BY month;

-- name: InvoiceTotalsByPeriod :many
-- tenant: system - the revenue dashboard's collections, per invoice month.
SELECT period_start, count(*)::bigint AS issued, coalesce(sum(total_minor), 0)::bigint AS total_minor,
  coalesce(sum(least(paid_minor + wht_deducted_minor, total_minor)), 0)::bigint AS paid_minor
FROM invoices WHERE status NOT IN ('draft', 'void') AND period_start >= @from_month AND period_start <= @to_month
GROUP BY period_start ORDER BY period_start;

-- name: UsageRevenueByPeriod :many
-- tenant: system - the revenue dashboard's metered revenue (overage, dedicated, add-ons), per invoice month.
SELECT i.period_start, coalesce(sum(l.amount_minor), 0)::bigint AS amount_minor
FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id
WHERE i.status NOT IN ('draft', 'void') AND l.kind IN ('overage', 'dedicated', 'addon')
  AND i.period_start >= @from_month AND i.period_start <= @to_month
GROUP BY i.period_start ORDER BY i.period_start;

-- name: OpenReceivables :many
-- tenant: system - accounts-receivable ageing across organisations.
SELECT i.id, i.org_id, o.name AS org_name, i.number, i.due_at,
  (i.total_minor - i.paid_minor - i.wht_deducted_minor -
   coalesce((SELECT sum(amount_minor + vat_minor) FROM credit_notes c WHERE c.invoice_id = i.id), 0))::bigint AS outstanding_minor
FROM invoices i JOIN organizations o ON o.id = i.org_id
WHERE i.status IN ('issued', 'partially_paid') ORDER BY i.due_at;
