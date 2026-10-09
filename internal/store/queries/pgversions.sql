-- name: EnsurePgVersion :exec
-- tenant: platform - Postgres versions are platform-wide.
INSERT INTO pg_versions (major) VALUES (@major) ON CONFLICT (major) DO NOTHING;

-- name: ListPgVersions :many
-- tenant: system - a platform-wide count of the projects on each major.
SELECT v.*,
  (SELECT count(*) FROM projects p JOIN instances i ON i.id = p.instance_id
   WHERE i.pg_version = v.major AND p.deleted_at IS NULL)::int AS projects
FROM pg_versions v ORDER BY v.major;

-- name: GetPgVersion :one
-- tenant: platform - Postgres versions are platform-wide.
SELECT * FROM pg_versions WHERE major = @major;

-- name: UpdatePgVersion :one
-- tenant: platform - Postgres versions are platform-wide.
UPDATE pg_versions SET status = @status, deprecated_at = sqlc.narg(deprecated_at), retires_at = sqlc.narg(retires_at),
  notes = @notes, updated_at = now()
WHERE major = @major RETURNING *;

-- name: ProjectsOnPgVersion :many
-- tenant: system - the version lifecycle tells every organisation with a project on a major.
SELECT p.id, p.org_id, p.name, o.name AS org_name FROM projects p
JOIN instances i ON i.id = p.instance_id JOIN organizations o ON o.id = p.org_id
WHERE i.pg_version = @major AND p.deleted_at IS NULL AND p.status <> 'deleted'
ORDER BY o.name, p.name;

-- name: InsertPgVersionNotice :execrows
-- tenant: system - the version lifecycle's notices, once per organisation and step.
INSERT INTO pg_version_notices (major, org_id, days_before) VALUES (@major, @org_id, @days_before)
ON CONFLICT DO NOTHING;
