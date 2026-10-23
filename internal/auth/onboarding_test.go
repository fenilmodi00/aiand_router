package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"aiand-os/router/internal/auth"
)

type onboardingRecorder struct {
	mu         sync.Mutex
	harnesses  []auth.APIKeyFirstUsedEvent
	lifecycles []auth.HarnessLifecycleEvent
}

func (r *onboardingRecorder) APIKeyFirstUsed(event auth.APIKeyFirstUsedEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.harnesses = append(r.harnesses, event)
}

func (r *onboardingRecorder) HarnessLifecycle(event auth.HarnessLifecycleEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lifecycles = append(r.lifecycles, event)
}

func (r *onboardingRecorder) harnessEvents() []auth.APIKeyFirstUsedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]auth.APIKeyFirstUsedEvent(nil), r.harnesses...)
}

func TestVerifyAPIKeyReportsFirstUseOnceIncludingCacheHits(t *testing.T) {
	installation := &auth.Installation{ID: "installation", ExternalID: "org-test"}
	first := &auth.APIKey{
		ID: "key-first", InstallationID: installation.ID, ExternalID: "kid-first",
		KeyHash: auth.HashAPIKeySHA256("rk_first"), Scope: auth.ScopeRouting,
		CredentialSubjectID: "subject", Harness: "codex",
	}
	keys := &fakeAPIKeyRepository{byHash: map[string]fakeKeyRow{
		first.KeyHash: {apiKey: first, installation: installation},
	}}
	recorder := &onboardingRecorder{}
	cache := newRecordingAPIKeyCache()
	svc := auth.NewService(&fakeInstallationRepository{}, keys, nil, nil, cache, nil, frozenClock()).
		WithOnboardingObserver(recorder)

	_, _, _, _, err := svc.VerifyAPIKey(context.Background(), "rk_first")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(recorder.harnessEvents()) == 1 }, time.Second, time.Millisecond)
	want := []auth.APIKeyFirstUsedEvent{{
		InstallationExternalID: installation.ExternalID, CredentialSubjectID: "subject",
		APIKeyID: first.ID, Harness: "codex", OccurredAt: frozenClock()(),
	}}
	require.Equal(t, want, recorder.harnessEvents())

	for range 3 {
		_, _, _, _, err = svc.VerifyAPIKey(context.Background(), "rk_first")
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return len(keys.markUsedSnapshot()) == 4 }, time.Second, time.Millisecond)
	require.Never(t, func() bool { return len(recorder.harnessEvents()) != 1 }, 50*time.Millisecond, time.Millisecond)
	require.Equal(t, want, recorder.harnessEvents())
}

func TestVerifyAPIKeyDoesNotReportIneligibleConnections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		harness string
		scope   auth.APIKeyScope
		markErr error
	}{
		{name: "analytics", harness: "codex", scope: auth.ScopeAnalyticsRead},
		{name: "persistence failure", harness: "codex", scope: auth.ScopeRouting, markErr: errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.scope.TokenPrefix() + "_test"
			installation := &auth.Installation{ID: "installation", ExternalID: "org-test"}
			key := &auth.APIKey{ID: "key", InstallationID: installation.ID, KeyHash: auth.HashAPIKeySHA256(raw), Scope: tc.scope, Harness: tc.harness}
			svc, keys := makeService(t, fakeKeyRow{apiKey: key, installation: installation})
			keys.markUsedErr = tc.markErr
			recorder := &onboardingRecorder{}
			svc.WithOnboardingObserver(recorder)
			if tc.scope == auth.ScopeAnalyticsRead {
				_, _, err := svc.VerifyAnalyticsAPIKey(context.Background(), raw)
				require.NoError(t, err)
			} else {
				_, _, _, _, err := svc.VerifyAPIKey(context.Background(), raw)
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool { return len(keys.markUsedSnapshot()) == 1 }, time.Second, time.Millisecond)
			require.Never(t, func() bool { return len(recorder.harnessEvents()) != 0 }, 50*time.Millisecond, time.Millisecond)
		})
	}
}

func TestReportHarnessLifecycleForwardsAuthenticatedIdentity(t *testing.T) {
	installation := &auth.Installation{ID: "installation", ExternalID: "org-test"}
	key := &auth.APIKey{ID: "key", InstallationID: installation.ID, CredentialSubjectID: "subject", Harness: "codex"}
	recorder := &onboardingRecorder{}
	svc := auth.NewService(&fakeInstallationRepository{}, &fakeAPIKeyRepository{}, nil, nil, nil, nil, frozenClock()).
		WithOnboardingObserver(recorder)

	svc.ReportHarnessLifecycle(installation, key, auth.LifecycleHarnessCodex, auth.HarnessLifecycleActionOff)

	require.Equal(t, []auth.HarnessLifecycleEvent{{
		InstallationExternalID: "org-test", CredentialSubjectID: "subject", APIKeyID: "key",
		Harness: auth.LifecycleHarnessCodex, Action: auth.HarnessLifecycleActionOff, OccurredAt: frozenClock()(),
	}}, recorder.lifecycles)
}

func TestReportHarnessLifecycleSkipsWithoutObserverOrIdentity(t *testing.T) {
	installation := &auth.Installation{ID: "installation", ExternalID: "org-test"}
	key := &auth.APIKey{ID: "key"}
	recorder := &onboardingRecorder{}
	unobserved := auth.NewService(&fakeInstallationRepository{}, &fakeAPIKeyRepository{}, nil, nil, nil, nil, frozenClock())
	require.NotPanics(t, func() {
		unobserved.ReportHarnessLifecycle(installation, key, auth.LifecycleHarnessClaude, auth.HarnessLifecycleActionOn)
	})

	observed := auth.NewService(&fakeInstallationRepository{}, &fakeAPIKeyRepository{}, nil, nil, nil, nil, frozenClock()).
		WithOnboardingObserver(recorder)
	observed.ReportHarnessLifecycle(nil, key, auth.LifecycleHarnessClaude, auth.HarnessLifecycleActionOn)
	observed.ReportHarnessLifecycle(installation, nil, auth.LifecycleHarnessClaude, auth.HarnessLifecycleActionOn)
	require.Empty(t, recorder.lifecycles)
}
