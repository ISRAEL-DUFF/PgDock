-- +goose Up
-- V3 M25: regions, data residency, and cross-region backup copies (V3 §6,
-- §2.5).

-- A region groups nodes, a pooler pair with its own hostname, a storage
-- target and a copy target (§6.1). pooler_host is the hostname in its
-- projects' connection strings; empty means the platform's database host.
-- residency: the region offers "data must stay in <country>" (§6.3).
CREATE TABLE regions (
  id                text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,30}$'),
  name              text NOT NULL,
  country           text NOT NULL DEFAULT '' CHECK (country = '' OR country ~ '^[A-Z]{2}$'),
  pooler_host       text NOT NULL DEFAULT '',
  provider          text NOT NULL DEFAULT 'manual',
  location          text NOT NULL DEFAULT '',
  storage_target_id uuid REFERENCES storage_targets(id),
  copy_target_id    uuid REFERENCES storage_targets(id),
  floating_ip_id    text,
  residency         boolean NOT NULL DEFAULT false,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'hidden')),
  created_at        timestamptz NOT NULL DEFAULT now()
);
INSERT INTO regions (id, name)
SELECT DISTINCT region, region FROM nodes
UNION SELECT 'eu-central', 'eu-central'
ON CONFLICT DO NOTHING;
ALTER TABLE nodes ADD CONSTRAINT nodes_region_fk FOREIGN KEY (region) REFERENCES regions(id);
ALTER TABLE capacity_proposals ADD CONSTRAINT capacity_proposals_region_fk FOREIGN KEY (region) REFERENCES regions(id);

-- A project's region is its instance's node's. forward_region and
-- forward_until: after a region move, the old region's poolers keep
-- routing its old hostname for 30 days (§6.1).
ALTER TABLE projects
  ADD COLUMN region         text REFERENCES regions(id),
  ADD COLUMN data_residency boolean NOT NULL DEFAULT false,
  ADD COLUMN forward_region text REFERENCES regions(id),
  ADD COLUMN forward_until  timestamptz;
UPDATE projects p SET region = n.region FROM instances i JOIN nodes n ON n.id = i.node_id WHERE i.id = p.instance_id;
UPDATE projects SET region = 'eu-central' WHERE region IS NULL;
ALTER TABLE projects ALTER COLUMN region SET NOT NULL;
CREATE INDEX projects_region ON projects (region) WHERE deleted_at IS NULL;

-- Where a storage target's bucket is: residency projects back up only to
-- targets in their region.
ALTER TABLE storage_targets ADD COLUMN pgdock_region text REFERENCES regions(id);

-- Each region's pooler hosts serve their own configuration generation.
CREATE TABLE pooler_generations (
  region     text PRIMARY KEY REFERENCES regions(id),
  generation bigint NOT NULL,
  hash       text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO pooler_generations (region, generation, hash, updated_at)
SELECT 'eu-central', generation, hash, updated_at FROM pooler_config WHERE id = 1
ON CONFLICT DO NOTHING;

-- Cross-region copies of backups on platform targets (§2.5).
ALTER TABLE backups
  ADD COLUMN copy_target_id uuid REFERENCES storage_targets(id),
  ADD COLUMN copy_status    text CHECK (copy_status IN ('pending', 'copied', 'failed', 'skipped')),
  ADD COLUMN copied_at      timestamptz,
  ADD COLUMN copy_error     text;
CREATE INDEX backups_copy_pending ON backups (started_at) WHERE copy_status = 'pending';

-- +goose Down
DROP INDEX backups_copy_pending;
ALTER TABLE backups DROP COLUMN copy_error, DROP COLUMN copied_at, DROP COLUMN copy_status, DROP COLUMN copy_target_id;
DROP TABLE pooler_generations;
ALTER TABLE storage_targets DROP COLUMN pgdock_region;
DROP INDEX projects_region;
ALTER TABLE projects DROP COLUMN forward_until, DROP COLUMN forward_region, DROP COLUMN data_residency, DROP COLUMN region;
ALTER TABLE capacity_proposals DROP CONSTRAINT capacity_proposals_region_fk;
ALTER TABLE nodes DROP CONSTRAINT nodes_region_fk;
DROP TABLE regions;
