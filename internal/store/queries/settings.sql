-- name: GetSetting :one
SELECT value FROM settings WHERE key = @key;

-- name: PutSetting :exec
INSERT INTO settings (key, value) VALUES (@key, @value)
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
