-- Adjustments for shared-tier provisioning (M1). See the decisions log in
-- docs/decisions.md.

-- +goose Up
-- Hosts may be DNS names (e.g. a Compose service), not only IPs.
ALTER TABLE nodes ALTER COLUMN private_addr TYPE text USING host(private_addr);
-- Nodes exist before an agent registers (agents arrive in M4).
ALTER TABLE nodes ALTER COLUMN agent_cert_fp DROP NOT NULL;

-- The control plane's address for an instance when it differs from the one
-- the pooler uses (nodes.private_addr:instances.port), e.g. a published port.
ALTER TABLE instances ADD COLUMN admin_host text, ADD COLUMN admin_port int;
CREATE UNIQUE INDEX instances_one_shared_per_node ON instances (node_id) WHERE kind = 'shared';

-- Storage targets are configured with backups (M3).
ALTER TABLE projects ALTER COLUMN storage_target_id DROP NOT NULL;
ALTER TABLE projects ADD CONSTRAINT projects_status_check CHECK (status IN
  ('provisioning','active','promoting','restoring','deleting','deleted','error'));
CREATE INDEX projects_live ON projects (created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX operations_project ON operations (project_id, created_at DESC);

-- +goose Down
DROP INDEX operations_project;
DROP INDEX projects_live;
ALTER TABLE projects DROP CONSTRAINT projects_status_check;
ALTER TABLE projects ALTER COLUMN storage_target_id SET NOT NULL;
DROP INDEX instances_one_shared_per_node;
ALTER TABLE instances DROP COLUMN admin_host, DROP COLUMN admin_port;
ALTER TABLE nodes ALTER COLUMN agent_cert_fp SET NOT NULL;
ALTER TABLE nodes ALTER COLUMN private_addr TYPE inet USING private_addr::inet;
