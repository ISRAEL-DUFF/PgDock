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
