-- name: ListLiveSharedInstances :many
SELECT i.id, i.node_id, n.name AS node_name, i.status
FROM instances i JOIN nodes n ON n.id = i.node_id
WHERE i.kind = 'shared' AND i.deleted_at IS NULL AND n.status <> 'removed'
ORDER BY n.name, i.created_at;

-- name: InstanceProjects :many
-- tenant: system - the platform's isolation checks.
-- Live projects whose database is on an instance.
SELECT id, db_name, owner_role FROM projects
WHERE instance_id = @instance_id AND deleted_at IS NULL AND status NOT IN ('provisioning', 'deleting', 'error')
ORDER BY db_name;

-- name: LatestIsolationChecks :many
-- tenant: system - the platform's isolation checks.
-- The newest isolation_check operation per instance.
SELECT DISTINCT ON (params->>'instance_id') (params->>'instance_id')::uuid AS instance_id, id, status, error, created_at, finished_at
FROM operations
WHERE kind = 'isolation_check'
ORDER BY params->>'instance_id', created_at DESC;
