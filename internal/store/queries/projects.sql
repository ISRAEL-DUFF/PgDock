-- name: InsertProject :one
INSERT INTO projects (id, org_id, name, slug, db_name, owner_role, scram_verifier, tier, instance_id, status, settings, description, created_by)
VALUES (@id, @org_id, @name, @slug, @db_name, @owner_role, @scram_verifier, @tier, @instance_id, 'provisioning', @settings, sqlc.narg(description), sqlc.narg(created_by))
RETURNING *;

-- name: GetProject :one
-- tenant: system - provisioning workers and the poolers, or a project the request already authorized.
SELECT * FROM projects WHERE id = @id;

-- name: GetLiveProjectForUpdate :one
-- tenant: system - provisioning workers and the poolers, or a project the request already authorized.
SELECT * FROM projects WHERE id = @id AND deleted_at IS NULL FOR UPDATE;

-- name: ListLiveProjects :many
-- tenant: system - provisioning workers and the poolers, or a project the request already authorized.
SELECT * FROM projects
WHERE deleted_at IS NULL
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
ORDER BY created_at DESC, id DESC
LIMIT @max_rows;

-- name: SetProjectStatus :exec
-- tenant: system - provisioning workers and the poolers, or a project the request already authorized.
UPDATE projects SET status = @status WHERE id = @id;

-- name: SetProjectVerifier :exec
-- tenant: system - provisioning workers and the poolers, or a project the request already authorized.
UPDATE projects SET scram_verifier = @scram_verifier WHERE id = @id;

-- name: SoftDeleteProject :exec
-- tenant: system - provisioning workers and the poolers, or a project the request already authorized.
UPDATE projects SET status = @status, deleted_at = now() WHERE id = @id;

-- PoolerRoutes lists every project the pooler should route to, with the
-- backend address the pooler uses and the SCRAM verifier for its auth file.
-- name: PoolerRoutes :many
-- tenant: system - provisioning workers and the poolers, or a project the request already authorized.
SELECT p.db_name, p.owner_role, p.scram_verifier, p.settings,
       COALESCE(i.host, n.private_addr)::text AS host, i.port
FROM projects p
JOIN instances i ON i.id = p.instance_id
JOIN nodes n ON n.id = i.node_id
WHERE p.deleted_at IS NULL
  AND p.status IN ('provisioning', 'active', 'promoting', 'restoring')
ORDER BY p.db_name;

-- name: UpdateProjectMeta :one
-- tenant: system - provisioning workers and the poolers, or a project the request already authorized.
UPDATE projects SET name = @name, description = sqlc.narg(description), settings = @settings
WHERE id = @id
RETURNING *;
