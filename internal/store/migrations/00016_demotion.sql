-- +goose Up
-- V2 s5 (M14): demotion moves a dedicated project back to the shared tier.
-- Its stopped instance is kept for 48 hours as a rollback option
-- (retired_databases, reason 'demotion'), then destroyed; until then it
-- still counts toward the organisation's dedicated allowance.
ALTER TABLE projects DROP CONSTRAINT projects_status_check;
ALTER TABLE projects ADD CONSTRAINT projects_status_check CHECK (status IN
  ('provisioning','active','promoting','demoting','restoring','deleting','deleted','error'));
CREATE INDEX retired_databases_instance ON retired_databases (instance_id) WHERE dropped_at IS NULL;

-- +goose Down
DROP INDEX retired_databases_instance;
UPDATE projects SET status = 'active' WHERE status = 'demoting';
ALTER TABLE projects DROP CONSTRAINT projects_status_check;
ALTER TABLE projects ADD CONSTRAINT projects_status_check CHECK (status IN
  ('provisioning','active','promoting','restoring','deleting','deleted','error'));
