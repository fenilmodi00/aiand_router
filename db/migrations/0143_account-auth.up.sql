BEGIN;

-- SaaS identity schema: one router account per Aiand identity (user + org),
-- its 1:1 installation, opaque-cookie dashboard sessions, and device-flow
-- login attempts. See docs/plans/2026-11-06-saas-auth-reform-design.md §DB.
--
-- One row per dashboard identity. The id is 'acct_' + 24 random chars
-- (auth.GenerateID with prefix "acct"), and also serves as the installation's
-- external_id AAD identity for credential unsealing. Concurrent first logins
-- of the same identity arbitrate via the partial unique index: the
-- unique-violation loser re-reads by identity and reuses the winner's
-- account + installation, so no 409 ever reaches the user.
CREATE TABLE router.accounts (
    id              VARCHAR(36) PRIMARY KEY,        -- 'acct_' + 24 random chars = 29 chars (GenerateID with prefix "acct"); column sized with headroom
    aiand_user_id   VARCHAR(128) NOT NULL,
    aiand_org_id    VARCHAR(128) NOT NULL,
    email           VARCHAR(254),
    display_name    VARCHAR(255),
    installation_id UUID NOT NULL UNIQUE REFERENCES router.model_router_installations(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ
);

CREATE UNIQUE INDEX accounts_identity_active_unique
    ON router.accounts (aiand_user_id, aiand_org_id) WHERE deleted_at IS NULL;

COMMENT ON TABLE router.accounts IS 'One row per Aiand identity (user + org); id doubles as the installation external_id used as Tink AAD for credential unsealing';

-- Opaque dashboard cookie tokens, stored hashed. The token hash is the lookup
-- key; token_prefix is display-only (same 8+4 convention as API keys).
-- Expiry is sliding: TouchSessionLastSeen extends expires_at on verified
-- requests, so mid-use expiry is rare.
CREATE TABLE router.account_sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id    VARCHAR(36) NOT NULL REFERENCES router.accounts(id),
    token_hash    VARCHAR(64) NOT NULL UNIQUE,
    token_prefix  VARCHAR(12) NOT NULL,
    issued_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ
);

CREATE INDEX account_sessions_account_idx ON router.account_sessions (account_id, issued_at DESC);
CREATE INDEX account_sessions_expiry_idx ON router.account_sessions (expires_at) WHERE revoked_at IS NULL;

COMMENT ON TABLE router.account_sessions IS 'Dashboard cookie sessions; token_hash is the SHA-256 hex of the opaque cookie token, expiry slides on verified requests';

-- One row per in-flight device-flow login, keyed by an unguessable attempt
-- id. Replaces the retired in-memory aiandPending map so flow state survives
-- restarts and never depends on a pre-existing installation. The gateway
-- device_code is Tink-sealed with AAD (attempt id string, "aiand-device"),
-- the same Encryptor AAD shape as aiand_credentials. Consumed attempts are
-- retained until GC for audit.
CREATE TABLE router.account_login_attempts (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code_ciphertext   BYTEA NOT NULL,
    gateway_interval_seconds INT NOT NULL,
    expires_at               TIMESTAMPTZ NOT NULL,
    consumed_at              TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE router.account_login_attempts IS 'In-flight device-flow logins; device_code is Tink-sealed with AAD (attempt id, aiand-device); consumed rows are kept until GC for audit';

COMMIT;
