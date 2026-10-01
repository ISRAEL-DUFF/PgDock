-- Operator authentication (M2, spec §7.2).

-- +goose Up
ALTER TABLE operators
  ADD COLUMN failed_logins  int NOT NULL DEFAULT 0,
  ADD COLUMN locked_until   timestamptz,
  ADD COLUMN totp_last_step bigint NOT NULL DEFAULT 0;   -- TOTP replay protection

-- Pending logins (password accepted, TOTP outstanding) and pending setup
-- (owner account waiting for its first TOTP code). id is a SHA-256 hash of
-- the token handed to the browser; payload is master-key encrypted.
CREATE TABLE auth_challenges (
  id          text PRIMARY KEY,
  kind        text NOT NULL CHECK (kind IN ('login', 'setup')),
  operator_id uuid REFERENCES operators(id) ON DELETE CASCADE,
  payload     bytea,
  attempts    int NOT NULL DEFAULT 0,
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX auth_challenges_expires ON auth_challenges (expires_at);

CREATE INDEX sessions_operator ON sessions (operator_id);
CREATE INDEX audit_log_created ON audit_log (created_at DESC, id DESC);
CREATE INDEX audit_log_action ON audit_log (action, created_at DESC);

-- +goose Down
DROP INDEX audit_log_action;
DROP INDEX audit_log_created;
DROP INDEX sessions_operator;
DROP TABLE auth_challenges;
ALTER TABLE operators DROP COLUMN failed_logins, DROP COLUMN locked_until, DROP COLUMN totp_last_step;
