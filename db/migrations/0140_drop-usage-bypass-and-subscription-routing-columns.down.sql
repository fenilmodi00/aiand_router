BEGIN;

ALTER TABLE router.model_router_installations
    ADD COLUMN usage_bypass_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN usage_bypass_threshold DOUBLE PRECISION CHECK (usage_bypass_threshold BETWEEN 0 AND 1),
    ADD COLUMN subscription_routing_disabled BOOLEAN NOT NULL DEFAULT FALSE;

COMMIT;
