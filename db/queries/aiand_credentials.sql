-- One row per installation (UNIQUE on installation_id): the Aiand upstream
-- credential the router serves every user with. Tokens are stored sealed;
-- plaintext exists only long enough to propagate the access token into
-- model_router_external_api_keys and is never logged.

-- Replaces the installation's credential. Paste logins store an empty refresh
-- ciphertext; the gateway never issued a refresh token for that key.
-- name: UpsertAiandCredential :one
INSERT INTO router.aiand_credentials (
    installation_id,
    access_token_ciphertext,
    refresh_token_ciphertext,
    expires_at,
    origin,
    user_email,
    org_id,
    org_name
)
VALUES (
    @installation_id::uuid,
    @access_token_ciphertext::bytea,
    @refresh_token_ciphertext::bytea,
    @expires_at::timestamptz,
    @origin::text,
    sqlc.narg('user_email')::text,
    sqlc.narg('org_id')::text,
    sqlc.narg('org_name')::text
)
ON CONFLICT (installation_id) DO UPDATE SET
    access_token_ciphertext = EXCLUDED.access_token_ciphertext,
    refresh_token_ciphertext = EXCLUDED.refresh_token_ciphertext,
    expires_at = EXCLUDED.expires_at,
    origin = EXCLUDED.origin,
    user_email = EXCLUDED.user_email,
    org_id = EXCLUDED.org_id,
    org_name = EXCLUDED.org_name,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetAiandCredentialByInstallation :one
SELECT *
FROM router.aiand_credentials
WHERE installation_id = @installation_id::uuid;

-- name: DeleteAiandCredential :execrows
DELETE FROM router.aiand_credentials
WHERE installation_id = @installation_id::uuid;

-- Hourly rotation sweep input: every credential whose access token expires
-- before the caller-computed threshold (now + rotation window is computed in
-- Go, the query takes a plain timestamp). Joins the installation so the auth
-- Service can rebuild the minimal *auth.Installation the rotation path needs
-- (ID for propagation, external ID for AAD unsealing). Soft-deleted
-- installations are excluded; their rows go away with the FK cascade.
-- name: ListExpiringAiandCredentials :many
SELECT sqlc.embed(c), sqlc.embed(i)
FROM router.aiand_credentials c
INNER JOIN router.model_router_installations i ON i.id = c.installation_id
WHERE c.expires_at < @expires_before::timestamptz
  AND i.deleted_at IS NULL;
