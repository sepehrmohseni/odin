package gateway_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"odin/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAPIDocumentAndUI(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	_, h := pipelineGateway(t, &config.Config{
		OpenAPI: config.OpenAPIConfig{Enabled: true, Title: "Shop <API>", UIEnabled: true, UIPath: "/docs/"},
		Services: []config.ServiceConfig{
			{Name: "orders", BasePath: "/api/orders", Targets: []string{be.URL}},
		},
	})

	rec := serve(h, "GET", "/openapi.json", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var doc struct {
		Paths map[string]interface{} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Contains(t, doc.Paths, "/api/orders/{path}")
	assert.NotContains(t, rec.Body.String(), be.URL, "targets are not published")

	rec = serve(h, "GET", "/openapi.yaml", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, strings.HasPrefix(rec.Body.String(), "openapi: 3.0.3"))

	rec = serve(h, "GET", "/docs", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	assert.Contains(t, page, "<title>Shop &lt;API&gt;</title>")
	assert.Contains(t, page, `integrity="sha384-`)
	assert.Contains(t, page, `src="/docs/init.js"`)
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'none'")

	rec = serve(h, "GET", "/docs/init.js", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `url: "/openapi.json"`)
}

func TestOpenAPIIsOffByDefault(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	_, h := pipelineGateway(t, &config.Config{
		Services: []config.ServiceConfig{{Name: "orders", BasePath: "/api/orders", Targets: []string{be.URL}}},
	})
	assert.Equal(t, http.StatusNotFound, serve(h, "GET", "/openapi.json", nil, nil).Code)
	assert.Equal(t, http.StatusNotFound, serve(h, "GET", "/openapi", nil, nil).Code)
}
