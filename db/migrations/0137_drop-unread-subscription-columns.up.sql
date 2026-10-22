BEGIN;

-- No Go reader consumes these three columns: the router always resolves a
-- key's subscription access from the linked account, never from a stored
-- flag. Drop them and their plumbing.
ALTER TABLE router.model_router_api_keys
    DROP COLUMN shared_subscription_access,
    DROP COLUMN paid_fallback_on_subscription_refusal;

ALTER TABLE router.model_router_installations
    DROP COLUMN subscription_sharing_enabled;

COMMIT;
