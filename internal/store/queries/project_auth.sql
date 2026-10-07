-- Auth core (V4 §4, M31): settings, queued emails, sends, active users.

-- name: GetAuthConfig :one
-- tenant: system - a project the request already authorized.
SELECT * FROM project_auth_config WHERE project_id = @project_id;

-- name: UpsertAuthConfig :one
-- tenant: system - a project the request already authorized.
INSERT INTO project_auth_config (project_id, config, providers_enc, templates)
VALUES (@project_id, @config, @providers_enc, @templates)
ON CONFLICT (project_id) DO UPDATE SET config = EXCLUDED.config, providers_enc = EXCLUDED.providers_enc,
  templates = EXCLUDED.templates, updated_at = now()
RETURNING *;

-- name: EdgeAuthConfigs :many
-- tenant: system - pgdock-edge's configuration feed: auth settings.
SELECT project_id, config FROM project_auth_config WHERE project_id = ANY(@project_ids::uuid[]);

-- name: EdgeSigningKeys :many
-- tenant: system - pgdock-edge's configuration feed: the active signing keys.
SELECT project_id, id, kid, private_enc FROM project_jwt_keys
WHERE project_id = ANY(@project_ids::uuid[]) AND status = 'active';

-- name: DemoteActiveJWTKey :exec
-- tenant: system - a project the request already authorized.
UPDATE project_jwt_keys SET status = 'verifying', verify_until = @verify_until
WHERE project_id = @project_id AND status = 'active';

-- name: RetireJWTKeys :execrows
-- tenant: system - the signing-key sweep across all projects.
UPDATE project_jwt_keys SET status = 'retired', retired_at = now()
WHERE status = 'verifying' AND verify_until IS NOT NULL AND verify_until < now();

-- name: InsertAuthEmail :exec
-- tenant: system - an email pgdock-edge asked for, for the project it named.
INSERT INTO auth_email_outbox (id, project_id, kind, via, message_enc) VALUES (@id, @project_id, @kind, @via, @message_enc);

-- name: CountPlatformAuthEmails :one
-- tenant: system - a project the caller resolved.
SELECT count(*) FROM auth_email_outbox WHERE project_id = @project_id AND via = 'platform' AND created_at > @since;

-- name: DueAuthEmails :many
-- tenant: system - the auth email sender across all projects.
SELECT * FROM auth_email_outbox
WHERE sent_at IS NULL AND message_enc IS NOT NULL AND next_attempt_at <= now()
ORDER BY next_attempt_at
LIMIT @lim
FOR UPDATE SKIP LOCKED;

-- name: MarkAuthEmailSent :exec
-- tenant: system - the auth email sender.
UPDATE auth_email_outbox SET sent_at = now(), message_enc = NULL, attempts = attempts + 1, last_error = NULL WHERE id = @id;

-- name: MarkAuthEmailFailed :exec
-- tenant: system - the auth email sender; give_up drops the message.
UPDATE auth_email_outbox SET attempts = attempts + 1, last_error = @last_error, next_attempt_at = @next_attempt_at,
  message_enc = CASE WHEN @give_up::boolean THEN NULL ELSE message_enc END
WHERE id = @id;

-- name: PruneAuthEmails :execrows
-- tenant: system - the auth email sender across all projects.
DELETE FROM auth_email_outbox WHERE created_at < @before;

-- name: InsertMessageSend :exec
-- tenant: system - a message sent for the project the caller resolved.
INSERT INTO message_sends (project_id, channel, provider, kind, status) VALUES (@project_id, @channel, @provider, @kind, @status);

-- name: ProjectMessageSends :many
-- tenant: system - a project the request already authorized.
SELECT channel, provider, status, count(*)::bigint AS n FROM message_sends
WHERE project_id = @project_id AND created_at > @since
GROUP BY channel, provider, status ORDER BY channel, provider, status;

-- name: InsertActiveUser :execrows
-- tenant: system - pgdock-edge's report, for projects it serves.
INSERT INTO auth_mau (project_id, month, user_id) VALUES (@project_id, @month, @user_id) ON CONFLICT DO NOTHING;

-- name: ProjectMAU :one
-- tenant: system - a project the request already authorized.
SELECT count(*) FROM auth_mau WHERE project_id = @project_id AND month = @month;
