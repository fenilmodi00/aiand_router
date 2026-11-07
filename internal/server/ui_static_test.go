package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterUIStatic covers clean-URL serving and the legacy settings
// redirects from the sidebar navigation migration
// (docs/plans/2026-11-07-sidebar-navigation-migration.md).
func TestRegisterUIStatic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("index"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "keys.html"), []byte("keys"), 0o644))

	engine := gin.New()
	registerUIStatic(engine, root)

	cases := []struct {
		name         string
		path         string
		wantCode     int
		wantLocation string
		wantBody     string
		wantContain  bool
	}{
		{
			name:         "legacy settings redirects to keys",
			path:         "/ui/settings",
			wantCode:     http.StatusPermanentRedirect,
			wantLocation: "/ui/keys",
		},
		{
			name:         "legacy settings keeps query",
			path:         "/ui/settings?tab=keys",
			wantCode:     http.StatusPermanentRedirect,
			wantLocation: "/ui/keys?tab=keys",
		},
		{
			name:         "legacy settings providers redirects to account",
			path:         "/ui/settings/providers",
			wantCode:     http.StatusPermanentRedirect,
			wantLocation: "/ui/account",
		},
		{
			name:         "legacy settings models redirects to models",
			path:         "/ui/settings/models",
			wantCode:     http.StatusPermanentRedirect,
			wantLocation: "/ui/models",
		},
		{
			name:         "trailing slash normalizes before legacy map",
			path:         "/ui/settings/",
			wantCode:     http.StatusPermanentRedirect,
			wantLocation: "/ui/settings",
		},
		{
			name:        "clean URL serves html file",
			path:        "/ui/keys",
			wantCode:    http.StatusOK,
			wantBody:    "keys",
			wantContain: true,
		},
		{
			name:     "unknown path 404s",
			path:     "/ui/nope",
			wantCode: http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)

			require.Equal(t, tc.wantCode, rec.Code, "GET %s", tc.path)
			if tc.wantLocation != "" {
				assert.Equal(t, tc.wantLocation, rec.Header().Get("Location"), "GET %s", tc.path)
			}
			if tc.wantBody != "" {
				if tc.wantContain {
					assert.Contains(t, rec.Body.String(), tc.wantBody, "GET %s", tc.path)
				} else {
					assert.Equal(t, tc.wantBody, rec.Body.String(), "GET %s", tc.path)
				}
			}
		})
	}
}
