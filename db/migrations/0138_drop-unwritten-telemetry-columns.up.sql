BEGIN;

-- These telemetry columns have no writer left in the router: the linked and
-- prepaid capacity sources no longer exist, and the subscription winner
-- attribution was never populated. Drop the columns; their check constraints
-- go with them.
ALTER TABLE router.model_router_request_telemetry
    DROP COLUMN linked_usage_usd_micros,
    DROP COLUMN prepaid_usage_usd_micros,
    DROP COLUMN subscription_account_id,
    DROP COLUMN subscription_owner_id,
    DROP COLUMN subscription_tier;

COMMIT;
