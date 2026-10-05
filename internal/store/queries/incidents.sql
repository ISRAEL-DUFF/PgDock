-- Incidents (V3 §2.6): the platform admin's, pushed to pgdock-status.

-- name: InsertIncident :one
INSERT INTO incidents (title, components, region_id, severity, status, created_by)
VALUES (@title, @components, @region_id, @severity, @status, @created_by)
RETURNING *;

-- name: GetIncident :one
SELECT * FROM incidents WHERE id = @id;

-- name: ListIncidents :many
SELECT * FROM incidents ORDER BY (resolved_at IS NULL) DESC, started_at DESC LIMIT @lim;

-- name: UpdateIncident :one
-- A change clears pushed_at so the pusher sends it again.
UPDATE incidents SET title = @title, components = @components, region_id = @region_id, severity = @severity,
  status = @status, resolved_at = @resolved_at, pushed_at = NULL, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: InsertIncidentUpdate :one
INSERT INTO incident_updates (incident_id, status, body, posted_by)
VALUES (@incident_id, @status, @body, @posted_by)
RETURNING *;

-- name: IncidentUpdates :many
SELECT u.*, users.email AS posted_by_email
FROM incident_updates u LEFT JOIN users ON users.id = u.posted_by
WHERE u.incident_id = @incident_id
ORDER BY u.posted_at, u.id;

-- name: UnpushedIncidents :many
-- Changed since the last push (or never pushed), oldest change first.
SELECT * FROM incidents WHERE pushed_at IS NULL ORDER BY updated_at LIMIT 20;

-- name: MarkIncidentPushed :exec
-- Only if nothing changed while the push was in flight.
UPDATE incidents SET pushed_at = now(), push_error = NULL WHERE id = @id AND updated_at = @updated_at;

-- name: MarkIncidentPushFailed :exec
UPDATE incidents SET push_error = @push_error WHERE id = @id;

-- name: OverdueJobs :one
-- tenant: system - the status heartbeat counts platform-wide scheduler lag, not any organisation's data.
-- The same jobs DueJobs picks: a deleted project's jobs never run.
SELECT count(*) FROM scheduled_jobs j JOIN projects p ON p.id = j.project_id
WHERE j.enabled AND j.next_run_at < now() - interval '5 minutes' AND p.deleted_at IS NULL;

-- name: DedicatedNodeHealth :one
-- tenant: system - the status heartbeat counts unreachable nodes from the platform's own alerts.
-- How many dedicated-capable nodes there are and how many are unreachable
-- (a firing node_unreachable alert), for the status heartbeat.
SELECT count(*) AS total,
  count(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM alerts a WHERE a.status = 'firing' AND a.kind = 'node_unreachable' AND a.target_id = n.id::text
  )) AS unreachable
FROM nodes n WHERE n.role IN ('dedicated', 'both') AND n.status <> 'removed';
