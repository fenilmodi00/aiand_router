BEGIN;

ALTER TABLE router.model_router_request_telemetry
    ADD COLUMN linked_usage_usd_micros BIGINT,
    ADD COLUMN prepaid_usage_usd_micros BIGINT,
    ADD COLUMN subscription_account_id UUID,
    ADD COLUMN subscription_owner_id UUID,
    ADD COLUMN subscription_tier VARCHAR CHECK (subscription_tier IN ('personal', 'shared'));

ALTER TABLE router.model_router_request_telemetry
    ADD CONSTRAINT model_router_request_telemetry_linked_usage_check
        CHECK (
            (
                capacity_source IN ('linked_claude', 'linked_codex')
                AND linked_usage_usd_micros IS NOT NULL
                AND linked_usage_usd_micros = retail_usage_usd_micros
            )
            OR (
                capacity_source IS NULL
                OR capacity_source NOT IN ('linked_claude', 'linked_codex')
            )
            AND linked_usage_usd_micros IS NULL
        ),
    ADD CONSTRAINT model_router_request_telemetry_prepaid_usage_check
        CHECK (
            (
                capacity_source = 'prepaid'
                AND prepaid_usage_usd_micros IS NOT NULL
                AND prepaid_usage_usd_micros = retail_usage_usd_micros
            )
            OR (
                capacity_source IS DISTINCT FROM 'prepaid'
                AND prepaid_usage_usd_micros IS NULL
            )
        );

COMMIT;
