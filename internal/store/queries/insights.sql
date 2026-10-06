-- Query insights (V3 §8).

-- name: InsightsProjects :many
-- tenant: system - the query insights collector, across organisations.
-- Live projects with their instance and their organisation's plan.
SELECT p.id, p.org_id, p.instance_id, p.db_name, p.tier, COALESCE(b.plan, 'free')::text AS plan
FROM projects p
LEFT JOIN billing_accounts b ON b.org_id = p.org_id
WHERE p.deleted_at IS NULL AND p.status = 'active' AND p.lifecycle = 'active'
ORDER BY p.instance_id, p.db_name;

-- name: ProjectPlan :one
-- tenant: system - a project the request already authorized.
SELECT COALESCE((SELECT b.plan FROM billing_accounts b WHERE b.org_id = @org_id), 'free')::text;

-- name: ListQuerySnapshots :many
-- tenant: system - the query insights collector.
SELECT * FROM query_snapshots WHERE instance_id = @instance_id;

-- name: DeleteQuerySnapshots :exec
-- tenant: system - the query insights collector.
DELETE FROM query_snapshots WHERE instance_id = @instance_id;

-- name: InsertQuerySnapshots :copyfrom
-- tenant: system - the query insights collector keeps the cumulative counters.
INSERT INTO query_snapshots (instance_id, dbid, userid, queryid, toplevel, calls, total_ms, rows, shared_blks_hit, shared_blks_read, max_ms, taken_at)
VALUES (@instance_id, @dbid, @userid, @queryid, @toplevel, @calls, @total_ms, @rows, @shared_blks_hit, @shared_blks_read, @max_ms, @taken_at);

-- name: AddQueryStats :batchexec
-- tenant: system - the query insights collector adds a snapshot's deltas.
INSERT INTO query_stats (project_id, queryid, bucket, calls, total_ms, rows, shared_blks_hit, shared_blks_read, max_ms)
VALUES (@project_id, @queryid, @bucket, @calls, @total_ms, @rows, @shared_blks_hit, @shared_blks_read, @max_ms)
ON CONFLICT (project_id, queryid, bucket) DO UPDATE SET
  calls = query_stats.calls + EXCLUDED.calls, total_ms = query_stats.total_ms + EXCLUDED.total_ms,
  rows = query_stats.rows + EXCLUDED.rows, shared_blks_hit = query_stats.shared_blks_hit + EXCLUDED.shared_blks_hit,
  shared_blks_read = query_stats.shared_blks_read + EXCLUDED.shared_blks_read, max_ms = GREATEST(query_stats.max_ms, EXCLUDED.max_ms);

-- name: UpsertQueryTexts :batchexec
-- tenant: system - the query insights collector records normalised texts.
INSERT INTO query_texts (project_id, queryid, query)
VALUES (@project_id, @queryid, @query)
ON CONFLICT (project_id, queryid) DO UPDATE SET query = EXCLUDED.query, last_seen = now();

-- name: SetQueryExample :exec
-- tenant: system - the query insights collector keeps the latest example with literals.
UPDATE query_texts SET example = @example, example_at = now() WHERE project_id = @project_id AND queryid = @queryid;

-- name: InsertSlowQuery :exec
-- tenant: system - the query insights collector logs a slow statement.
INSERT INTO slow_queries (project_id, queryid, query, duration_ms, source, role_name)
VALUES (@project_id, sqlc.narg(queryid), @query, @duration_ms, @source, @role_name);

-- name: RecentSlowQuery :one
-- tenant: system - the collector doesn't log one running statement twice.
SELECT EXISTS (SELECT 1 FROM slow_queries WHERE project_id = @project_id AND query = @query AND source = 'running'
  AND seen_at > now() - interval '10 minutes')::bool;

