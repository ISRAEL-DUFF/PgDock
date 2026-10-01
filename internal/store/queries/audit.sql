-- name: InsertAudit :exec
INSERT INTO audit_log (operator_id, action, target_type, target_id, detail, ip, user_agent, outcome)
VALUES (sqlc.narg(operator_id), @action, sqlc.narg(target_type), sqlc.narg(target_id), @detail, sqlc.narg(ip), sqlc.narg(user_agent), @outcome);

-- name: ListAudit :many
SELECT a.*, o.email AS operator_email
FROM audit_log a LEFT JOIN operators o ON o.id = a.operator_id
WHERE (sqlc.narg(action)::text IS NULL OR a.action LIKE sqlc.narg(action) || '%')
  AND (sqlc.narg(outcome)::text IS NULL OR a.outcome = sqlc.narg(outcome))
  AND (sqlc.narg(target_id)::text IS NULL OR a.target_id = sqlc.narg(target_id))
  AND (sqlc.narg(before_id)::bigint IS NULL OR a.id < sqlc.narg(before_id))
ORDER BY a.id DESC
LIMIT @max_rows;
