-- Dedicated instances and multi-node placement (M4).

-- +goose Up
-- host/port: where poolers and agents reach the instance (a container name
-- on the node's Docker network, or a published address); admin_host/_port
-- stay the control plane's view. Each instance has its own superuser
-- (admin_secret, sealed like nodes.pg_admin_secret) and WAL-G location.
ALTER TABLE instances
  ADD COLUMN host         text,
  ADD COLUMN admin_secret bytea,
  ADD COLUMN profile      text,
  ADD COLUMN walg_prefix  text,
  ADD COLUMN error        text,
  ADD COLUMN deleted_at   timestamptz;
ALTER TABLE instances ADD CONSTRAINT instances_status_check
  CHECK (status IN ('provisioning','running','stopped','error','deleted'));
CREATE INDEX instances_node ON instances (node_id) WHERE deleted_at IS NULL;
-- One live shared cluster per node; removed ones do not count.
DROP INDEX instances_one_shared_per_node;
CREATE UNIQUE INDEX instances_one_shared_per_node ON instances (node_id) WHERE kind = 'shared' AND deleted_at IS NULL;

-- +goose Down
DROP INDEX instances_one_shared_per_node;
CREATE UNIQUE INDEX instances_one_shared_per_node ON instances (node_id) WHERE kind = 'shared';
DROP INDEX instances_node;
ALTER TABLE instances DROP CONSTRAINT instances_status_check;
ALTER TABLE instances DROP COLUMN host, DROP COLUMN admin_secret, DROP COLUMN profile,
  DROP COLUMN walg_prefix, DROP COLUMN error, DROP COLUMN deleted_at;
