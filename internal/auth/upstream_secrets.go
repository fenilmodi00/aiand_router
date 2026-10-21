package auth

import (
	"context"
)

// ExternalAPIKeyWithCredential returns a BYOK key with Plaintext resolved,
// so callers authenticate exactly as an inference call would.
func (s *Service) ExternalAPIKeyWithCredential(ctx context.Context, installationID, id string) (*ExternalAPIKey, error) {
	keys, err := s.externalKeys.GetForInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if key.ID != id {
			continue
		}
		if len(key.Plaintext) == 0 {
			return nil, ErrUpstreamCredentialUnavailable
		}
		return key, nil
	}
	return nil, ErrExternalAPIKeyNotFound
}
