-- +goose Up
-- V3.1-M2: one etcd cluster per region (V3.1 §3). Existing members become
-- their nodes' region's cluster; each instance under Patroni records which
-- region's cluster holds its state.
ALTER TABLE etcd_members ADD COLUMN region text REFERENCES regions(id);
UPDATE etcd_members e SET region = n.region FROM nodes n WHERE n.id = e.node_id;
ALTER TABLE etcd_members ALTER COLUMN region SET NOT NULL;
CREATE INDEX etcd_members_region ON etcd_members (region);

ALTER TABLE instances ADD COLUMN etcd_region text REFERENCES regions(id);
UPDATE instances SET etcd_region = (SELECT e.region FROM etcd_members e ORDER BY e.created_at LIMIT 1)
WHERE patroni AND deleted_at IS NULL;

-- +goose Down
ALTER TABLE instances DROP COLUMN etcd_region;
DROP INDEX etcd_members_region;
ALTER TABLE etcd_members DROP COLUMN region;
