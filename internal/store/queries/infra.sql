-- name: UpsertNode :one
INSERT INTO nodes (name, private_addr, role, pg_admin_secret, capacity)
VALUES (@name, @private_addr, @role, @pg_admin_secret, '{}')
ON CONFLICT (name) DO UPDATE
SET private_addr = EXCLUDED.private_addr, role = EXCLUDED.role, pg_admin_secret = EXCLUDED.pg_admin_secret
RETURNING *;

-- name: SetNodeAdminSecret :exec
UPDATE nodes SET pg_admin_secret = @pg_admin_secret WHERE id = @id;

-- name: UpsertSharedInstance :one
INSERT INTO instances (node_id, kind, pg_version, port, admin_host, admin_port, status)
VALUES (@node_id, 'shared', @pg_version, @port, sqlc.narg(admin_host), sqlc.narg(admin_port), 'running')
ON CONFLICT (node_id) WHERE kind = 'shared' DO UPDATE
SET pg_version = EXCLUDED.pg_version, port = EXCLUDED.port,
    admin_host = EXCLUDED.admin_host, admin_port = EXCLUDED.admin_port, status = 'running'
RETURNING *;

-- PickSharedInstance chooses the running shared instance hosting the fewest
-- live projects.
-- name: PickSharedInstance :one
SELECT i.* FROM instances i
JOIN nodes n ON n.id = i.node_id
WHERE i.kind = 'shared' AND i.status = 'running' AND n.status = 'healthy'
ORDER BY (SELECT count(*) FROM projects p WHERE p.instance_id = i.id AND p.deleted_at IS NULL), i.created_at
LIMIT 1;

-- name: GetInstanceTarget :one
SELECT i.id, i.kind, i.port, i.admin_host, i.admin_port,
       n.id AS node_id, n.name AS node_name, n.private_addr, n.pg_admin_secret
FROM instances i JOIN nodes n ON n.id = i.node_id
WHERE i.id = @id;