-- name: RollUpQueryStats :exec
-- tenant: system - the query insights collector folds 5-minute buckets older than a day into hours.
WITH old AS (
  DELETE FROM query_stats d WHERE d.bucket < @before AND d.bucket <> date_trunc('hour', d.bucket) RETURNING d.*
)
INSERT INTO query_stats (project_id, queryid, bucket, calls, total_ms, rows, shared_blks_hit, shared_blks_read, max_ms)
SELECT project_id, queryid, date_trunc('hour', bucket), sum(calls), sum(total_ms), sum(rows), sum(shared_blks_hit), sum(shared_blks_read), max(max_ms)
FROM old GROUP BY 1, 2, 3
ON CONFLICT (project_id, queryid, bucket) DO UPDATE SET
  calls = query_stats.calls + EXCLUDED.calls, total_ms = query_stats.total_ms + EXCLUDED.total_ms,
  rows = query_stats.rows + EXCLUDED.rows, shared_blks_hit = query_stats.shared_blks_hit + EXCLUDED.shared_blks_hit,
  shared_blks_read = query_stats.shared_blks_read + EXCLUDED.shared_blks_read, max_ms = GREATEST(query_stats.max_ms, EXCLUDED.max_ms);

-- name: PruneQueryInsights :exec
-- tenant: system - the query insights collector keeps 30 days.
WITH a AS (DELETE FROM query_stats WHERE bucket < @before),
     b AS (DELETE FROM slow_queries WHERE seen_at < @before)
DELETE FROM query_texts t WHERE t.last_seen < @before;

-- name: TopQueries :many
-- tenant: system - a project the request already authorized.
SELECT s.queryid, COALESCE(t.query, '')::text AS query, (t.example IS NOT NULL)::bool AS has_example,
  sum(s.calls)::bigint AS calls, sum(s.total_ms)::float8 AS total_ms, sum(s.rows)::bigint AS rows,
  COALESCE(sum(s.total_ms) / NULLIF(sum(s.calls), 0), 0)::float8 AS mean_ms, max(s.max_ms)::float8 AS max_ms,
  COALESCE(sum(s.shared_blks_hit)::float8 / NULLIF(sum(s.shared_blks_hit) + sum(s.shared_blks_read), 0), 1)::float8 AS hit_ratio
FROM query_stats s
LEFT JOIN query_texts t ON t.project_id = s.project_id AND t.queryid = s.queryid
WHERE s.project_id = @project_id AND s.bucket >= @since
GROUP BY s.queryid, t.query, t.example
HAVING sum(s.calls) > 0
ORDER BY CASE @sort::text
  WHEN 'mean' THEN sum(s.total_ms) / NULLIF(sum(s.calls), 0)
  WHEN 'calls' THEN sum(s.calls)::float8
  WHEN 'rows' THEN sum(s.rows)::float8
  ELSE sum(s.total_ms) END DESC NULLS LAST, s.queryid
LIMIT @lim;

-- name: QueryTotals :one
-- tenant: system - a project the request already authorized.
SELECT COALESCE(sum(calls), 0)::bigint AS calls, COALESCE(sum(total_ms), 0)::float8 AS total_ms
FROM query_stats WHERE project_id = @project_id AND bucket >= @since;

-- name: GetQueryText :one
-- tenant: system - a project the request already authorized.
SELECT * FROM query_texts WHERE project_id = @project_id AND queryid = @queryid;

-- name: QuerySeries :many
-- tenant: system - a project the request already authorized.
SELECT date_bin(@step::interval, bucket, '2000-01-01'::timestamptz)::timestamptz AS ts,
  sum(calls)::bigint AS calls, sum(total_ms)::float8 AS total_ms, sum(rows)::bigint AS rows, max(max_ms)::float8 AS max_ms
FROM query_stats WHERE project_id = @project_id AND queryid = @queryid AND bucket >= @since
GROUP BY 1 ORDER BY 1;

-- name: ListSlowQueries :many
-- tenant: system - a project the request already authorized. With the reaper's cancellations.
SELECT * FROM (
  SELECT q.queryid, q.query, q.duration_ms, q.source, q.role_name, q.seen_at FROM slow_queries q
  WHERE q.project_id = @project_id AND q.seen_at >= @since
  UNION ALL
  SELECT NULL::bigint, COALESCE(r.query, ''), (r.duration_s * 1000)::float8, 'reaper', r.role_name, r.created_at FROM reaped_sessions r
  WHERE r.project_id = @project_id AND r.kind = 'statement' AND r.created_at >= @since
) s ORDER BY seen_at DESC LIMIT @lim;
