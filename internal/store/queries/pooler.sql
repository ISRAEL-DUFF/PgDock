-- name: PoolerHosts :many
-- Registered pooler hosts (V3 §2.1), in a stable order.
SELECT * FROM nodes
WHERE role = 'pooler' AND status <> 'removed' AND agent_cert_fp IS NOT NULL
ORDER BY name;

-- name: RecordPoolerHostState :exec
UPDATE nodes SET pooler_generation = @pooler_generation, pooler_hash = @pooler_hash,
  pooler_vrrp_state = @pooler_vrrp_state, pooler_ready = @pooler_ready, pooler_checked_at = now()
WHERE id = @id;

-- name: SetProviderServerID :exec
UPDATE nodes SET provider_server_id = @provider_server_id WHERE id = @id;

-- name: GetPoolerGeneration :one
SELECT * FROM pooler_generations WHERE region = @region;

-- name: AdvancePoolerGeneration :one
-- Records a region's rendered configuration's hash, moving to the next
-- generation only when the content changed.
INSERT INTO pooler_generations (region, generation, hash) VALUES (@region, 1, @hash)
ON CONFLICT (region) DO UPDATE
  SET generation = pooler_generations.generation + CASE WHEN pooler_generations.hash = EXCLUDED.hash THEN 0 ELSE 1 END,
      hash = EXCLUDED.hash,
      updated_at = CASE WHEN pooler_generations.hash = EXCLUDED.hash THEN pooler_generations.updated_at ELSE now() END
RETURNING *;

-- name: InsertPoolerEvent :exec
INSERT INTO pooler_events (node_id, kind, detail) VALUES (@node_id, @kind, @detail);

-- name: ListPoolerEvents :many
SELECT e.*, n.name AS node_name FROM pooler_events e
LEFT JOIN nodes n ON n.id = e.node_id
ORDER BY e.occurred_at DESC LIMIT @lim;
