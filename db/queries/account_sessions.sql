-- Dashboard cookie sessions. The token is opaque; only its SHA-256 hex hash
-- is stored. Expiry is sliding: TouchSessionLastSeen extends both last_seen_at
-- and expires_at on verified requests so mid-use expiry is rare.

-- name: InsertAccountSession :one
INSERT INTO router.account_sessions (
    account_id,
    token_hash,
    token_prefix,
    expires_at
)
VALUES (
    @account_id::varchar,
    @token_hash::varchar,
    @token_prefix::varchar,
    @expires_at::timestamptz
)
RETURNING *;

-- The session-middleware lookup. Joins the account and the installation in
-- one row so the handler gets installation scoping (and the account's
-- identity) without extra queries. Expired and revoked sessions never match;
-- the adapter maps pgx.ErrNoRows to the caller's "unauthenticated" path.
-- name: GetActiveSessionByTokenHash :one
SELECT sqlc.embed(s), sqlc.embed(a), sqlc.embed(i)
FROM router.account_sessions s
INNER JOIN router.accounts a ON a.id = s.account_id
INNER JOIN router.model_router_installations i ON i.id = a.installation_id
WHERE s.token_hash = @token_hash::varchar
  AND s.revoked_at IS NULL
  AND s.expires_at > NOW()
  AND a.deleted_at IS NULL;

-- Sliding-window touch on a verified request: bumps last_seen_at and extends
-- expires_at so an actively used session does not expire mid-use. Cookie
-- Max-Age is rewritten from the new expires_at by the middleware.
-- name: TouchSessionLastSeen :exec
UPDATE router.account_sessions
SET last_seen_at = NOW(),
    expires_at = @expires_at::timestamptz
WHERE token_hash = @token_hash::varchar
  AND revoked_at IS NULL
  AND expires_at > NOW();

-- Logout for a single cookie: idempotent (revoking twice is a no-op).
-- name: RevokeSessionByTokenHash :exec
UPDATE router.account_sessions
SET revoked_at = NOW()
WHERE token_hash = @token_hash::varchar
  AND revoked_at IS NULL;

-- "Log out everywhere": revokes every live session of the account.
-- name: RevokeAllSessionsForAccount :execrows
UPDATE router.account_sessions
SET revoked_at = NOW()
WHERE account_id = @account_id::varchar
  AND revoked_at IS NULL;
