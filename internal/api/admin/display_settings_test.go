package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aiand-os/router/internal/api/admin"
	"aiand-os/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// fakeExperimentChecker stands in for the auth service's installation experiment lookup.
type fakeExperimentChecker struct{ enabled bool }

func (f fakeExperimentChecker) InstallationBlindExperimentEnabled(context.Context, string) bool {
	return f.enabled
}

// TestDisplaySettingsHandler covers the flag passthrough and unauthenticated rejection.
func TestDisplaySettingsHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("reports the installation flag", func(t *testing.T) {
		for _, hide := range []bool{true, false} {
			engine := gin.New()
			engine.GET("/v1/display-settings", func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{ID: "inst-1", HideTerminalSurfaces: hide})
			}, admin.DisplaySettingsHandlerFor(nil, nil))

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, hide, body.HideTerminalSurfaces)
		}
	})

	t.Run("hides surfaces for an active blind experiment subject in either arm", func(t *testing.T) {
		for _, state := range []auth.BlindExperimentState{
			{Active: true, Arm: auth.BlindExperimentArmRouterOn},
			{Active: true, Arm: auth.BlindExperimentArmPassthrough},
			{Active: false, Arm: auth.BlindExperimentArmRouterOn},
		} {
			engine := gin.New()
			engine.GET("/v1/display-settings", func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{ID: "inst-1"})
			}, admin.DisplaySettingsHandlerFor(func(c *gin.Context) context.Context {
				return context.WithValue(c.Request.Context(), auth.BlindExperimentContextKey{}, state)
			}, nil))

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, state.Active, body.HideTerminalSurfaces, "arm %s active %v", state.Arm, state.Active)
		}
	})

	t.Run("hides unidentified callers while the installation runs an experiment", func(t *testing.T) {
		for _, tc := range []struct {
			name              string
			userID            string
			experimentEnabled bool
			wantHidden        bool
		}{
			{name: "unidentified caller, experiment enabled", experimentEnabled: true, wantHidden: true},
			{name: "unidentified caller, no experiment"},
			{name: "identified non-subject, experiment enabled", userID: "user-1", experimentEnabled: true},
		} {
			engine := gin.New()
			engine.GET("/v1/display-settings", func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{ID: "inst-1"})
			}, admin.DisplaySettingsHandlerFor(func(c *gin.Context) context.Context {
				if tc.userID == "" {
					return c.Request.Context()
				}
				return context.WithValue(c.Request.Context(), auth.UserIDContextKey{}, tc.userID)
			}, fakeExperimentChecker{enabled: tc.experimentEnabled}))

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, tc.wantHidden, body.HideTerminalSurfaces, tc.name)
		}
	})

	t.Run("unauthenticated without an installation", func(t *testing.T) {
		engine := gin.New()
		engine.GET("/v1/display-settings", admin.DisplaySettingsHandlerFor(nil, nil))

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}
