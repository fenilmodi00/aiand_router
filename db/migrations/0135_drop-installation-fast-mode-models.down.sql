BEGIN;

ALTER TABLE router.model_router_installations
  ADD COLUMN fast_mode_models TEXT[] NOT NULL DEFAULT '{}';

COMMIT;
