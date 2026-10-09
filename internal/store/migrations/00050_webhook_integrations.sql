-- +goose Up
-- Integrators' webhooks (Taskiem P1-G3): a description and string
-- metadata to tag the webhooks a tool created, and the previous signing
-- secret while a rotation overlaps (deliveries are signed with both).
ALTER TABLE webhooks
  ADD COLUMN description text NOT NULL DEFAULT '',
  ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}',
  ADD COLUMN previous_secret_enc bytea,
  ADD COLUMN previous_secret_expires_at timestamptz;

-- +goose Down
ALTER TABLE webhooks
  DROP COLUMN previous_secret_expires_at,
  DROP COLUMN previous_secret_enc,
  DROP COLUMN metadata,
  DROP COLUMN description;
