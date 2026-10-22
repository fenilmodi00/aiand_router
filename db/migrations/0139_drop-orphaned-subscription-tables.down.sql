BEGIN;

CREATE TABLE router.subscriber_credit_balance (
    subscriber_id           UUID        PRIMARY KEY REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    balance_usd_micros      BIGINT      NOT NULL DEFAULT 0,
    low_balance_notified_at TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE router.subscriber_credit_reservations (
    action_id              VARCHAR(128) PRIMARY KEY,
    subscriber_id          UUID NOT NULL REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    router_request_id      VARCHAR(64) NOT NULL,
    api_key_id             VARCHAR(255),
    requested_model        VARCHAR(128) NOT NULL,
    reserved_usd_micros    BIGINT NOT NULL CHECK (reserved_usd_micros > 0),
    settled_usd_micros     BIGINT NOT NULL DEFAULT 0 CHECK (settled_usd_micros >= 0),
    state                  VARCHAR(16) NOT NULL DEFAULT 'reserved'
                           CHECK (state IN ('reserved', 'settled', 'released')),
    capacity_source        VARCHAR(32) NOT NULL DEFAULT 'prepaid'
                           CHECK (capacity_source = 'prepaid'),
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX subscriber_credit_reservations_subscriber_created_idx
    ON router.subscriber_credit_reservations (subscriber_id, created_at DESC);

CREATE TABLE router.subscriber_credit_ledger (
    id                       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_id            UUID        NOT NULL REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    delta_usd_micros         BIGINT      NOT NULL,
    notional_cost_micros     BIGINT      NOT NULL DEFAULT 0,
    balance_after_micros     BIGINT      NOT NULL,
    entry_type               VARCHAR(32) NOT NULL CHECK (entry_type IN ('topup','inference','refund','adjustment')),
    stripe_payment_intent_id VARCHAR(255),
    router_request_id        VARCHAR(64),
    router_model             VARCHAR(128),
    memo                     TEXT,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX subscriber_credit_ledger_subscriber_created_at_idx
    ON router.subscriber_credit_ledger (subscriber_id, created_at DESC);

ALTER TABLE router.subscriber_credit_ledger
    ADD COLUMN authorization_action_id VARCHAR(128)
        REFERENCES router.subscriber_credit_reservations(action_id) ON DELETE SET NULL,
    ADD COLUMN action_id VARCHAR(128),
    ADD COLUMN capacity_source VARCHAR(32);

CREATE UNIQUE INDEX subscriber_credit_ledger_action_id_uidx
    ON router.subscriber_credit_ledger (action_id)
    WHERE action_id IS NOT NULL;

CREATE TABLE router.model_router_subscription_accounts (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  api_key_id             UUID REFERENCES router.model_router_api_keys(id) ON DELETE SET NULL,
  provider               VARCHAR(32) NOT NULL,
  external_account_id    VARCHAR(255) NOT NULL,
  refresh_token_ciphertext BYTEA NOT NULL,
  enabled                BOOLEAN NOT NULL DEFAULT TRUE,
  cooldown_until         TIMESTAMP,
  created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (api_key_id, provider, external_account_id),
  CHECK (provider IN ('claude', 'codex'))
);

CREATE INDEX model_router_subscription_accounts_api_key_idx
  ON router.model_router_subscription_accounts(api_key_id, provider);

ALTER TABLE router.model_router_subscription_accounts
    ADD COLUMN access_token_ciphertext BYTEA,
    ADD COLUMN access_token_expires_at TIMESTAMP,
    ADD COLUMN token_refresh_lease_until TIMESTAMP,
    ADD COLUMN token_refresh_lease_id UUID,
    ADD COLUMN token_refresh_version BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN subscriber_id UUID REFERENCES router.credential_subjects(id) ON DELETE CASCADE,
    ADD COLUMN health_state VARCHAR(32) NOT NULL DEFAULT 'healthy',
    ADD COLUMN display_name TEXT,
    ADD COLUMN provider_user_id TEXT;

ALTER TABLE router.model_router_subscription_accounts
    ADD CONSTRAINT model_router_subscription_accounts_health_state_valid
        CHECK (health_state IN ('healthy', 'reconnect_required', 'disabled'));

CREATE UNIQUE INDEX model_router_subscription_accounts_subscriber_account_idx
    ON router.model_router_subscription_accounts(subscriber_id, provider, external_account_id)
    WHERE subscriber_id IS NOT NULL AND provider <> 'codex';

CREATE UNIQUE INDEX model_router_subscription_accounts_codex_subscriber_idx
    ON router.model_router_subscription_accounts(subscriber_id, external_account_id, provider_user_id)
    WHERE subscriber_id IS NOT NULL AND provider = 'codex';

CREATE UNIQUE INDEX model_router_subscription_accounts_codex_identity_idx
    ON router.model_router_subscription_accounts(external_account_id, provider_user_id)
    WHERE provider = 'codex' AND provider_user_id IS NOT NULL;

CREATE TABLE router.model_router_subscription_account_installations (
    installation_id UUID NOT NULL REFERENCES router.model_router_installations(id) ON DELETE CASCADE,
    subscription_account_id UUID NOT NULL REFERENCES router.model_router_subscription_accounts(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (installation_id, subscription_account_id)
);

CREATE INDEX model_router_subscription_account_installations_account_idx
ON router.model_router_subscription_account_installations(subscription_account_id);

COMMIT;
