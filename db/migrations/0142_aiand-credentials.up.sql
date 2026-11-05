BEGIN;

-- One row per installation holding the installation-wide Aiand upstream
-- credential minted by the dashboard's device flow or pasted directly (see
-- internal/auth/aiand_credential.go). Both ciphertexts are Tink-sealed with
-- AAD (installation external ID, "aiand"); paste rows carry an empty refresh
-- ciphertext because a pasted key cannot be rotated or revoked. The access
-- token is additionally propagated into model_router_external_api_keys as the
-- aiand provider's BYOK key so dispatch serves it unchanged.
CREATE TABLE router.aiand_credentials (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id          UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    access_token_ciphertext  BYTEA NOT NULL,
    refresh_token_ciphertext BYTEA NOT NULL,
    expires_at               TIMESTAMPTZ NOT NULL,
    origin                   TEXT NOT NULL CHECK (origin IN ('device', 'paste')),
    user_email               TEXT,
    org_id                   TEXT,
    org_name                 TEXT,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (installation_id)
);

COMMENT ON TABLE router.aiand_credentials IS 'Installation-wide Aiand upstream credential from the dashboard device flow or a pasted key; ciphertexts are Tink-sealed with AAD (installation external ID, aiand)';

COMMIT;
