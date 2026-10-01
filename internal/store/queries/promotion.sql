-- name: InsertRetiredDatabase :one
INSERT INTO retired_databases (project_id, instance_id, db_name, owner_role, reason, drop_after)
VALUES (@project_id, @instance_id, @db_name, @owner_role, @reason, @drop_after)
RETURNING *;

-- name: DueRetiredDatabases :many
SELECT * FROM retired_databases WHERE dropped_at IS NULL AND drop_after <= now() ORDER BY drop_after;

-- name: MarkRetiredDropped :exec
UPDATE retired_databases SET dropped_at = now() WHERE id = @id;

-- name: LiveRetiredForProject :one
SELECT * FROM retired_databases WHERE project_id = @project_id AND dropped_at IS NULL ORDER BY created_at DESC LIMIT 1;

-- name: CutOverProject :exec
UPDATE projects SET instance_id = @instance_id, tier = 'dedicated', settings = @settings WHERE id = @id;
