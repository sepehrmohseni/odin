package gateway_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"odin/pkg/config"
	"odin/pkg/plugins"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildSO compiles the package in dir as a Go plugin with the same flags as this test binary,
// so the gateway can load it. It skips where plugins cannot be built or loaded.
func buildSO(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Go plugins are only tested on Linux")
	}
	if testing.CoverMode() != "" {
		t.Skip("coverage instrumentation changes package hashes, so a separately built plugin cannot load")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not available")
	}
	out := filepath.Join(t.TempDir(), filepath.Base(dir)+".so")
	args := []string{"build", "-buildmode=plugin", "-o", out}
	if raceEnabled {
		args = append(args, "-race")
	}
	args = append(args, dir)
	cmd := exec.Command(goBin, args...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "cgo") || strings.Contains(string(output), "gcc") {
			t.Skipf("cannot build Go plugins here: %s", output)
		}
		t.Fatalf("building the test plugin: %v\n%s", err, output)
	}
	return out
}

func TestUploadedStyleMiddlewarePluginRunsOnTheRequestPath(t *testing.T) {
	so := buildSO(t, "./testdata/apikey")

	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	gw, h := pipelineGateway(t, &config.Config{
		Plugins:  config.PluginsConfig{Directory: filepath.Dir(so)},
		Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{be.URL}}},
	})

	// What the admin's enable does for a middleware record.
	err := gw.Plugins().LoadRecord(&plugins.PluginRecord{
		Name:       "api-key",
		BinaryPath: so,
		PluginType: "middleware",
		Config:     map[string]interface{}{"key": "s3cret"},
		AppliedTo:  []string{"/api/*"},
		Phase:      "pre-auth",
	})
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnauthorized, serve(h, "GET", "/api/x", nil, nil).Code,
		"an enabled auth plugin enforces its check (it used to be loaded and never run)")
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/api/x", nil, map[string]string{"X-API-Key": "s3cret"}).Code)

	require.NoError(t, gw.Plugins().UnloadRecord("api-key"))
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/api/x", nil, nil).Code)

	// Static validation accepts the same binary without running it.
	assert.NoError(t, plugins.NewPluginValidator().ValidatePlugin(so, "api-key"))
}

// The shipped example exports `var Middleware` and is configured through Initialize; it must
// build with examples/middleware-plugins/build.sh's flags and enforce its keys once enabled.
func TestExampleAPIKeyPluginLoadsAndEnforces(t *testing.T) {
	so := buildSO(t, "../../../../examples/middleware-plugins/api-key-auth")

	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	gw, h := pipelineGateway(t, &config.Config{
		Plugins:  config.PluginsConfig{Directory: filepath.Dir(so)},
		Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{be.URL}}},
	})

	require.NoError(t, plugins.NewPluginValidator().ValidatePlugin(so, "api-key-auth"))
	require.NoError(t, gw.Plugins().LoadRecord(&plugins.PluginRecord{
		Name:       "api-key-auth",
		BinaryPath: so,
		PluginType: "middleware",
		Config:     map[string]interface{}{"keys": []interface{}{"k1", "k2"}},
		AppliedTo:  []string{"/api/*"},
		Phase:      "pre-auth",
	}))

	assert.Equal(t, http.StatusUnauthorized, serve(h, "GET", "/api/x", nil, map[string]string{"X-API-Key": "nope"}).Code)
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/api/x", nil, map[string]string{"X-API-Key": "k2"}).Code)
}
