package gateway_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"odin/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildWASM compiles a Go package to a WebAssembly plugin (GOOS=wasip1, c-shared).
func buildWASM(t testing.TB, dir string) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go command not available")
	}
	out := filepath.Join(t.TempDir(), filepath.Base(dir)+".wasm")
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared", "-o", out, dir)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", dir, err, output)
	}
	return out
}

func TestWASMPluginOnTheRequestPath(t *testing.T) {
	wasmFile := buildWASM(t, "../../../../examples/wasm-plugins/api-key")

	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Consumer", r.Header.Get("X-Consumer"))
		w.Header().Set("X-Seen-Key", r.Header.Get("X-API-Key"))
		w.WriteHeader(http.StatusOK)
	})
	_, h := pipelineGateway(t, &config.Config{
		WASM: config.WASMConfig{
			Enabled:   true,
			PluginDir: filepath.Dir(wasmFile),
			Plugins: []config.WASMPluginConfig{{
				Name:    "api-key",
				Path:    filepath.Base(wasmFile),
				Enabled: true,
				Phase:   "pre-auth",
				Routes:  []string{"/api/*"},
				Timeout: 2 * time.Second,
				Config: map[string]interface{}{
					"keys": map[string]interface{}{"k-123": "mobile-app"},
				},
			}},
		},
		Services: []config.ServiceConfig{
			{Name: "api", BasePath: "/api", Targets: []string{be.URL}},
			{Name: "open", BasePath: "/open", Targets: []string{be.URL}},
		},
	})

	rec := serve(h, "GET", "/api/orders", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.JSONEq(t, `{"message":"missing or unknown API key"}`, rec.Body.String())
	assert.Equal(t, "ApiKey header=X-API-Key", rec.Header().Get("WWW-Authenticate"))
	assert.Equal(t, http.StatusUnauthorized, serve(h, "GET", "/api/orders", nil, map[string]string{"X-API-Key": "wrong"}).Code)

	rec = serve(h, "GET", "/api/orders", nil, map[string]string{"X-API-Key": "k-123"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "mobile-app", rec.Header().Get("X-Seen-Consumer"), "the plugin set a request header")
	assert.Empty(t, rec.Header().Get("X-Seen-Key"), "the plugin removed the key before the upstream")
	assert.Equal(t, "wasm-api-key", rec.Header().Get("X-Checked-By"), "the plugin added a response header")

	assert.Equal(t, http.StatusOK, serve(h, "GET", "/open/x", nil, nil).Code, "routes outside the plugin's are untouched")

	// More concurrent requests than instances: they queue for the pool and all succeed.
	var wg sync.WaitGroup
	codes := make([]int, 24)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = serve(h, "GET", "/api/orders", nil, map[string]string{"X-API-Key": "k-123"}).Code
		}(i)
	}
	wg.Wait()
	for _, c := range codes {
		assert.Equal(t, http.StatusOK, c)
	}
}

// A plugin that never returns is interrupted at its timeout (it was not: AUDIT SEC-28), and
// the next request gets a fresh instance.
func TestWASMPluginTimeoutIsEnforced(t *testing.T) {
	wasmFile := buildWASM(t, "./testdata/wasmspin")
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	plugin := func(name, route string, failOpen bool) config.WASMPluginConfig {
		return config.WASMPluginConfig{Name: name, Path: wasmFile, Enabled: true, Routes: []string{route}, Timeout: 50 * time.Millisecond, FailOpen: failOpen}
	}
	_, h := pipelineGateway(t, &config.Config{
		WASM: config.WASMConfig{Enabled: true, MaxInstances: 2, Plugins: []config.WASMPluginConfig{
			plugin("spin", "/api/*", false),
			plugin("spin-open", "/lenient/*", true),
		}},
		Services: []config.ServiceConfig{
			{Name: "api", BasePath: "/api", Targets: []string{be.URL}},
			{Name: "lenient", BasePath: "/lenient", Targets: []string{be.URL}},
		},
	})

	for i := 0; i < 3; i++ {
		start := time.Now()
		rec := serve(h, "GET", "/api/x", nil, nil)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Less(t, time.Since(start), 3*time.Second, "interrupted at the timeout")
	}
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/lenient/x", nil, nil).Code, "failOpen lets the request through")
}

func TestWASMPluginThatCannotLoadStopsStartup(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.wasm")
	require.NoError(t, os.WriteFile(bad, []byte("not webassembly"), 0o600))
	cfg := &config.Config{
		Server: config.ServerConfig{Port: 8080},
		WASM:   config.WASMConfig{Enabled: true, Plugins: []config.WASMPluginConfig{{Name: "bad", Path: bad, Enabled: true}}},
	}
	_, err := gatewayNew(t, cfg)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "bad"), err.Error())

	missing := &config.Config{
		Server: config.ServerConfig{Port: 8080},
		WASM:   config.WASMConfig{Enabled: true, Plugins: []config.WASMPluginConfig{{Name: "gone", Path: "/nonexistent.wasm", Enabled: true}}},
	}
	_, err = gatewayNew(t, missing)
	assert.Error(t, err)
}

// BenchmarkWASMPlugin measures a proxied request through the API key plugin against the same
// request with no plugin:
//
//	scripts/docker-check.sh -- go test -run x -bench WASMPlugin -benchmem ./test/unit/pkg/gateway/
func BenchmarkWASMPlugin(b *testing.B) {
	wasmFile := buildWASM(b, "../../../../examples/wasm-plugins/api-key")
	be := newBackend(b, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, withPlugin := range []bool{false, true} {
		cfg := &config.Config{
			Server:   config.ServerConfig{Port: 8080},
			Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{be.URL}}},
		}
		if withPlugin {
			cfg.WASM = config.WASMConfig{Enabled: true, Plugins: []config.WASMPluginConfig{{
				Name: "api-key", Path: wasmFile, Enabled: true, Routes: []string{"/api/*"},
				Config: map[string]interface{}{"keys": map[string]interface{}{"k-123": "app"}},
			}}}
		}
		gw, err := gatewayNew(b, cfg)
		require.NoError(b, err)
		h := gw.Handler()
		name := "plugin=none"
		if withPlugin {
			name = "plugin=wasm-api-key"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if rec := serve(h, "GET", "/api/x", nil, map[string]string{"X-API-Key": "k-123"}); rec.Code != http.StatusOK {
					b.Fatalf("status %d", rec.Code)
				}
			}
		})
	}
}
