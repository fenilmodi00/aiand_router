package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aiand-os/router/internal/api/admin"
	"aiand-os/router/internal/auth"
	"aiand-os/router/internal/providers"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pollStep is one scripted PollToken outcome.
type pollStep struct {
	token auth.AiandTokenResponse
	err   error
}

// handlerFakeGateway is a minimal auth.AiandGateway for handler tests.
type handlerFakeGateway struct {
	mu sync.Mutex

	startCode auth.AiandDeviceCode
	startErr  error

	pollToken auth.AiandTokenResponse
	pollErr   error

	// pollScript, when non-empty, is consumed step by step by PollToken
	// before falling back to pollErr/pollToken; pollCalls counts calls.
	pollScript []pollStep
	pollCalls  int

	refreshToken auth.AiandTokenResponse
	refreshErr   error

	revokeErr error

	validateUser auth.AiandUser
	validateErr  error
}

func (f *handlerFakeGateway) DeviceCodeStart(context.Context, string) (auth.AiandDeviceCode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCode, f.startErr
}

func (f *handlerFakeGateway) PollToken(context.Context, string) (auth.AiandTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pollCalls++
	if len(f.pollScript) > 0 {
		step := f.pollScript[0]
		f.pollScript = f.pollScript[1:]
		return step.token, step.err
	}
	return f.pollToken, f.pollErr
}

func (f *handlerFakeGateway) RefreshToken(context.Context, string) (auth.AiandTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshToken, f.refreshErr
}

func (f *handlerFakeGateway) Revoke(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revokeErr
}

func (f *handlerFakeGateway) ValidateKey(context.Context, string) (auth.AiandUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.validateUser, f.validateErr
}

// handlerFakeCredentialRepo satisfies auth.AiandCredentialRepository.
type handlerFakeCredentialRepo struct {
	mu   sync.Mutex
	rows map[string]*auth.AiandCredential
}

func (f *handlerFakeCredentialRepo) Upsert(_ context.Context, params auth.UpsertAiandCredentialParams) (*auth.AiandCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := &auth.AiandCredential{
		ID:                     "cred-1",
		InstallationID:         params.InstallationID,
		AccessTokenCiphertext:  params.AccessTokenCiphertext,
		RefreshTokenCiphertext: params.RefreshTokenCiphertext,
		Plaintext:              params.AccessTokenCiphertext,
		ExpiresAt:              params.ExpiresAt,
		Origin:                 params.Origin,
		UserEmail:              params.UserEmail,
		OrgID:                  params.OrgID,
		OrgName:                params.OrgName,
	}
	f.rows[params.InstallationID] = row
	return row, nil
}

func (f *handlerFakeCredentialRepo) GetByInstallation(_ context.Context, installationID string) (*auth.AiandCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[installationID]
	if !ok {
		return nil, auth.ErrAiandCredentialNotFound
	}
	return row, nil
}

func (f *handlerFakeCredentialRepo) Delete(_ context.Context, installationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, installationID)
	return nil
}

func (f *handlerFakeCredentialRepo) ListExpiring(_ context.Context, _ time.Time) ([]auth.AiandExpiringCredential, error) {
	return nil, errors.New("ListExpiring is not used by admin handlers")
}

// handlerFakeExternalKeyRepo satisfies auth.ExternalAPIKeyRepository.
type handlerFakeExternalKeyRepo struct {
	mu      sync.Mutex
	deleted []string
}

func (f *handlerFakeExternalKeyRepo) Create(_ context.Context, params auth.CreateExternalAPIKeyParams) (*auth.ExternalAPIKey, error) {
	return &auth.ExternalAPIKey{ID: "ek-1", InstallationID: params.InstallationID, Provider: params.Provider}, nil
}

func (f *handlerFakeExternalKeyRepo) GetForInstallation(context.Context, string) ([]*auth.ExternalAPIKey, error) {
	return nil, nil
}

func (f *handlerFakeExternalKeyRepo) SoftDeleteByProvider(_ context.Context, _, provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, provider)
	return nil
}

