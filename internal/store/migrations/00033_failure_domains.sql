-- +goose Up
-- V3.1-M1: failure domains (V3.1 §2). A node's failure domain names what
-- fails with it (a rack, a host, a power feed); a Hetzner node also
-- records its spread placement group. HA pairs, a region's etcd members
-- and its pooler hosts must sit in different domains.
ALTER TABLE nodes
  ADD COLUMN failure_domain  text CHECK (failure_domain IS NULL OR failure_domain ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,62}$'),
  ADD COLUMN placement_group text;

-- +goose Down
ALTER TABLE nodes DROP COLUMN failure_domain, DROP COLUMN placement_group;
