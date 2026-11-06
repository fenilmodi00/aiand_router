-- One row per Aiand identity (user + org). The id is 'acct_' + 24 random
-- chars (auth.GenerateID) and doubles as the installation external_id used
-- as Tink AAD for credential unsealing. The partial unique index on the
-- identity pair arbitrates concurrent first logins: the unique-violation
-- loser re-reads by identity and reuses the winner's account + installation
-- (arbitration lives in the postgres adapter, not in SQL — SQLC cannot
-- target a partial index with ON CONFLICT).

-- Creates the account for a first-time login of a new identity. The caller
-- already created the installation and holds its id. A unique violation on
-- accounts_identity_active_unique means a concurrent login won; the adapter
-- catches it, re-reads by identity, and reuses the winner's rows.
-- The login service mints the account id (GenerateID("acct")) so it can
-- create the installation with external_id = account id (the AAD identity)
-- inside the same transaction; omitting it keeps GenerateID in the adapter.
-- name: InsertAccount :one
INSERT INTO router.accounts (
    id,
    aiand_user_id,
    aiand_org_id,
    email,
    display_name,
    installation_id
)
VALUES (
    COALESCE(sqlc.narg('id')::varchar, @generated_id::varchar),
    @aiand_user_id::varchar,
    @aiand_org_id::varchar,
    sqlc.narg('email')::varchar,
    sqlc.narg('display_name')::varchar,
    @installation_id::uuid
)
RETURNING *;

-- The login-identity lookup: exact (user, org) pair among non-deleted
-- accounts. Email is display-only and never part of the identity.
-- name: GetAccountByIdentity :one
SELECT *
FROM router.accounts
WHERE aiand_user_id = @aiand_user_id::varchar
  AND aiand_org_id = @aiand_org_id::varchar
  AND deleted_at IS NULL;

-- name: GetAccountByID :one
SELECT *
FROM router.accounts
WHERE id = @id::varchar
  AND deleted_at IS NULL;

-- Soft-deletes the account; sessions and the credential keep their rows but
-- the account stops resolving. The installation row is left intact — the
-- rotation sweep excludes soft-deleted installations via its join.
-- name: SoftDeleteAccount :exec
UPDATE router.accounts
SET deleted_at = NOW()
WHERE id = @id::varchar
  AND deleted_at IS NULL;
