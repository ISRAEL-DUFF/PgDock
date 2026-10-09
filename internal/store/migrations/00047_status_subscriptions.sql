-- +goose Up
-- V4.1-M6: status page emails for paying organisations' contacts, and
-- the edges' liveness for the backend services component (V4.1 §7).

-- A billing contact gets status page emails unless turned off (Org →
-- Billing → Notifications); pgdock-server sends the list to pgdock-status
-- hourly.
ALTER TABLE billing_contacts ADD COLUMN status_emails boolean NOT NULL DEFAULT true;

-- Every pgdock-edge reports at least every 30 seconds, empty or not; one
-- that stops is what the backend services component shows. Edges silent
-- for a day are taken as gone.
CREATE TABLE edges (
  name           text PRIMARY KEY,
  region         text NOT NULL DEFAULT '',
  last_report_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE edges;
ALTER TABLE billing_contacts DROP COLUMN status_emails;
