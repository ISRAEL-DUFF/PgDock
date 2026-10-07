-- name: InsertRetiredDatabase :one
-- tenant: system - promotion workers.
INSERT INTO retired_databases (project_id, instance_id, db_name, owner_role, reason, drop_after)
VALUES (@project_id, @instance_id, @db_name, @owner_role, @reason, @drop_after)
RETURNING *;

-- name: DueRetiredDatabases :many
-- tenant: system - promotion workers.
SELECT * FROM retired_databases WHERE dropped_at IS NULL AND drop_after <= now() ORDER BY drop_after;

-- name: MarkRetiredDropped :exec
-- tenant: system - promotion workers.
UPDATE retired_databases SET dropped_at = now() WHERE id = @id;

-- name: LiveRetiredForProject :one
-- tenant: system - promotion workers.
SELECT * FROM retired_databases WHERE project_id = @project_id AND dropped_at IS NULL ORDER BY created_at DESC LIMIT 1;

-- name: CutOverProject :exec
-- tenant: system - promotion workers.
UPDATE projects SET instance_id = @instance_id, tier = 'dedicated', settings = @settings WHERE id = @id;

-- name: DemoteCutOver :exec
-- tenant: system - demotion workers.
UPDATE projects SET instance_id = @instance_id, tier = 'shared', settings = @settings WHERE id = @id;

-- name: ListLiveRetiredForProject :many
-- tenant: system - demotion workers.
SELECT * FROM retired_databases WHERE project_id = @project_id AND dropped_at IS NULL ORDER BY created_at;

-- name: ExpireBaseBackups :execrows
-- tenant: system - demotion workers.
-- After a demotion the WAL-G base backups stay restorable until retention
-- would have dropped them (V2 s5.4).
UPDATE backups SET expires_at = @expires_at
WHERE project_id = @project_id::uuid AND kind = 'base' AND status = 'succeeded' AND expires_at IS NULL;

-- name: SharedClustersForOrg :many
-- tenant: system - placement across organisations' shared clusters.
-- The shared clusters a project of the organisation may be placed on (its
-- own when it has any, V2 s10.5), with live projects and the node's latest
-- measured free disk (-1: not measured yet).
SELECT i.id, i.node_id, n.name AS node_name, (i.org_id IS NOT NULL)::bool AS org_cluster,
       (SELECT count(*) FROM projects p WHERE p.instance_id = i.id AND p.deleted_at IS NULL)::int AS projects,
       COALESCE((SELECT t.value - u.value FROM metric_points t JOIN metric_points u
          ON u.scope = t.scope AND u.scope_id = t.scope_id AND u.ts = t.ts AND u.resolution = t.resolution AND u.metric = 'disk_used_bytes'
        WHERE t.scope = 'node' AND t.scope_id = n.id AND t.metric = 'disk_total_bytes' AND t.resolution = '1m'
        ORDER BY t.ts DESC LIMIT 1), -1)::float8 AS free_bytes
FROM instances i JOIN nodes n ON n.id = i.node_id
WHERE i.kind = 'shared' AND i.status = 'running' AND n.status = 'healthy' AND n.lifecycle = 'active' AND n.role IN ('shared', 'both')
  AND i.deleted_at IS NULL AND i.pg_version = @pg_version AND n.region = @region
  AND CASE WHEN EXISTS (SELECT 1 FROM instances x WHERE x.kind = 'shared' AND x.deleted_at IS NULL AND x.org_id = @org_id)
           THEN i.org_id = @org_id ELSE i.org_id IS NULL END
ORDER BY i.created_at;

-- name: PeakProjectConnections :one
-- tenant: system - a project the request already authorized.
-- The most backend connections the project had at once since @since.
SELECT COALESCE(max(a.value + COALESCE(i.value, 0)), 0)::float8
FROM metric_points a LEFT JOIN metric_points i
  ON i.scope = a.scope AND i.scope_id = a.scope_id AND i.ts = a.ts AND i.resolution = a.resolution AND i.metric = 'connections_idle'
WHERE a.scope = 'project' AND a.scope_id = @project_id AND a.metric = 'connections_active' AND a.ts >= @since;
