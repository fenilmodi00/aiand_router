package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"aiand-os/router/internal/router"
	"github.com/stretchr/testify/assert"
)

func TestDirectRoutingModelSelection(t *testing.T) {
	for _, test := range []struct {
		name          string
		method        string
		path          string
		body          string
		model         router.RoutingModel
		selectsRouter bool
	}{
		{name: "implicit current classifier", method: http.MethodPost, path: "/v1/messages", body: `{}`, model: router.ModelAutomatic, selectsRouter: true},
		{name: "explicit current classifier", method: http.MethodPost, path: "/v1/responses", body: `{"model":"` + string(router.ModelAutomatic) + `"}`, model: router.ModelAutomatic, selectsRouter: true},
		{name: "new HMM", method: http.MethodPost, path: "/v1/chat/completions", body: `{"model":"` + string(router.ModelHMM) + `"}`, model: router.ModelHMM, selectsRouter: true},
		{name: "concrete model does not select a classifier", method: http.MethodPost, path: "/v1/responses", body: `{"model":"z-ai/glm-5.3"}`},
		{name: "Qwen", method: http.MethodPost, path: "/v1/messages", body: `{"model":"` + string(router.ModelQwen) + `"}`, model: router.ModelQwen, selectsRouter: true},
		{name: "inspection", method: http.MethodGet, path: "/v1/models", body: `{}`},
		{name: "prepare", method: http.MethodPost, path: "/v1/router/models/" + string(router.ModelQwen) + "/prepare", body: `{}`},
		{name: "unknown selector", method: http.MethodPost, path: "/v1/messages", body: `{"model":"aiand-unknown"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			model, selectsRouter := directRoutingModel(request, []byte(test.body))
			assert.Equal(t, test.model, model)
			assert.Equal(t, test.selectsRouter, selectsRouter)
		})
	}
}
