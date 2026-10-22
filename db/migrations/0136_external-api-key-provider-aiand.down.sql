BEGIN;

-- Restore the pre-cut allowlist. 'aiand' stays listed so rows written while
-- this migration was applied still validate the constraint on rollback.
ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN (
    'aiand','anthropic','openai','google','openrouter','fireworks',
    'bedrock','makora','together','xai','anthropic_gateway','openai_gateway'
  ));

COMMIT;
