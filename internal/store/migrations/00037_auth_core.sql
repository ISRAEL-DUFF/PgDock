-- +goose Up
-- Auth core (V4 §4, M31). Users live in each project's database
-- (pgd_auth, applied by the control plane); the platform keeps a project's
-- auth settings, the auth emails waiting to go out, what was sent, and
-- who was active each month.

CREATE TABLE project_auth_config (
  project_id    uuid PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  config        jsonb NOT NULL DEFAULT '{}',   -- methods, token lifetimes, redirects, password rules (no secrets)
  providers_enc bytea,                         -- the project's SMTP credentials (encrypted)
  templates     jsonb NOT NULL DEFAULT '{}',   -- per-kind subject and body overrides
  updated_at    timestamptz NOT NULL DEFAULT now()
);
-- pgdock-edge carries the settings: a change reaches it through the feed.
CREATE TRIGGER project_auth_config_touch AFTER INSERT OR UPDATE OR DELETE ON project_auth_config
  FOR EACH ROW EXECUTE FUNCTION project_services_touch();

-- Auth emails queued by pgdock-edge, sent by pgdock-server: through the
-- project's SMTP, or the platform's at a low hourly rate. The message
-- (with its code or link) is sealed and dropped once sent.
CREATE TABLE auth_email_outbox (
  id              uuid PRIMARY KEY,
  project_id      uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind            text NOT NULL,
  via             text NOT NULL CHECK (via IN ('platform', 'project')),
  message_enc     bytea,
  attempts        int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error      text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  sent_at         timestamptz
);
CREATE INDEX auth_email_outbox_due ON auth_email_outbox (next_attempt_at) WHERE sent_at IS NULL AND message_enc IS NOT NULL;
CREATE INDEX auth_email_outbox_project ON auth_email_outbox (project_id, created_at);

-- Messages sent for a project's users (V4 §11.1): metering and fraud
-- monitoring. Email now; SMS and WhatsApp with M32.
CREATE TABLE message_sends (
  id          bigserial PRIMARY KEY,
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  channel     text NOT NULL CHECK (channel IN ('email', 'sms', 'whatsapp')),
  provider    text NOT NULL,
  kind        text NOT NULL,
  country     text,
  status      text NOT NULL CHECK (status IN ('sent', 'failed')),
  cost_minor  bigint,
  currency    text,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX message_sends_project ON message_sends (project_id, created_at);

-- Monthly active users (V4 §12): a user counts once a month per project,
-- when they sign in or refresh a token.
CREATE TABLE auth_mau (
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  month      date NOT NULL,
  user_id    uuid NOT NULL,
  PRIMARY KEY (project_id, month, user_id)
);

-- A rotated-out signing key verifies tokens until verify_until, then retires.
ALTER TABLE project_jwt_keys ADD COLUMN verify_until timestamptz;

-- +goose Down
ALTER TABLE project_jwt_keys DROP COLUMN verify_until;
DROP TABLE auth_mau;
DROP TABLE message_sends;
DROP TABLE auth_email_outbox;
DROP TRIGGER project_auth_config_touch ON project_auth_config;
DROP TABLE project_auth_config;
