-- +goose Up
-- Auth: phone, OAuth, MFA and hooks (V4 §4, M32). Auth emails' queue
-- carries SMS and WhatsApp codes too; webhook hooks get a queue of their
-- own; fraud alerts go out once a day per project and kind.

ALTER TABLE auth_email_outbox RENAME TO auth_message_outbox;
ALTER INDEX auth_email_outbox_pkey RENAME TO auth_message_outbox_pkey;
ALTER INDEX auth_email_outbox_due RENAME TO auth_message_outbox_due;
ALTER INDEX auth_email_outbox_project RENAME TO auth_message_outbox_project;
ALTER TABLE auth_message_outbox
  ADD COLUMN channel text NOT NULL DEFAULT 'email' CHECK (channel IN ('email', 'sms', 'whatsapp')),
  -- A hash of the recipient (for per-number limits) and its country.
  ADD COLUMN recipient_hash text,
  ADD COLUMN country text;
CREATE INDEX auth_message_outbox_recipient ON auth_message_outbox (project_id, recipient_hash, created_at)
  WHERE recipient_hash IS NOT NULL;
CREATE INDEX auth_message_outbox_phone ON auth_message_outbox (project_id, created_at) WHERE channel <> 'email';

-- After sign-up and after sign-in hooks (V4 §4.7), delivered with
-- retries through the outbound client.
CREATE TABLE auth_hook_outbox (
  id              uuid PRIMARY KEY,
  project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  event           text NOT NULL,
  payload         jsonb NOT NULL,
  attempts        int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error      text,
  last_status     int,
  created_at      timestamptz NOT NULL DEFAULT now(),
  delivered_at    timestamptz,
  failed_at       timestamptz
);
CREATE INDEX auth_hook_outbox_due ON auth_hook_outbox (next_attempt_at) WHERE delivered_at IS NULL AND failed_at IS NULL;
CREATE INDEX auth_hook_outbox_project ON auth_hook_outbox (project_id, created_at DESC);

-- Fraud alerts sent (V4 §10.2): at most one a day per project and kind.
CREATE TABLE auth_alerts (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind       text NOT NULL,
  day        date NOT NULL,
  details    jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, kind, day)
);

-- +goose Down
DROP TABLE auth_alerts;
DROP TABLE auth_hook_outbox;
DROP INDEX auth_message_outbox_phone;
DROP INDEX auth_message_outbox_recipient;
ALTER TABLE auth_message_outbox DROP COLUMN country, DROP COLUMN recipient_hash, DROP COLUMN channel;
ALTER INDEX auth_message_outbox_project RENAME TO auth_email_outbox_project;
ALTER INDEX auth_message_outbox_due RENAME TO auth_email_outbox_due;
ALTER INDEX auth_message_outbox_pkey RENAME TO auth_email_outbox_pkey;
ALTER TABLE auth_message_outbox RENAME TO auth_email_outbox;