func (f *handlerFakeExternalKeyRepo) SoftDelete(context.Context, string, string) error { return nil }

func (f *handlerFakeExternalKeyRepo) UpdateModelAliases(_ context.Context, _, _ string, _ map[string]string) (*auth.ExternalAPIKey, error) {
	return nil, errors.New("not used")
}

func (f *handlerFakeExternalKeyRepo) MarkUsed(context.Context, string) error { return nil }

// aiandEngine mounts the five aiand auth routes the orchestrator will wire,
// with the installation injected the way middleware.WithAuth does.
func aiandEngine(svc *auth.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	inject := func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1", ExternalID: "ext-1"})
	}
	engine.POST("/admin/v1/aiand/device/start", inject, admin.StartAiandDeviceLoginHandler(svc))
	engine.POST("/admin/v1/aiand/device/poll", inject, admin.PollAiandDeviceLoginHandler(svc))
	engine.POST("/admin/v1/aiand/paste", inject, admin.PasteAiandKeyHandler(svc))
	engine.GET("/admin/v1/aiand/status", inject, admin.AiandAuthStatusHandler(svc))
	engine.POST("/admin/v1/aiand/disconnect", inject, admin.DisconnectAiandAuthHandler(svc))
	return engine
}

func newAiandService(gateway auth.AiandGateway) *auth.Service {
	external := &handlerFakeExternalKeyRepo{}
	return auth.NewService(nil, nil, external, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) }).
		WithEncryptor(auth.NoOpEncryptor{}).
		WithAiandGateway(gateway, &handlerFakeCredentialRepo{rows: map[string]*auth.AiandCredential{}}, "router-host")
}

func doJSON(t *testing.T, engine *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("response is not JSON: %q", rec.Body.String())
		}
	}
	return rec.Code, out
}

func TestStartAiandDeviceLoginHandler_ReturnsUserCodeContract(t *testing.T) {
	gateway := &handlerFakeGateway{startCode: auth.AiandDeviceCode{
		UserCode: "ABCD-1234", VerificationURI: "https://api.aiand.com/device", ExpiresIn: 900, Interval: 5,
	}}
	engine := aiandEngine(newAiandService(gateway))

	code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/start", "")

	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ABCD-1234", body["user_code"])
	assert.Equal(t, "https://api.aiand.com/device", body["verification_uri"])
	assert.InEpsilon(t, float64(900), body["expires_in"], 0)
	assert.InEpsilon(t, float64(5), body["interval"], 0)
}

func TestStartAiandDeviceLoginHandler_GatewayDownIs502(t *testing.T) {
	gateway := &handlerFakeGateway{startErr: auth.ErrAiandGateway}
	engine := aiandEngine(newAiandService(gateway))
	code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/start", "")
	assert.Equal(t, http.StatusBadGateway, code)
	assert.Equal(t, "aiand_gateway_unavailable", body["error"])
}

func TestPollAiandDeviceLoginHandler_Statuses(t *testing.T) {
	setup := func(pollErr error, token auth.AiandTokenResponse) (*gin.Engine, *handlerFakeGateway) {
		gateway := &handlerFakeGateway{
			startCode: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5},
			pollErr:   pollErr,
			pollToken: token,
		}
		engine := aiandEngine(newAiandService(gateway))
		code, _ := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/start", "")
		require.Equal(t, http.StatusOK, code)
		return engine, gateway
	}

	t.Run("pending", func(t *testing.T) {
		engine, _ := setup(auth.ErrAiandAuthorizationPending, auth.AiandTokenResponse{})
		code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/poll", "")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "pending", body["status"])
	})

	t.Run("authorized carries masked key", func(t *testing.T) {
		engine, _ := setup(nil, auth.AiandTokenResponse{
			AccessToken: "sk-aiand-123456789", RefreshToken: "rt", ExpiresIn: 60,
			Org: &auth.AiandOrg{ID: "org-1", Name: "Acme"},
		})
		code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/poll", "")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "authorized", body["status"])
		assert.Equal(t, "sk-aiand…6789", body["key_masked"])
		assert.Equal(t, "Acme", body["org_name"])
		assert.NotContains(t, body, "access_token")
	})

	t.Run("denied", func(t *testing.T) {
		engine, _ := setup(auth.ErrAiandAccessDenied, auth.AiandTokenResponse{})
		code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/poll", "")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "denied", body["status"])
	})

	t.Run("expired", func(t *testing.T) {
		engine, _ := setup(auth.ErrAiandExpiredToken, auth.AiandTokenResponse{})
		code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/poll", "")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "expired", body["status"])
	})
}

