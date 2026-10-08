-- +goose Up
-- Backend services' request roles become logins of their own (V4 §2.3, M32):
-- pgdock-edge connects as anon, user, service or the auth hook role directly
-- instead of switching to them from its login, so tenant SQL that resets the
-- role lands on the same role. Their SCRAM verifiers, for the poolers, by role.
ALTER TABLE project_services ADD COLUMN login_verifiers jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE project_services DROP COLUMN login_verifiers;
