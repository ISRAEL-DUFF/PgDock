-- name: GetDefaultStorageTarget :one
SELECT * FROM storage_targets WHERE is_default;

-- name: InsertStorageTarget :one
INSERT INTO storage_targets (id, name, endpoint, bucket, prefix, credentials, is_default)
VALUES (@id, @name, @endpoint, @bucket, @prefix, @credentials, true)
RETURNING *;

-- name: UpdateStorageTarget :one
UPDATE storage_targets
SET endpoint = @endpoint, bucket = @bucket, prefix = @prefix, credentials = @credentials
WHERE id = @id
RETURNING *;

-- name: InsertBackup :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
INSERT INTO backups (project_id, kind, object_key, started_at, status, storage_target_id, operation_id, key_wrapped, expires_at)
VALUES (sqlc.narg(project_id), @kind, @object_key, now(), 'running', @storage_target_id, sqlc.narg(operation_id), @key_wrapped, sqlc.narg(expires_at))
RETURNING *;

-- name: FinishBackup :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
UPDATE backups SET status = 'succeeded', size_bytes = @size_bytes, checksum = @checksum, finished_at = now()
WHERE id = @id
RETURNING *;

-- name: FailBackup :exec
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
UPDATE backups SET status = 'failed', error = @error::text, finished_at = now() WHERE id = @id;

-- name: MarkBackupDeleted :exec
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
UPDATE backups SET status = 'deleted', deleted_at = now() WHERE id = @id;

-- name: GetBackup :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM backups WHERE id = @id;

-- name: ListProjectBackups :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM backups
WHERE project_id = @project_id AND status <> 'deleted'
ORDER BY started_at DESC
LIMIT @max_rows;

-- name: LatestSucceededBackup :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
-- The newest logical backup (base backups restore only by PITR).
SELECT * FROM backups
WHERE project_id = @project_id AND status = 'succeeded' AND kind IN ('logical', 'final', 'safety')
ORDER BY finished_at DESC
LIMIT 1;

-- RetentionCandidates lists the succeeded backups of one kind for a project
-- (or metadata backups when project_id is null), newest first.
-- name: RetentionCandidates :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM backups
WHERE kind = @kind AND status = 'succeeded'
  AND ((sqlc.narg(project_id)::uuid IS NULL AND project_id IS NULL) OR project_id = sqlc.narg(project_id))
ORDER BY finished_at DESC;

-- name: ExpiredBackups :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM backups
WHERE status = 'succeeded' AND expires_at IS NOT NULL AND expires_at < now();

-- name: StaleRunningBackups :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM backups WHERE status = 'running' AND started_at < @before;

-- name: LastBackupTimes :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT project_id, max(finished_at)::timestamptz AS last_backup_at
FROM backups
WHERE status = 'succeeded' AND project_id IS NOT NULL AND kind IN ('logical','base','final','safety')
GROUP BY project_id;

-- name: ProjectsDueForBackup :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
-- Active projects created before the window that opened at @since, with no
-- backup operation since then and no operation in flight (the next tick
-- picks them up). A project created after the window opened waits for the
-- next one.
SELECT p.* FROM projects p
WHERE p.deleted_at IS NULL AND p.status = 'active' AND p.created_at < @since
  AND NOT EXISTS (
    SELECT 1 FROM operations o
    WHERE o.project_id = p.id AND o.kind IN ('backup', 'base_backup') AND o.created_at >= @since
  )
  AND NOT EXISTS (
    SELECT 1 FROM operations o
    WHERE o.project_id = p.id AND o.status IN ('queued', 'running')
  );

-- name: LastOperationOfKind :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM operations WHERE kind = @kind ORDER BY created_at DESC LIMIT 1;

-- name: RandomProjectWithBackup :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT p.* FROM projects p
WHERE p.deleted_at IS NULL AND p.status = 'active'
  AND EXISTS (SELECT 1 FROM backups b WHERE b.project_id = p.id AND b.status = 'succeeded' AND b.kind = 'logical')
ORDER BY random()
LIMIT 1;

-- name: BackupForOperation :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM backups
WHERE operation_id = @operation_id AND kind = @kind AND status = 'succeeded'
ORDER BY finished_at DESC
LIMIT 1;

-- name: ListAllBackups :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
-- Backups of live and deleted projects (final backups outlive their project).
SELECT b.*, p.name AS project_name, (p.deleted_at IS NOT NULL)::bool AS project_deleted
FROM backups b
LEFT JOIN projects p ON p.id = b.project_id
WHERE b.status <> 'deleted' AND (sqlc.narg(kind)::text IS NULL OR b.kind = sqlc.narg(kind))
ORDER BY b.started_at DESC
LIMIT @max_rows;

-- name: ListRestoreTests :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM operations WHERE kind = 'restore_test' ORDER BY created_at DESC LIMIT @max_rows;

-- name: InsertBaseBackup :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
INSERT INTO backups (project_id, kind, object_key, started_at, finished_at, status, size_bytes, storage_target_id, operation_id)
VALUES (@project_id, 'base', @object_key, @started_at, @finished_at, 'succeeded', @size_bytes, @storage_target_id, sqlc.narg(operation_id))
RETURNING *;

-- name: ListBaseBackups :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM backups WHERE project_id = @project_id AND kind = 'base' AND status = 'succeeded' ORDER BY finished_at;

-- BaseBackupBefore is the newest base backup of a project finished at or
-- before a time (the starting point of a point-in-time recovery).
-- name: BaseBackupBefore :one
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
SELECT * FROM backups
WHERE project_id = @project_id AND kind = 'base' AND status = 'succeeded' AND finished_at <= @before
ORDER BY finished_at DESC
LIMIT 1;

-- name: FailInterruptedBackups :execrows
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
-- Backups an earlier attempt of the operation left running (its worker or
-- agent died mid-dump): they never finished.
UPDATE backups SET status = 'failed', error = 'interrupted: the attempt that took it did not finish', finished_at = now()
WHERE operation_id = @operation_id AND status = 'running';

-- name: SweepStaleBackups :execrows
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
-- Running backups whose operation has finished (or, without one, that
-- started over a day ago) were interrupted.
UPDATE backups b SET status = 'failed', error = 'interrupted: the operation ended before it finished', finished_at = now()
WHERE b.status = 'running' AND (
  EXISTS (SELECT 1 FROM operations o WHERE o.id = b.operation_id AND o.status IN ('succeeded', 'failed'))
  OR (b.operation_id IS NULL AND b.started_at < now() - interval '1 day'));

-- name: FailedBackupObjects :many
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
-- Failed backups whose object may still be in storage: an upload can
-- complete at the bucket after its attempt gave up on it.
SELECT * FROM backups
WHERE status = 'failed' AND deleted_at IS NULL AND storage_target_id = @storage_target_id
ORDER BY started_at LIMIT 100;

-- name: MarkFailedBackupCleaned :exec
-- tenant: system - backup workers and the scheduler, or a project the request already authorized.
UPDATE backups SET deleted_at = now() WHERE id = @id AND status = 'failed';
