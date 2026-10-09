
-- name: SetInstancePITRDays :exec
-- tenant: system - a dedicated project's instance the request already authorized.
UPDATE instances SET pitr_days = @pitr_days WHERE id = @id;
