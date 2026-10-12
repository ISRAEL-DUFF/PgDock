-- Incidents (V3 §2.6): the platform admin's, pushed to pgdock-status.

-- name: InsertIncident :one
INSERT INTO incidents (title, components, region_id, severity, status, created_by)
VALUES (@title, @components, @region_id, @severity, @status, @created_by)
RETURNING *;

-- name: GetIncident :one
SELECT * FROM incidents WHERE id = @id;

-- name: ListIncidents :many
SELECT * FROM incidents WHERE NOT (severity = 'maintenance' AND announced_at IS NULL)
ORDER BY (resolved_at IS NULL) DESC, started_at DESC LIMIT @lim;

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
-- Maintenance drafts (never announced) stay off the status page.
SELECT * FROM incidents WHERE pushed_at IS NULL AND NOT (severity = 'maintenance' AND announced_at IS NULL)
ORDER BY updated_at LIMIT 20;

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

-- ---- Announced maintenance (V3.1 4) -----------------------------------------

-- name: InsertMaintenance :one
-- tenant: system - platform maintenance announcements.
INSERT INTO incidents (title, components, region_id, severity, status, created_by, started_at, scheduled_start, scheduled_end, announced_at, replaces)
VALUES (@title, @components, sqlc.narg(region_id), 'maintenance', 'identified', sqlc.narg(created_by), @scheduled_start, @scheduled_start, @scheduled_end, now(), sqlc.narg(replaces))
RETURNING *;

-- name: InsertIncidentScope :exec
-- tenant: system - what a maintenance announcement covers.
INSERT INTO incident_scope (incident_id, project_id, node_id) VALUES (@incident_id, sqlc.narg(project_id), sqlc.narg(node_id));

-- name: IncidentScope :many
-- tenant: system - what a maintenance announcement covers.
SELECT * FROM incident_scope WHERE incident_id = @incident_id;

-- name: CancelMaintenance :one
-- tenant: system - a maintenance announcement called off.
UPDATE incidents SET cancelled_at = now(), status = 'resolved', resolved_at = now(), updated_at = now()
WHERE id = @id AND severity = 'maintenance' AND announced_at IS NOT NULL AND cancelled_at IS NULL AND resolved_at IS NULL
RETURNING *;

-- name: MaintenanceDone :many
-- tenant: system - announced maintenance whose window has ended.
UPDATE incidents SET status = 'resolved', resolved_at = scheduled_end, updated_at = now()
WHERE severity = 'maintenance' AND announced_at IS NOT NULL AND resolved_at IS NULL AND scheduled_end <= @at::timestamptz
RETURNING *;

-- name: ListMaintenance :many
-- tenant: system - maintenance announcements, newest window first.
SELECT * FROM incidents WHERE (announced_at IS NOT NULL OR proposed_for IS NOT NULL) AND scheduled_end >= @since::timestamptz
ORDER BY scheduled_start DESC LIMIT @lim;

-- name: MaintenanceOrgEmails :many
-- tenant: system - who is told of an announcement: owners and admins of the organisations with live projects it covers.
SELECT DISTINCT u.email FROM users u
JOIN org_members m ON m.user_id = u.id AND m.role IN ('owner', 'admin')
JOIN projects p ON p.org_id = m.org_id AND p.deleted_at IS NULL
JOIN incidents i ON i.id = @incident_id
WHERE u.disabled_at IS NULL AND maintenance_covers(i, p)
ORDER BY u.email;

-- name: AnnouncedMaintenanceFor :one
-- tenant: system - the announcement, made 72 hours ahead, covering a project at a moment (the maintenance window's gate).
SELECT i.id FROM incidents i, projects p, (SELECT sqlc.arg(at)::timestamptz AS at) w
WHERE p.id = sqlc.arg(project_id) AND i.severity = 'maintenance' AND i.announced_at IS NOT NULL AND i.cancelled_at IS NULL
  AND w.at >= i.scheduled_start AND w.at < i.scheduled_end
  AND i.announced_at <= w.at - interval '72 hours' AND maintenance_covers(i, p)
ORDER BY i.announced_at LIMIT 1;

-- name: InsertMaintenanceDraft :one
-- tenant: system - maintenance PGDock proposes; not announced until confirmed.
INSERT INTO incidents (title, components, region_id, severity, status, started_at, scheduled_start, scheduled_end, proposed_for)
VALUES (@title, @components, NULL, 'maintenance', 'draft', @scheduled_start, @scheduled_start, @scheduled_end, @proposed_for)
RETURNING *;

-- name: OpenDraftFor :one
-- tenant: system - the open draft proposed for a window and a reason, to add projects to.
SELECT * FROM incidents WHERE status = 'draft' AND proposed_for = @proposed_for AND scheduled_start = @scheduled_start
ORDER BY started_at, id LIMIT 1;

-- name: MaintenancePlannedFor :one
-- tenant: system - whether a project already has maintenance ahead (announced, or a draft), or had a draft for that window discarded.
SELECT EXISTS (
  SELECT 1 FROM incidents i, projects p
  WHERE p.id = @project_id AND i.severity = 'maintenance' AND i.scheduled_end > @now::timestamptz
    AND (i.announced_at IS NOT NULL OR i.status = 'draft' OR i.proposed_for IS NOT NULL)
    AND (i.cancelled_at IS NULL OR (i.announced_at IS NULL AND i.scheduled_start = @window_start::timestamptz))
    AND maintenance_covers(i, p))::bool;

-- name: ConfirmDraft :one
-- tenant: system - a draft announced: from now its notice counts.
UPDATE incidents SET status = 'identified', announced_at = now(), pushed_at = NULL, updated_at = now()
WHERE id = @id AND status = 'draft' AND scheduled_start > now()
RETURNING *;

-- name: DiscardDraft :one
-- tenant: system - a draft thrown away; the work it was for keeps waiting.
UPDATE incidents SET status = 'resolved', resolved_at = now(), cancelled_at = now(), updated_at = now()
WHERE id = @id AND status = 'draft'
RETURNING *;

-- name: ExpireDrafts :execrows
-- tenant: system - drafts whose window started unconfirmed.
UPDATE incidents SET status = 'resolved', resolved_at = now(), cancelled_at = now(), updated_at = now()
WHERE status = 'draft' AND scheduled_start <= @at::timestamptz;

-- name: MaintenanceRecipients :many
-- tenant: system - who an announcement of this scope would reach: owners and admins of organisations with live projects it covers (as maintenance_covers).
SELECT DISTINCT m.org_id, u.email::text AS email FROM users u
JOIN org_members m ON m.user_id = u.id AND m.role IN ('owner', 'admin')
JOIN projects p ON p.org_id = m.org_id AND p.deleted_at IS NULL
WHERE u.disabled_at IS NULL AND (
  CASE WHEN cardinality(@project_ids::uuid[]) + cardinality(@node_ids::uuid[]) > 0 THEN
    p.id = ANY(@project_ids::uuid[])
    OR EXISTS (SELECT 1 FROM instance_members im WHERE im.instance_id = p.instance_id AND im.deleted_at IS NULL AND im.node_id = ANY(@node_ids::uuid[]))
    OR (SELECT n.node_id FROM instances n WHERE n.id = p.instance_id) = ANY(@node_ids::uuid[])
  ELSE sqlc.narg(region)::text IS NULL OR p.region = sqlc.narg(region)::text END)
ORDER BY email;