func TestPollAiandDeviceLoginHandler_SlowDownIs428WithRetryAfter(t *testing.T) {
	gateway := &handlerFakeGateway{
		startCode: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5},
		pollErr:   auth.ErrAiandSlowDown,
	}
	engine := aiandEngine(newAiandService(gateway))
	code, _ := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/start", "")
	require.Equal(t, http.StatusOK, code)

	req := httptest.NewRequest(http.MethodPost, "/admin/v1/aiand/device/poll", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusPreconditionRequired, rec.Code)
	assert.Equal(t, "10", rec.Header().Get("Retry-After"), "interval 5s + gateway-required 5s")
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "slow_down", body["error"])
	assert.NotContains(t, body, "retry_after_seconds", "Retry-After header is the single source for the raised interval")
}

// TestPollAiandDeviceLoginHandler_SlowDownIsNonTerminal walks the full poll
// sequence through the handler: pending, then a gateway slow-down (428, the
// dashboard keeps polling at the raised interval), then a successful
// authorization — proving a slow-down response does not end the login.
func TestPollAiandDeviceLoginHandler_SlowDownIsNonTerminal(t *testing.T) {
	gateway := &handlerFakeGateway{
		startCode: auth.AiandDeviceCode{DeviceCode: "dc-1", Interval: 5},
		pollScript: []pollStep{
			{err: auth.ErrAiandAuthorizationPending},
			{err: auth.ErrAiandSlowDown},
			{token: auth.AiandTokenResponse{
				AccessToken: "sk-aiand-123456789", RefreshToken: "rt", ExpiresIn: 60,
				Org: &auth.AiandOrg{ID: "org-1", Name: "Acme"},
			}},
		},
	}
	engine := aiandEngine(newAiandService(gateway))
	code, _ := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/start", "")
	require.Equal(t, http.StatusOK, code)

	poll := func() (int, map[string]any, http.Header) {
		req := httptest.NewRequest(http.MethodPost, "/admin/v1/aiand/device/poll", nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return rec.Code, body, rec.Header()
	}

	// First poll: still pending; the dashboard keeps its loop.
	code, body, _ := poll()
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "pending", body["status"])

	// Second poll: gateway slow-down. 428 + Retry-After only; the poll
	// loop must survive this response.
	code, body, header := poll()
	assert.Equal(t, http.StatusPreconditionRequired, code)
	assert.Equal(t, "slow_down", body["error"])
	assert.Equal(t, "10", header.Get("Retry-After"), "interval 5s + gateway-required 5s")
	assert.NotContains(t, body, "retry_after_seconds")

	// Third poll: authorization completes normally after the slow-down.
	code, body, _ = poll()
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "authorized", body["status"])
	assert.Equal(t, "sk-aiand…6789", body["key_masked"])
	assert.Equal(t, "Acme", body["org_name"])
	assert.Equal(t, 3, gateway.pollCalls, "three gateway polls exercised the full sequence")
}

func TestPollAiandDeviceLoginHandler_NoPendingLoginIs409(t *testing.T) {
	engine := aiandEngine(newAiandService(&handlerFakeGateway{}))
	code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/device/poll", "")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "no_pending_login", body["error"])
}

