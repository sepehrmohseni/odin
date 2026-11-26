package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"odin/pkg/admin"
	"odin/pkg/plugins"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type passThrough struct{ name string }

func (p *passThrough) Name() string                                  { return p.name }
func (p *passThrough) Version() string                               { return "1.2.3" }
func (p *passThrough) Initialize(map[string]interface{}) error       { return nil }
func (p *passThrough) Cleanup() error                                { return nil }
func (p *passThrough) Handle(next echo.HandlerFunc) echo.HandlerFunc { return next }

// The Middleware page's API is mounted and works on the live chain without MongoDB: it used
// to be defined but never mounted, and a phase change only touched the database record.
func TestMiddlewareChainAPI(t *testing.T) {
	pm := plugins.NewPluginManager(quietLogger())
	require.NoError(t, pm.RegisterMiddleware("api-key", &passThrough{"api-key"}, 10, []string{"/api/*"}, ""))

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("server:\n  port: 8080\n"), 0o600))
	e := echo.New()
	h := admin.New(baseConfig(), cfgPath, quietLogger())
	h.SetMiddlewareAPIHandler(pm, nil)
	h.Register(e)
	auth := basic("root", "s3cret-pass")

	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", auth)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	phaseOf := func() string {
		for _, en := range pm.GetMiddlewareChain() {
			if en.Name == "api-key" {
				return en.Phase
			}
		}
		return ""
	}

	rec := call(http.MethodGet, "/admin/api/middleware/chain", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var chain struct {
		Chain []struct {
			Name, Phase, Version string
			Priority             int
		} `json:"chain"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &chain))
	require.Len(t, chain.Chain, 1)
	assert.Equal(t, "post-auth", chain.Chain[0].Phase)
	assert.Equal(t, "1.2.3", chain.Chain[0].Version)

	assert.Equal(t, http.StatusOK, call(http.MethodPut, "/admin/api/middleware/api-key/phase", `{"phase":"pre-auth"}`).Code)
	assert.Equal(t, "pre-auth", phaseOf(), "the phase change applies to the running chain")
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPut, "/admin/api/middleware/api-key/phase", `{"phase":"post-route-ish"}`).Code)

	assert.Equal(t, http.StatusOK, call(http.MethodPut, "/admin/api/middleware/api-key/priority", `{"priority":5}`).Code)
	assert.Equal(t, 5, pm.GetMiddlewareChain()[0].Priority)
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPut, "/admin/api/middleware/api-key/priority", `{"priority":5000}`).Code)
	assert.Equal(t, http.StatusNotFound, call(http.MethodPut, "/admin/api/middleware/nope/priority", `{"priority":1}`).Code)

	assert.Equal(t, http.StatusOK, call(http.MethodDelete, "/admin/api/middleware/api-key/unregister", "").Code)
	assert.Empty(t, pm.GetMiddlewareChain())

	rec = call(http.MethodGet, "/admin/api/middleware/chain/stats", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, http.StatusConflict, call(http.MethodPost, "/admin/api/middleware/reload-all", "").Code, "plugin records need MongoDB")
	assert.NotEqual(t, http.StatusOK, call(http.MethodPost, "/admin/api/middleware/compile", `{}`).Code, "the compile stub is gone")
}
