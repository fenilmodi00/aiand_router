package proxy

import (
	"context"
	"errors"
	"testing"
	"aiand-os/router/internal/dispatch"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"

	"github.com/stretchr/testify/assert"
)

func compatibilityService(mode TranslationCompatibilityMode) *Service {
	return &Service{
		clients: dispatch.NewClients(map[string]providers.Client{
			providers.ProviderAIAND: nil,
		}),
		translationCompatibilityMode: mode,
	}
}

func TestApplyTranslationPlan_IntrinsicallyIncompatible(t *testing.T) {
	svc := compatibilityService(TranslationCompatibilityEnforce)
	_, err := svc.applyTranslationPlan(context.Background(), router.Request{
		TranslationRequirements: router.TranslationRequirements{NativeOnly: true},
	})

	assert.True(t, errors.Is(err, ErrTranslationIntrinsicallyIncompatible))
}

func requireExclusion(t *testing.T, plan TranslationPlan, code, provider string, enforced bool) {
	t.Helper()
	for _, exclusion := range plan.Exclusions {
		if exclusion.Code == code && exclusion.Provider == provider {
			assert.Equal(t, enforced, exclusion.Enforced)
			return
		}
	}
	t.Fatalf("missing exclusion code=%q provider=%q in %#v", code, provider, plan.Exclusions)
}
