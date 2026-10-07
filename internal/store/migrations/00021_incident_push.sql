-- +goose Up
-- V3 M17: incidents are pushed to pgdock-status in the background; a
-- change clears pushed_at, and a failed push leaves its error here.
ALTER TABLE incidents ADD COLUMN push_error text;
ALTER TABLE incidents ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE incidents DROP COLUMN updated_at;
ALTER TABLE incidents DROP COLUMN push_error;
