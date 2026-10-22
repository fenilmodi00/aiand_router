BEGIN;

-- The provider CHECK is an allowlist and still listed the 11 providers that
-- were deleted in the Aiand-only cut, so an 'aiand' BYOK key was rejected at
-- INSERT. Narrow the allowlist to the only provider that exists.
ALTER TABLE router.model_router_external_api_keys
  DROP CONSTRAINT model_router_external_api_keys_provider_check;

ALTER TABLE router.model_router_external_api_keys
  ADD CONSTRAINT model_router_external_api_keys_provider_check
  CHECK (provider IN ('aiand'));

COMMIT;
