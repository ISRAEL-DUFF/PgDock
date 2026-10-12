-- Cost attribution and exchange rates (V3 §5.4, §7.2).

-- name: InsertFXRate :one
INSERT INTO fx_rates (currency, ngn_per_unit, effective_at, source, set_by)
VALUES (@currency, @ngn_per_unit, @effective_at, @source, sqlc.narg(set_by))
RETURNING *;

-- name: LatestFXRates :many
SELECT DISTINCT ON (currency) * FROM fx_rates WHERE effective_at <= @at ORDER BY currency, effective_at DESC, id DESC;

-- name: FXRateHistory :many
SELECT * FROM fx_rates ORDER BY effective_at DESC, id DESC LIMIT 200;

-- name: CostNodesForDay :many
-- Nodes that existed on the day, with what they cost and what runs there.
SELECT n.id, n.name, n.region, n.role, n.status, n.monthly_cost_minor, n.cost_currency, n.capacity, n.created_at
FROM nodes n
WHERE n.role <> 'pooler' AND n.monthly_cost_minor IS NOT NULL AND n.created_at < @day_end;

-- name: CostInstances :many
-- tenant: system - every live instance and the project on it, for cost attribution.
SELECT i.id, i.node_id, i.kind, i.cpu_limit, i.mem_limit_mb, i.volume_gb, i.ha_enabled,
  p.id AS project_id, p.org_id
FROM instances i LEFT JOIN projects p ON p.instance_id = i.id AND p.deleted_at IS NULL
WHERE i.deleted_at IS NULL;

-- name: CostMembers :many
-- tenant: system - HA instances' members (the primary and its standbys):
-- the node each runs on, for cost attribution.
SELECT m.node_id, m.instance_id FROM instance_members m WHERE m.deleted_at IS NULL AND m.role <> 'stopped';

-- name: CostUsageForDay :many
-- tenant: system - every organisation's usage on a day, for cost attribution,
-- with each project's region (backend services' edge cost is split by region).
SELECT u.org_id, u.project_id, u.metric, coalesce(p.region, '')::text AS region, sum(u.quantity)::float8 AS quantity
FROM usage_records u LEFT JOIN projects p ON p.id = u.project_id
WHERE u.period_start >= @day_start AND u.period_start < @day_end
  AND u.metric IN ('shared_storage_gb_hours', 'backup_storage_gb_hours', 'pooler_transfer_gb', 'branch_gb_hours',
    'api_requests', 'realtime_connection_minutes', 'storage_gb_hours', 'storage_egress_gb',
    'messages_sms_cost_kobo', 'messages_whatsapp_cost_kobo')
GROUP BY u.org_id, u.project_id, u.metric, p.region;

-- name: CostConnectionHours :many
-- tenant: system - each project's client connection-hours on a day (hourly
-- averages of active and idle backends), for cost attribution.
SELECT scope_id AS project_id, sum(value)::float8 AS connection_hours
FROM metric_points
WHERE scope = 'project' AND resolution = '1h' AND metric IN ('connections_active', 'connections_idle')
  AND ts >= @day_start AND ts < @day_end
GROUP BY scope_id;

-- name: DeleteCostAllocations :exec
-- tenant: system - a day's attribution is recomputed whole.
DELETE FROM cost_allocations WHERE day = @day;

-- name: InsertCostAllocation :exec
-- tenant: system - the daily cost attribution.
INSERT INTO cost_allocations (day, org_id, category, region, currency, amount_minor, quantity)
VALUES (@day, @org_id, @category, @region, @currency, @amount_minor, @quantity)
ON CONFLICT (day, org_id, category, region, currency) DO UPDATE
SET amount_minor = cost_allocations.amount_minor + EXCLUDED.amount_minor, quantity = cost_allocations.quantity + EXCLUDED.quantity;

-- name: CostAllocationsBetween :many
-- tenant: system - the margin dashboard, across organisations.
SELECT day, org_id, category, region, currency, amount_minor, quantity
FROM cost_allocations WHERE day >= @from_day AND day < @to_day ORDER BY day;

-- name: OrgCostAllocations :many
SELECT day, category, region, currency, amount_minor, quantity
FROM cost_allocations WHERE org_id = @org_id AND day >= @from_day AND day < @to_day ORDER BY day;

-- name: AttributedDays :many
-- tenant: system - which days have been attributed.
SELECT DISTINCT day FROM cost_allocations WHERE day >= @from_day AND day < @to_day ORDER BY day;

-- name: CostOrgs :many
-- tenant: system - organisations with their plan, for the margin dashboard.
SELECT o.id, o.name, o.status, coalesce(b.plan, 'free')::text AS plan
FROM organizations o LEFT JOIN billing_accounts b ON b.org_id = o.id
WHERE o.status <> 'deleted' OR EXISTS (SELECT 1 FROM cost_allocations c WHERE c.org_id = o.id AND c.day >= @from_day);
