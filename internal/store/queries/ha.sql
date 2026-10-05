-- name: ListEtcdMembers :many
-- tenant: system - platform infrastructure (the etcd cluster).
SELECT e.*, n.name AS node_name FROM etcd_members e JOIN nodes n ON n.id = e.node_id ORDER BY e.created_at, e.name;

-- name: InsertEtcdMember :exec
-- tenant: system - platform infrastructure (the etcd cluster).
INSERT INTO etcd_members (node_id, name, client_url, peer_url) VALUES (@node_id, @name, @client_url, @peer_url);

-- name: SetEtcdMemberStatus :exec
-- tenant: system - platform infrastructure (the etcd cluster).
UPDATE etcd_members SET status = @status, error = sqlc.narg(error), checked_at = now() WHERE node_id = @node_id;

-- name: DeleteEtcdMembers :exec
-- tenant: system - platform infrastructure (the etcd cluster).
DELETE FROM etcd_members;

-- name: InsertInstanceMember :one
-- tenant: system - HA members of an instance the caller already resolved.
INSERT INTO instance_members (id, instance_id, node_id, role) VALUES (@id, @instance_id, @node_id, @role)
ON CONFLICT (id) DO UPDATE SET deleted_at = NULL, node_id = EXCLUDED.node_id, role = EXCLUDED.role, updated_at = now()
RETURNING *;

-- name: ListInstanceMembers :many
-- tenant: system - HA members of an instance the caller already resolved.
SELECT m.*, n.name AS node_name, n.private_addr AS node_addr
FROM instance_members m JOIN nodes n ON n.id = m.node_id
WHERE m.instance_id = @instance_id AND m.deleted_at IS NULL
ORDER BY m.created_at, m.id;

-- name: SetMemberAddress :exec
-- tenant: system - HA members of an instance the caller already resolved.
UPDATE instance_members SET host = sqlc.narg(host), port = sqlc.narg(port), rest_host = sqlc.narg(rest_host),
  rest_port = sqlc.narg(rest_port), admin_host = sqlc.narg(admin_host), admin_port = sqlc.narg(admin_port), updated_at = now()
WHERE id = @id;

-- name: SetMemberState :exec
-- tenant: system - HA members of an instance the caller already resolved.
UPDATE instance_members SET role = @role, state = sqlc.narg(state), lag_bytes = sqlc.narg(lag_bytes),
  timeline = sqlc.narg(timeline), error = sqlc.narg(error), updated_at = now()
WHERE id = @id;

-- name: DeleteInstanceMember :exec
-- tenant: system - HA members of an instance the caller already resolved.
UPDATE instance_members SET deleted_at = now(), role = 'stopped' WHERE id = @id;

-- name: SetInstancePatroni :exec
-- tenant: system - HA settings of an instance the caller already resolved.
UPDATE instances SET patroni = @patroni, ha_enabled = @ha_enabled, sync_replication = @sync_replication,
  leader_member = sqlc.narg(leader_member), patroni_secret = sqlc.narg(patroni_secret)
WHERE id = @id;

-- name: SetInstanceHAEnabled :exec
-- tenant: system - HA settings of an instance the caller already resolved.
UPDATE instances SET ha_enabled = @ha_enabled WHERE id = @id;

-- name: SetInstanceSync :exec
-- tenant: system - HA settings of an instance the caller already resolved.
UPDATE instances SET sync_replication = @sync_replication WHERE id = @id;

-- SetInstanceLeader points the instance at its new leader: the node, the
-- member (the agent's container key), and the addresses.
-- name: SetInstanceLeader :exec
-- tenant: system - HA failover of an instance the caller already resolved.
UPDATE instances SET node_id = @node_id, leader_member = @leader_member, host = sqlc.narg(host), port = @port,
  admin_host = sqlc.narg(admin_host), admin_port = sqlc.narg(admin_port)
WHERE id = @id;

-- name: ListPatroniInstances :many
-- tenant: system - the HA leader watcher.
SELECT * FROM instances WHERE patroni AND deleted_at IS NULL ORDER BY created_at;

-- name: InsertFailoverEvent :one
-- tenant: system - the HA leader watcher.
INSERT INTO failover_events (instance_id, from_member, to_member, from_node, to_node, kind, duration_ms)
VALUES (@instance_id, sqlc.narg(from_member), sqlc.narg(to_member), sqlc.narg(from_node), sqlc.narg(to_node), @kind, sqlc.narg(duration_ms))
RETURNING *;

-- name: ListFailoverEvents :many
-- tenant: system - an instance the request already authorized.
SELECT f.*, fn.name AS from_node_name, tn.name AS to_node_name
FROM failover_events f LEFT JOIN nodes fn ON fn.id = f.from_node LEFT JOIN nodes tn ON tn.id = f.to_node
WHERE f.instance_id = @instance_id ORDER BY f.occurred_at DESC LIMIT @lim;

-- name: ProjectOnInstance :one
-- tenant: system - the HA leader watcher (one project per dedicated instance).
SELECT * FROM projects WHERE instance_id = @instance_id AND deleted_at IS NULL ORDER BY created_at LIMIT 1;

-- name: AvailabilitySummary :one
-- tenant: system - a project the request already authorized.
SELECT count(*) FILTER (WHERE NOT excluded)::int AS measured,
       count(*) FILTER (WHERE NOT available AND NOT excluded)::int AS unavailable
FROM availability_minutes WHERE project_id = @project_id AND minute >= @from_ts AND minute < @to_ts;

-- name: RecentOutageMinutes :many
-- tenant: system - a project the request already authorized.
SELECT * FROM availability_minutes
WHERE project_id = @project_id AND NOT available AND NOT excluded AND minute >= @since
ORDER BY minute DESC LIMIT @lim;
