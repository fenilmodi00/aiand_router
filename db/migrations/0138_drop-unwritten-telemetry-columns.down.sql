BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN linked_usage_usd_micros BIGINT,
    ADD COLUMN prepaid_usage_usd_micros BIGINT,
    ADD COLUMN subscription_account_id UUID,
    ADD COLUMN subscription_owner_id UUID,
    ADD COLUMN subscription_tier VARCHAR CHECK (subscription_tier IN ('personal', 'shared'));

COMMIT;
