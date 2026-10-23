package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/policy"
	"aiand-os/router/internal/router/sessionpin"
	"aiand-os/router/internal/translate"
)

const handoffTestBody = `{"model":"deepseek/deepseek-v4.1-flash","max_tokens":1024,"stream":false,"metadata":{"user_id":"pi:handoff-test"},"messages":[{"role":"user","content":"Implement the parser and verify its behavior."}]}`

type handoffResponseProvider struct{ betaCaptureProvider }

func (p *handoffResponseProvider) Proxy(ctx context.Context, decision router.Decision, req providers.PreparedRequest, w http.ResponseWriter, request *http.Request) error {
	if err := p.betaCaptureProvider.Proxy(ctx, decision, req, w, request); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	_, err := io.WriteString(w, `{"type":"message","role":"assistant","content":[{"type":"text","text":"Review complete."}],"stop_reason":"end_turn","usage":{"input_tokens":2000,"output_tokens":10}}`)
	return err
}

func handoffTestService() (*Service, *betaTestRouter, *betaCaptureProvider, context.Context) {
	classifier := &betaTestRouter{decision: router.Decision{
		Model: "z-ai/glm-5.2", Provider: providers.ProviderAIAND,
		Metadata: &router.RoutingMetadata{PolicyGroup: "high", AuthoritativePerTurnSelection: true},
	}}
	upstream := &betaCaptureProvider{}
	svc := NewService(nil, map[string]providers.Client{providers.ProviderAIAND: upstream}, nil, false, nil, nil, false, providers.ProviderAIAND, "z-ai/glm-5.3-flash", nil).
		WithPiHandoffSecret(strings.Repeat("test-secret-", 4)).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: classifier, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "handoff-test-key")
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
	return svc, classifier, upstream, router.WithStrategy(ctx, router.StrategyHMM)
}

func prepareTestHandoff(t *testing.T, svc *Service, ctx context.Context) preparedHandoff {
	t.Helper()
	recorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(WithHandoffPreparation(ctx), []byte(handoffTestBody), recorder, httptest.NewRequest("POST", "/v1/route/handoff", nil)))
	var prepared preparedHandoff
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &prepared), recorder.Body.String())
	require.NotEmpty(t, prepared.Token)
	require.NotEmpty(t, prepared.SessionToken)
	return prepared
}

func TestPiHandoffRejectsTamperingIdentityAndExpiration(t *testing.T) {
	svc, _, upstream, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.Token)
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		ctx   context.Context
		token string
	}{
		{"other-key", context.WithValue(ctx, APIKeyIDContextKey{}, "another-key"), prepared.Token},
		{"other-installation", context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString()), prepared.Token},
		{"tampered", ctx, prepared.Token + "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			altered, err := sjson.Set(body, piHandoffField, test.token)
			require.NoError(t, err)
			_, _, err = svc.parseHandoff(test.ctx, []byte(altered))
			require.ErrorIs(t, err, ErrHandoffInvalid)
		})
	}
	parsed, _, err := svc.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	claims := handoffFromContext(parsed)
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(svc.piHandoffSecret)
	require.NoError(t, err)
	body, err = sjson.Set(body, piHandoffField, expired)
	require.NoError(t, err)
	_, _, err = svc.parseHandoff(ctx, []byte(body))
	require.ErrorIs(t, err, ErrHandoffInvalid)
	require.Empty(t, upstream.body)
}

