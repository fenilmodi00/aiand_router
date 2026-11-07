package aiandauth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"aiand-os/router/internal/aiandauth"
	"aiand-os/router/internal/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityGateway serves the three identity reads VerifyKey performs and
// records each call so the header/fallback contract is observable.
type identityGateway struct {
	calls      []string
	headStatus int
	headOrg    string
	getOrg     string
	orgs       string
	userStatus int
	orgsStatus int
}

func (g *identityGateway) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.calls = append(g.calls, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test-key" {
			t.Errorf("%s %s Authorization = %q", r.Method, r.URL.Path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user":
			if g.userStatus != 0 {
				w.WriteHeader(g.userStatus)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"user-1","email":"ops@example.com"}`))
		case "/api/orgs":
			if g.orgsStatus != 0 {
				w.WriteHeader(g.orgsStatus)
				_, _ = w.Write([]byte(`{"error":"gateway_down"}`))
				return
			}
			_, _ = w.Write([]byte(g.orgs))
		case "/v1/models":
			switch r.Method {
			case http.MethodHead:
				if g.headStatus != 0 {
					w.WriteHeader(g.headStatus)
					return
				}
				if g.headOrg != "" {
					w.Header().Set("X-Org-Id", g.headOrg)
				}
				w.WriteHeader(http.StatusOK)
			case http.MethodGet:
				if g.getOrg != "" {
					w.Header().Set("X-Org-Id", g.getOrg)
				}
				_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newIdentityClient(t *testing.T, g *identityGateway) *aiandauth.Client {
	t.Helper()
	server := g.serve(t)
	return aiandauth.NewClient(server.URL, server.Client())
}

// A multi-org account must use the key's own org from the gateway header, not
// the first membership. This is the regression for paste vs device divergence.
func TestVerifyKey_UsesKeyScopedOrgOverMembershipOrder(t *testing.T) {
	g := &identityGateway{
		headOrg: "org-2",
		orgs:    `[{"id":"org-1","name":"First"},{"id":"org-2","name":"Second"}]`,
	}
	identity, err := newIdentityClient(t, g).VerifyKey(context.Background(), "sk-test-key")
	require.NoError(t, err)

	assert.Equal(t, auth.AiandIdentity{
		UserID:  "user-1",
		OrgID:   "org-2",
		Email:   "ops@example.com",
		OrgName: "Second",
	}, identity)
	assert.Contains(t, g.calls, "HEAD /v1/models")
}

func TestVerifyKey_NoHeaderUsesSoleMembership(t *testing.T) {
	g := &identityGateway{
		orgs:   `[{"id":"org-9","name":"Only Org"}]`,
		getOrg: "",
	}
	identity, err := newIdentityClient(t, g).VerifyKey(context.Background(), "sk-test-key")
	require.NoError(t, err)
	assert.Equal(t, "org-9", identity.OrgID)
	assert.Equal(t, "Only Org", identity.OrgName)
	assert.Contains(t, g.calls, "GET /v1/models", "GET probes the header when HEAD has none")
}

func TestVerifyKey_NoHeaderAndMultipleOrgsFailsClosed(t *testing.T) {
	g := &identityGateway{
		orgs: `[{"id":"org-1","name":"First"},{"id":"org-2","name":"Second"}]`,
	}
	_, err := newIdentityClient(t, g).VerifyKey(context.Background(), "sk-test-key")
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrAiandOrgUnresolved)
}

func TestVerifyKey_HeadUnsupportedFallsBackToGET(t *testing.T) {
	g := &identityGateway{
		headStatus: http.StatusMethodNotAllowed,
		getOrg:     "org-3",
		orgs:       `[{"id":"org-3","name":"Third"}]`,
	}
	identity, err := newIdentityClient(t, g).VerifyKey(context.Background(), "sk-test-key")
	require.NoError(t, err)
	assert.Equal(t, "org-3", identity.OrgID)
	assert.Equal(t, "Third", identity.OrgName)
	assert.Contains(t, g.calls, "HEAD /v1/models")
	assert.Contains(t, g.calls, "GET /v1/models")
}

func TestVerifyKey_RejectedKeyIs401AndStops(t *testing.T) {
	g := &identityGateway{userStatus: http.StatusUnauthorized}
	_, err := newIdentityClient(t, g).VerifyKey(context.Background(), "sk-test-key")
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrAiandInvalidKey)
	assert.Equal(t, []string{"GET /api/user"}, g.calls, "a rejected key must not probe further")
}

// The org id is already authoritative from the header; a membership-list
// outage only costs the display name.
func TestVerifyKey_OrgNameLookupFailureDoesNotFailLogin(t *testing.T) {
	g := &identityGateway{
		headOrg:    "org-4",
		orgsStatus: http.StatusBadGateway,
	}
	identity, err := newIdentityClient(t, g).VerifyKey(context.Background(), "sk-test-key")
	require.NoError(t, err)
	assert.Equal(t, "org-4", identity.OrgID)
	assert.Empty(t, identity.OrgName)
}
