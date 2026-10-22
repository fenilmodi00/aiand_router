BEGIN;

ALTER TABLE router.model_router_installations
    ADD COLUMN subscription_sharing_enabled BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE router.model_router_api_keys
    ADD COLUMN shared_subscription_access BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN paid_fallback_on_subscription_refusal BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