func TestPasteAiandKeyHandler(t *testing.T) {
	t.Run("valid key stores and returns masked summary", func(t *testing.T) {
		gateway := &handlerFakeGateway{validateUser: auth.AiandUser{ID: "u1", Email: "ops@example.com"}}
		engine := aiandEngine(newAiandService(gateway))
		code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/paste", `{"key": "sk-aiand-123456789"}`)
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "ops@example.com", body["email"])
		assert.Equal(t, "sk-aiand…6789", body["key_masked"])
	})

	t.Run("rejected key is 401 invalid_key", func(t *testing.T) {
		gateway := &handlerFakeGateway{validateErr: auth.ErrAiandInvalidKey}
		engine := aiandEngine(newAiandService(gateway))
		code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/paste", `{"key": "sk-bad"}`)
		assert.Equal(t, http.StatusUnauthorized, code)
		assert.Equal(t, "invalid_key", body["error"])
	})

	t.Run("missing key is 400", func(t *testing.T) {
		engine := aiandEngine(newAiandService(&handlerFakeGateway{}))
		code, _ := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/paste", `{}`)
		assert.Equal(t, http.StatusBadRequest, code)
	})

	t.Run("gateway outage is 502", func(t *testing.T) {
		gateway := &handlerFakeGateway{validateErr: auth.ErrAiandGateway}
		engine := aiandEngine(newAiandService(gateway))
		code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/paste", `{"key": "sk-fine"}`)
		assert.Equal(t, http.StatusBadGateway, code)
		assert.Equal(t, "aiand_gateway_unavailable", body["error"])
	})
}

func TestAiandAuthStatusHandler(t *testing.T) {
	t.Run("not connected", func(t *testing.T) {
		engine := aiandEngine(newAiandService(&handlerFakeGateway{}))
		code, body := doJSON(t, engine, http.MethodGet, "/admin/v1/aiand/status", "")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, false, body["connected"])
	})

	t.Run("connected hides the raw key", func(t *testing.T) {
		gateway := &handlerFakeGateway{validateUser: auth.AiandUser{Email: "ops@example.com"}}
		engine := aiandEngine(newAiandService(gateway))
		code, _ := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/paste", `{"key": "sk-aiand-abcdefgh"}`)
		require.Equal(t, http.StatusOK, code)

		code, body := doJSON(t, engine, http.MethodGet, "/admin/v1/aiand/status", "")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, true, body["connected"])
		assert.Equal(t, "ops@example.com", body["email"])
		assert.Equal(t, "paste", body["origin"])
		assert.Equal(t, "sk-aiand…efgh", body["key_masked"])
	})
}

func TestDisconnectAiandAuthHandler(t *testing.T) {
	external := &handlerFakeExternalKeyRepo{}
	svc := auth.NewService(nil, nil, external, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return time.Unix(0, 0) }).
		WithEncryptor(auth.NoOpEncryptor{}).
		WithAiandGateway(&handlerFakeGateway{validateUser: auth.AiandUser{Email: "ops@example.com"}},
			&handlerFakeCredentialRepo{rows: map[string]*auth.AiandCredential{}}, "router-host")
	engine := aiandEngine(svc)

	code, _ := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/paste", `{"key": "sk-1"}`)
	require.Equal(t, http.StatusOK, code)

	code, body := doJSON(t, engine, http.MethodPost, "/admin/v1/aiand/disconnect", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, body["disconnected"])

	code, body = doJSON(t, engine, http.MethodGet, "/admin/v1/aiand/status", "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, body["connected"])

	// The paste upsert itself soft-deleted the previous provider key once;
	// disconnect deletes it once more.
	assert.Equal(t, []string{providers.ProviderAIAND, providers.ProviderAIAND}, external.deleted,
		"disconnect must delete the propagated aiand provider key")
}

func TestAiandHandlers_UnauthenticatedRequestIs401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	svc := newAiandService(&handlerFakeGateway{})
	engine.POST("/admin/v1/aiand/paste", admin.PasteAiandKeyHandler(svc))
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/aiand/paste", strings.NewReader(`{"key": "sk-1"}`))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "no identity middleware, no access")
}
