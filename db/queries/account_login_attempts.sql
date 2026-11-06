-- Device-flow login attempts, replacing the retired in-memory aiandPending
-- map. Keyed by an unguessable attempt id (the row's UUID); the gateway
-- device_code is Tink-sealed with AAD (attempt id string, "aiand-device").
-- An attempt is consumed only after the full persistence transaction
-- succeeds; consumed rows are retained until GC for audit.

-- The login service mints the attempt id (a UUID) before the insert so it
-- can seal the device code with AAD (attempt id, "aiand-device"); passing it
-- explicitly also keeps the seal and the row key identical after a retry.
-- Callers that omit the id keep the DB-generated default.
-- name: InsertLoginAttempt :one
INSERT INTO router.account_login_attempts (
    id,
    device_code_ciphertext,
    gateway_interval_seconds,
    expires_at
)
VALUES (
    COALESCE(sqlc.narg('id')::uuid, gen_random_uuid()),
    @device_code_ciphertext::bytea,
    @gateway_interval_seconds::int,
    @expires_at::timestamptz
)
RETURNING *;

-- The poll lookup: only an unconsumed, unexpired attempt can be advanced.
-- The adapter maps pgx.ErrNoRows to the caller's expired-or-unknown path.
-- name: GetActiveLoginAttempt :one
SELECT *
FROM router.account_login_attempts
WHERE id = @id::uuid
  AND consumed_at IS NULL
  AND expires_at > NOW();

-- Marks the attempt consumed after the login persistence transaction
-- committed. Failed polls leave the row pollable.
-- name: MarkLoginAttemptConsumed :exec
UPDATE router.account_login_attempts
SET consumed_at = NOW()
WHERE id = @id::uuid
  AND consumed_at IS NULL;

-- Disconnect cleanup: removes the attempt row so a stale approval can never
-- silently reconnect (clear-on-disconnect semantics).
-- name: DeleteLoginAttempt :exec
DELETE FROM router.account_login_attempts
WHERE id = @id::uuid;

-- Hourly GC input (called from the existing rotation sweep): audit retention
-- ends once the window plus a grace margin has passed.
-- name: DeleteExpiredLoginAttempts :execrows
DELETE FROM router.account_login_attempts
WHERE expires_at < @now::timestamptz;
