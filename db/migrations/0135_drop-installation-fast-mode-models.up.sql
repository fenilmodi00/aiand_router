BEGIN;

-- The fast-mode opt-in is gone: no catalog binding publishes a fast rate, so
-- the per-installation list could never take effect. Drop the column.
ALTER TABLE router.model_router_installations
  DROP COLUMN fast_mode_models;

COMMIT;
