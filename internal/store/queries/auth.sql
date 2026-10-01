-- name: CountOperators :one
SELECT count(*) FROM operators;

-- name: InsertOperator :one
INSERT INTO operators (email, password_hash, totp_secret, role)
VALUES (@email, @password_hash, @totp_secret, @role)
RETURNING *;

-- name: GetOperatorByEmail :one
SELECT * FROM operators WHERE email = @email;

-- name: GetOperator :one
SELECT * FROM operators WHERE id = @id;

-- name: RecordLoginFailure :one
-- Counts a failed password or TOTP attempt and locks the account for
-- @lock_seconds once @threshold consecutive failures are reached.
UPDATE operators
SET failed_logins = CASE WHEN failed_logins + 1 >= @threshold::int THEN 0 ELSE failed_logins + 1 END,
    locked_until  = CASE WHEN failed_logins + 1 >= @threshold::int THEN now() + make_interval(secs => @lock_seconds::int) ELSE locked_until END
WHERE id = @id
RETURNING failed_logins, locked_until;

-- name: ResetLoginFailures :exec
UPDATE operators SET failed_logins = 0, locked_until = NULL WHERE id = @id;

-- name: AdvanceTOTPStep :execrows
-- Accepts a TOTP step only once: replayed or older codes match no row.
UPDATE operators SET totp_last_step = @step WHERE id = @id AND totp_last_step < @step;

-- name: InsertChallenge :exec
INSERT INTO auth_challenges (id, kind, operator_id, payload, expires_at)
VALUES (@id, @kind, sqlc.narg(operator_id), @payload, @expires_at);

-- name: GetChallenge :one
SELECT * FROM auth_challenges WHERE id = @id AND kind = @kind AND expires_at > now();

-- name: BumpChallengeAttempts :one
UPDATE auth_challenges SET attempts = attempts + 1 WHERE id = @id RETURNING attempts;

-- name: DeleteChallenge :exec
DELETE FROM auth_challenges WHERE id = @id;

-- name: DeleteExpiredChallenges :exec
DELETE FROM auth_challenges WHERE expires_at <= now();

-- Session timestamps come from the auth service's clock, which is also
-- what checks them.
-- name: InsertSession :exec
INSERT INTO sessions (id, operator_id, ip, user_agent, created_at, last_seen_at, reauth_at)
VALUES (@id, @operator_id, sqlc.narg(ip), sqlc.narg(user_agent), @now, @now, @now);

-- name: GetSession :one
SELECT s.id, s.operator_id, s.created_at, s.last_seen_at, s.reauth_at,
       o.email, o.role, o.disabled_at
FROM sessions s JOIN operators o ON o.id = s.operator_id
WHERE s.id = @id;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = @now WHERE id = @id;

-- name: SetSessionReauth :exec
UPDATE sessions SET reauth_at = @now::timestamptz WHERE id = @id;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = @id;

-- name: DeleteIdleSessions :exec
DELETE FROM sessions WHERE last_seen_at < @idle_before OR created_at < @created_before;
