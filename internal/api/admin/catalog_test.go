package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"aiand-os/router/internal/api/admin"
	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogModelsHandler_SortsByProviderThenModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	src := &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND},
		{Model: "z-ai/glm-5.3-flash", Provider: providers.ProviderAIAND},
		{Model: "deepseek/deepseek-v4.1-flash", Provider: providers.ProviderAIAND},
		{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderAIAND},
	}}

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(src))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/models", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var got admin.CatalogModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	require.Len(t, got.Models, 4)
	assert.Equal(t, providers.ProviderAIAND, got.Models[0].Provider)
	assert.Equal(t, "deepseek/deepseek-v4-pro", got.Models[0].Model)
	assert.Equal(t, providers.ProviderAIAND, got.Models[1].Provider)
	assert.Equal(t, "deepseek/deepseek-v4.1-flash", got.Models[1].Model)
	assert.Equal(t, providers.ProviderAIAND, got.Models[2].Provider)
	assert.Equal(t, "z-ai/glm-5.3", got.Models[2].Model)
	assert.Equal(t, providers.ProviderAIAND, got.Models[3].Provider)
	assert.Equal(t, "z-ai/glm-5.3-flash", got.Models[3].Model)
}

func TestCatalogModelsHandler_ServesCatalogPricesPerRow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(&fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "deepseek/deepseek-v4.1-flash", Provider: providers.ProviderAIAND},
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/models?strategy=hmm", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"models":[{"model":"deepseek/deepseek-v4.1-flash","provider":"aiand","input_usd_per_1m":0.3,"output_usd_per_1m":0.6}]}`, rec.Body.String())
}

func TestCatalogModelsHandler_EmptyListReturnsEmptyArray(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(&fakeHMMRoster{}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/models", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// Empty slice must round-trip as [], not null — the Aiand control plane
	// distinguishes "no models" from "missing field".
	assert.JSONEq(t, `{"models":[]}`, rec.Body.String())
}

type fakeHMMRoster struct {
	entries []router.DeployedEntry
	err     error
	calls   int
}

func (f *fakeHMMRoster) HMMDeployedModels(context.Context) ([]router.DeployedEntry, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

func TestCatalogModelsHandler_ServesHMMRosterForEveryStrategy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hmmSrc := &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND},
		{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderAIAND},
	}}

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(hmmSrc))

	for _, query := range []string{"", "?strategy=hmm", "?strategy=hmm_embedding", "?strategy=cluster"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/models"+query, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, query)
		var got admin.CatalogModelsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Len(t, got.Models, 2, query)
		assert.Equal(t, "deepseek/deepseek-v4-pro", got.Models[0].Model, query)
		assert.Equal(t, "z-ai/glm-5.3", got.Models[1].Model, query)
	}
	assert.Equal(t, 4, hmmSrc.calls, "the default universe must come from the HMM roster, not a legacy scorer")
}

// The Aiand control plane decodes this body into {model, provider} rows with
// optional per-1M prices to validate exclusions; renaming or adding a field
// breaks that client.
func TestCatalogModelsHandler_WireShapeContract(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hmmSrc := &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND},
		{Model: "deepseek/deepseek-v4-pro", Provider: providers.ProviderAIAND},
	}}
	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(hmmSrc))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/models", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string][]map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body, 1, "top level carries only models")
	rows := body["models"]
	require.Len(t, rows, 2)
	for _, row := range rows {
		assert.IsType(t, "", row["model"])
		assert.IsType(t, "", row["provider"])
		for key := range row {
			assert.Contains(t, []string{"model", "provider", "input_usd_per_1m", "output_usd_per_1m"}, key, "unexpected wire field %q", key)
		}
	}
	assert.Equal(t, "deepseek/deepseek-v4-pro", rows[0]["model"])
	assert.Equal(t, "aiand", rows[0]["provider"])
	assert.Equal(t, "z-ai/glm-5.3", rows[1]["model"])
	assert.Equal(t, "aiand", rows[1]["provider"])
}

func TestCatalogModelsHandler_HMMRosterErrorReturns503(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hmmSrc := &fakeHMMRoster{err: errors.New("sidecar unavailable")}

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(hmmSrc))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/models?strategy=hmm_embedding", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestCatalogModelsHandler_ScopeCatalogReturnsFullCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hmmSrc := &fakeHMMRoster{entries: []router.DeployedEntry{
		{Model: "z-ai/glm-5.3", Provider: providers.ProviderAIAND},
	}}

	engine := gin.New()
	engine.GET("/v1/router/models", admin.CatalogModelsHandler(hmmSrc))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/router/models?scope=catalog&strategy=hmm", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, hmmSrc.calls, "scope=catalog must not consult the HMM roster")

	var got admin.CatalogModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Greater(t, len(got.Models), 1, "full catalog is larger than the 1-entry roster fixture")

	seen := map[string]struct{}{}
	for _, m := range got.Models {
		require.NotEmpty(t, m.Model)
		require.NotEmpty(t, m.Provider)
		_, dup := seen[m.Model]
		require.False(t, dup, "duplicate catalog id %s", m.Model)
		seen[m.Model] = struct{}{}
	}
	_, hasOpus := seen["z-ai/glm-5.3"]
	require.True(t, hasOpus, "catalog must include a known Models row")
}