func TestPiHandoffRevalidatesSessionConfigurationAndEligibility(t *testing.T) {
	svc, _, _, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.Token)
	require.NoError(t, err)
	parsed, clean, err := svc.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(clean)
	require.NoError(t, err)
	_, req, err := svc.anthropicRoutingRequest(parsed, clean, nil, "handoff-test")
	require.NoError(t, err)
	_, err = svc.resumeHandoff(parsed, env, req, false)
	require.NoError(t, err)
	for _, mutation := range []struct {
		name  string
		apply func(*router.Request)
	}{
		{"tools", func(r *router.Request) { r.ToolConfigurationSHA256 = "changed" }},
		{"reasoning", func(r *router.Request) { r.ReasoningConfigurationSHA256 = "changed" }},
		{"model", func(r *router.Request) { r.RequestedModel = "z-ai/glm-5.3-flash" }},
		{"excluded", func(r *router.Request) { r.ExcludedModels = map[string]struct{}{prepared.Model: {}} }},
		{"provider", func(r *router.Request) { r.EnabledProviders = map[string]struct{}{} }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := req
			mutation.apply(&changed)
			_, err := svc.resumeHandoff(parsed, env, changed, false)
			require.ErrorIs(t, err, ErrHandoffInvalid)
		})
	}
	otherSession, err := translate.ParseAnthropic([]byte(strings.ReplaceAll(string(clean), "handoff-test", "other-session")))
	require.NoError(t, err)
	_, err = svc.resumeHandoff(parsed, otherSession, req, false)
	require.ErrorIs(t, err, ErrHandoffInvalid)
}

func TestPiHandoffDisabledAndUtilityTurnsDoNotDispatch(t *testing.T) {
	svc, classifier, upstream, ctx := handoffTestService()
	for _, prompt := range []string{"/beta", "/force-model claude-opus-4-7", "/unforce-model"} {
		body, err := sjson.Set(handoffTestBody, "messages.0.content", prompt)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		require.NoError(t, svc.ProxyMessages(WithHandoffPreparation(ctx), []byte(body), recorder, httptest.NewRequest("POST", "/v1/route/handoff", nil)))
		require.True(t, gjson.GetBytes(recorder.Body.Bytes(), "bypass").Bool())
	}
	svc.piHandoffSecret = nil
	err := svc.ProxyMessages(WithHandoffPreparation(ctx), []byte(handoffTestBody), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/route/handoff", nil))
	require.ErrorIs(t, err, ErrHandoffUnavailable)
	require.Zero(t, classifier.calls)
	require.Empty(t, upstream.body)
}

func TestPiHandoffRejectsConcurrentForceAndCrossSessionCommandsBeforeMutation(t *testing.T) {
	svc, _, upstream, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.Token)
	require.NoError(t, err)
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{Model: "z-ai/glm-5.3-flash", Provider: providers.ProviderAIAND, Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour)}
	svc.pinStore = store
	err = svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))
	require.ErrorIs(t, err, ErrHandoffInvalid)
	for _, prompt := range []string{"/beta", "/force-model claude-opus-4-7", "/unforce-model"} {
		changed, err := sjson.Set(body, "messages.0.content", prompt)
		require.NoError(t, err)
		err = svc.ProxyMessages(ctx, []byte(changed), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))
		require.ErrorIs(t, err, ErrHandoffInvalid)
	}
	require.Empty(t, upstream.body)
	require.Empty(t, store.upserts)
}

func TestForceParentDoesNotRejectChildHandoff(t *testing.T) {
	svc, _, _, ctx := handoffTestService()
	svc.pinStore = newForceModelMapStore()
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.Token)
	require.NoError(t, err)
	parsed, clean, err := svc.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(clean)
	require.NoError(t, err)
	_, req, err := svc.anthropicRoutingRequest(parsed, clean, nil, "handoff-test")
	require.NoError(t, err)
	claims := handoffFromContext(parsed)
	apiKeyID, _ := parsed.Value(APIKeyIDContextKey{}).(string)
	installationID := claims.Route.InstallationID
	controlKey := deriveForceModelSessionKeyForRequest(parsed, env, apiKeyID, claims.Route.SessionKey)
	_, _, err = svc.applyForceModelCommand(parsed, translate.ForceModelResult{Model: "opus", ParentOnly: true}, installationID, claims.Route.SessionKey, controlKey)
	require.NoError(t, err)
	_, err = svc.resumeHandoff(parsed, env, req, true)
	require.NoError(t, err, "parent-only force must preserve a child's prepared continuation")
	_, err = svc.resumeHandoff(parsed, env, req, false)
	require.ErrorIs(t, err, ErrHandoffInvalid, "the force still invalidates a parent continuation")
}
