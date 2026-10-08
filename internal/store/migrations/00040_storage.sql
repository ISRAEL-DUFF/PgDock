-- +goose Up
-- V4-M33: backend services' storage (V4 §5). Buckets and objects' metadata
-- live in each project's database (pgd_storage, schema version 4); the
-- bytes in its region's object store. pgdock-edge enforces what the
-- control plane works out here and sends in its feed.

-- What the edge applies, recomputed by the storage sweep: the bytes the
-- project may hold (its organisation's file quota less its other projects'
-- files; null is unlimited), the largest upload, and whether downloads
-- (the month's egress cap) or image transforms (the month's quota) are
-- stopped. A change moves the project in the edge feed.
ALTER TABLE project_services
  ADD COLUMN storage_quota_bytes    bigint,
  ADD COLUMN upload_max_bytes       bigint,
  ADD COLUMN storage_egress_blocked boolean NOT NULL DEFAULT false,
  ADD COLUMN transforms_blocked     boolean NOT NULL DEFAULT false;

-- What the sweep measured and the nightly reconciler found; not in the feed.
CREATE TABLE project_storage (
  project_id      uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  bytes           bigint NOT NULL DEFAULT 0,
  objects         bigint NOT NULL DEFAULT 0,
  measured_at     timestamptz,
  -- Rows whose bytes are missing, and a few of their paths.
  missing_objects int NOT NULL DEFAULT 0,
  missing_sample  text[] NOT NULL DEFAULT '{}',
  orphans_removed bigint NOT NULL DEFAULT 0,
  reconciled_at   timestamptz
);

-- Files left to delete after a project goes, by region and key prefix: the
-- sweep removes them (V4 §5.6).
CREATE TABLE storage_cleanups (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id uuid NOT NULL,
  region     text NOT NULL,
  prefix     text NOT NULL,
  residency  boolean NOT NULL DEFAULT false,
  not_before timestamptz NOT NULL DEFAULT now(),
  attempts   int NOT NULL DEFAULT 0,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- The plans' storage limits (V4 §10.1): the free plan's files, monthly
-- download egress, image transforms and largest upload; paid plans are
-- metered past their inclusions rather than stopped.
UPDATE quota_plans SET limits = limits || '{"file_storage_mb": 1024, "storage_egress_mb_per_month": 5120,
  "image_transforms_per_month": 500, "upload_max_mb": 50}' WHERE name = 'Personal';
UPDATE quota_plans SET limits = limits || '{"upload_max_mb": 5120}' WHERE name IN ('Pro', 'Team');

-- +goose Down
UPDATE quota_plans SET limits = limits - 'file_storage_mb' - 'storage_egress_mb_per_month' - 'image_transforms_per_month' - 'upload_max_mb';
DROP TABLE storage_cleanups;
DROP TABLE project_storage;
ALTER TABLE project_services DROP COLUMN storage_quota_bytes, DROP COLUMN upload_max_bytes,
  DROP COLUMN storage_egress_blocked, DROP COLUMN transforms_blocked;
