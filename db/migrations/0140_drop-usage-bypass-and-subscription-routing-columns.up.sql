BEGIN;

-- The usage-bypass gate and the subscription-routing-disabled switch have no
-- writer and no reader: the subscription credential path is gone, so both
-- columns are dead. Drop them.
ALTER TABLE router.model_router_installations
    DROP COLUMN usage_bypass_enabled,
    DROP COLUMN usage_bypass_threshold,
    DROP COLUMN subscription_routing_disabled;

COMMIT;
