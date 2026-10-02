-- name: UpsertMetricPoints :exec
-- tenant: system - the metrics collector, or a project the request already authorized.
-- One row per (scope_id, metric) pair in the arrays, at one timestamp.
INSERT INTO metric_points (scope, scope_id, metric, ts, resolution, value)
SELECT @scope::text, unnest(@scope_ids::uuid[]), unnest(@metrics::text[]), @ts::timestamptz, '1m', unnest(@vals::float8[])
ON CONFLICT (scope, scope_id, metric, resolution, ts) DO UPDATE SET value = EXCLUDED.value;

-- name: DownsampleMetrics :execrows
-- tenant: system - the metrics collector, or a project the request already authorized.
-- Hourly averages of the 1-minute points of completed hours since @since.
INSERT INTO metric_points (scope, scope_id, metric, ts, resolution, value)
SELECT scope, scope_id, metric, date_trunc('hour', ts), '1h', avg(value)
FROM metric_points
WHERE resolution = '1m' AND ts >= date_trunc('hour', @since::timestamptz) AND ts < date_trunc('hour', now())
GROUP BY scope, scope_id, metric, date_trunc('hour', ts)
ON CONFLICT (scope, scope_id, metric, resolution, ts) DO UPDATE SET value = EXCLUDED.value;

-- name: PruneMetrics :execrows
-- tenant: system - the metrics collector, or a project the request already authorized.
DELETE FROM metric_points
WHERE (resolution = '1m' AND ts < now() - interval '24 hours')
   OR (resolution = '1h' AND ts < now() - interval '30 days');

-- name: MetricSeries :many
-- tenant: system - the metrics collector, or a project the request already authorized.
SELECT metric, ts, value FROM metric_points
WHERE scope = @scope AND scope_id = @scope_id AND resolution = @resolution
  AND ts >= @since AND (cardinality(@metrics::text[]) = 0 OR metric = ANY(@metrics::text[]))
ORDER BY metric, ts;

-- name: LatestMetrics :many
-- tenant: system - the metrics collector, or a project the request already authorized.
SELECT DISTINCT ON (scope, scope_id, metric) scope, scope_id, metric, ts, value
FROM metric_points
WHERE resolution = '1m' AND ts >= @since
ORDER BY scope, scope_id, metric, ts DESC;

-- name: AddProjectExtension :exec
-- tenant: system - the metrics collector, or a project the request already authorized.
UPDATE projects SET extensions = (SELECT array_agg(DISTINCT e ORDER BY e) FROM unnest(extensions || @name::text) AS e)
WHERE id = @id;
