package admin_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminPagesRenderTheirOwnContent(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())
	auth := map[string]string{"Authorization": basic("root", "s3cret-pass")}

	pages := map[string]string{
		"/admin/dashboard":        "<title>Odin API Gateway - Dashboard</title>",
		"/admin/settings":         "<title>Gateway Settings - Odin Admin</title>",
		"/admin/traces":           "<title>Distributed Traces - Odin Admin</title>",
		"/admin/monitoring":       "<title>Real-time Monitoring - Odin Admin</title>",
		"/admin/middleware-chain": "<title>Middleware Chain - Odin Admin</title>",
		"/admin/services/new":     "<title>Add Service - Odin Admin</title>",
	}
	for path, title := range pages {
		rec := do(e, http.MethodGet, path, "", auth)
		require.Equal(t, http.StatusOK, rec.Code, path)
		body := rec.Body.String()
		assert.Contains(t, body, title, "%s renders its own page (every page used to show the traces page)", path)
		if path != "/admin/traces" {
			assert.NotContains(t, body, "Distributed Traces - Odin Admin", path)
		}
	}

	rec := do(e, http.MethodGet, "/admin/login", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, strings.ToLower(rec.Body.String()), "password")
}

func TestAdminRendererServesPluginPages(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())
	require.NotNil(t, e.Renderer, "the plugin pages call c.Render, which needs a renderer")

	for _, page := range []string{"plugins.html", "plugin_new.html", "plugin-binaries.html", "plugin-binary-upload.html"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/x", nil)
		rec := httptest.NewRecorder()
		var out strings.Builder
		err := e.Renderer.Render(&out, page, map[string]interface{}{"Plugins": []interface{}{}}, e.NewContext(req, rec))
		require.NoError(t, err, page)
		assert.NotEmpty(t, strings.TrimSpace(out.String()), page)
	}
}
