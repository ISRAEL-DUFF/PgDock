-- name: ListNodes :many
SELECT * FROM nodes ORDER BY created_at;

-- name: GetNode :one
SELECT * FROM nodes WHERE id = @id;

-- name: GetNodeByName :one
SELECT * FROM nodes WHERE name = @name;

-- name: InsertNode :one
INSERT INTO nodes (name, private_addr, role, capacity, registration_token, registration_expires_at)
VALUES (@name, @private_addr, @role, '{}', @registration_token, @registration_expires_at)
RETURNING *;

-- name: SetRegistrationToken :exec
UPDATE nodes SET registration_token = @registration_token, registration_expires_at = @registration_expires_at
WHERE id = @id;

-- name: GetNodeByRegistrationToken :one
SELECT * FROM nodes
WHERE registration_token = @registration_token AND registration_expires_at > now()
FOR UPDATE;

-- name: CompleteNodeRegistration :one
UPDATE nodes
SET agent_cert_fp = @agent_cert_fp, agent_host = @agent_host, agent_port = @agent_port,
    agent_version = @agent_version, registration_token = NULL, registration_expires_at = NULL,
    last_heartbeat = now(), status = 'healthy'
WHERE id = @id
RETURNING *;

-- name: RecordNodeHeartbeat :exec
UPDATE nodes SET last_heartbeat = now(), status = @status, capacity = @capacity, agent_version = @agent_version
WHERE id = @id;

-- name: SetNodeStatus :exec
UPDATE nodes SET status = @status WHERE id = @id;

-- name: NodeForInstance :one
SELECT n.* FROM nodes n JOIN instances i ON i.node_id = n.id WHERE i.id = @instance_id;

-- name: FirstAgentNode :one
SELECT * FROM nodes WHERE agent_cert_fp IS NOT NULL ORDER BY created_at LIMIT 1;

-- name: RemoveNode :exec
UPDATE nodes SET status = 'removed', agent_cert_fp = NULL, registration_token = NULL WHERE id = @id;

-- name: NodeLiveInstances :one
SELECT count(*)::int FROM instances WHERE node_id = @node_id AND deleted_at IS NULL;
