-- name: StartMove :one
-- tenant: system - the move worker records its own progress.
INSERT INTO moves (operation_id, project_id, source_instance, target_instance, mode, fallback_reason)
VALUES (@operation_id, @project_id, @source_instance, @target_instance, @mode, sqlc.narg(fallback_reason))
ON CONFLICT (operation_id) DO UPDATE SET mode = EXCLUDED.mode, fallback_reason = EXCLUDED.fallback_reason,
  phase = 'preparing', started_at = now(), finished_at = NULL, freeze_ms = NULL
RETURNING *;

-- name: MovePhase :exec
-- tenant: system - the move worker records its own progress.
UPDATE moves SET phase = @phase, tables_total = coalesce(sqlc.narg(tables_total), tables_total),
  tables_ready = coalesce(sqlc.narg(tables_ready), tables_ready), lag_bytes = coalesce(sqlc.narg(lag_bytes), lag_bytes),
  freeze_ms = coalesce(sqlc.narg(freeze_ms), freeze_ms),
  finished_at = CASE WHEN @phase IN ('done', 'failed') THEN now() END
WHERE operation_id = @operation_id;

-- name: LatestMoves :many
-- tenant: system - moves of a project the request already authorized.
SELECT * FROM moves WHERE project_id = @project_id ORDER BY started_at DESC LIMIT @lim;

-- name: MoveCutOver :exec
-- tenant: system - move workers switch the project's instance.
UPDATE projects SET instance_id = @instance_id WHERE id = @id;
