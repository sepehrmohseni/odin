package gateway_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"odin/pkg/config"
	"odin/pkg/gateway"
	"odin/pkg/plugins"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// funcMiddleware is an in-process middleware plugin.
type funcMiddleware struct {
	name   string
	handle func(next echo.HandlerFunc) echo.HandlerFunc
}

func (m *funcMiddleware) Name() string                            { return m.name }
func (m *funcMiddleware) Version() string                         { return "test" }
func (m *funcMiddleware) Initialize(map[string]interface{}) error { return nil }
func (m *funcMiddleware) Handle(next echo.HandlerFunc) echo.HandlerFunc {
	return m.handle(next)
}
func (m *funcMiddleware) Cleanup() error { return nil }

func mw(name string, handle func(next echo.HandlerFunc) echo.HandlerFunc) *funcMiddleware {
	return &funcMiddleware{name: name, handle: handle}
}

func pipelineGateway(t *testing.T, cfg *config.Config) (*gateway.Gateway, http.Handler) {
	t.Helper()
	for i := range cfg.Services {
		cfg.Services[i].SetDefaults()
		cfg.Services[i].RetryCount = -1
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	gw, err := gatewayNew(t, cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = gw.Shutdown(ctx)
	})
	h := gw.Handler()
	return gw, h
}

func TestPreAuthPluginRunsBeforeJWT(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	gw, h := pipelineGateway(t, &config.Config{
		Auth:     config.AuthConfig{JWTSecret: testSecret},
		Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{be.URL}, Authentication: true}},
	})

	// An API-key plugin in the pre-auth phase answers before the JWT check.
	require.NoError(t, gw.Plugins().RegisterMiddleware("api-key", mw("api-key", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get("X-API-Key") != "letmein" {
				return echo.NewHTTPError(http.StatusForbidden, "bad api key")
			}
			return next(c)
		}
	}), 10, []string{"/api/*"}, "pre-auth"))

	assert.Equal(t, http.StatusForbidden, serve(h, "GET", "/api/x", nil, nil).Code,
		"the plugin answers first (the JWT check would have said 401)")
	assert.Equal(t, http.StatusUnauthorized, serve(h, "GET", "/api/x", nil, map[string]string{"X-API-Key": "letmein"}).Code,
		"with the key, the request continues to the JWT check")
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/api/x", nil, map[string]string{
		"X-API-Key": "letmein", "Authorization": "Bearer " + validToken(t),
	}).Code)
}

func TestPostAuthPluginSeesTheUserAndRunsInPriorityOrder(t *testing.T) {
	var seenUser, seenOrder string
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		seenUser, seenOrder = r.Header.Get("X-User"), r.Header.Get("X-Order")
	})
	gw, h := pipelineGateway(t, &config.Config{
		Auth:     config.AuthConfig{JWTSecret: testSecret},
		Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{be.URL}, Authentication: true}},
	})
	appendOrder := func(tag string) func(echo.HandlerFunc) echo.HandlerFunc {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				prev := c.Request().Header.Get("X-Order")
				c.Request().Header.Set("X-Order", strings.TrimPrefix(prev+","+tag, ","))
				return next(c)
			}
		}
	}
	pm := gw.Plugins()
	require.NoError(t, pm.RegisterMiddleware("late", mw("late", appendOrder("late")), 50, []string{"*"}, "post-auth"))
	require.NoError(t, pm.RegisterMiddleware("early", mw("early", appendOrder("early")), 5, []string{"*"}, ""))
	require.NoError(t, pm.RegisterMiddleware("user", mw("user", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if u, ok := c.Get("user_id").(string); ok {
				c.Request().Header.Set("X-User", u)
			}
			return next(c)
		}
	}), 20, []string{"/api/*"}, "post-auth"))

	rec := serve(h, "GET", "/api/x", nil, map[string]string{"Authorization": "Bearer " + validToken(t)})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "u1", seenUser, "post-auth plugins run after the JWT check")
	assert.Equal(t, "early,late", seenOrder, "lower priority runs first")
}

func TestPreProxyPluginRunsAfterTheCache(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "body") })
	gw, h := pipelineGateway(t, &config.Config{
		Cache:    config.CacheConfig{Enabled: true, TTL: time.Minute},
		Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{be.URL}}},
	})
	var calls atomic.Int64
	require.NoError(t, gw.Plugins().RegisterMiddleware("count", mw("count", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			calls.Add(1)
			return next(c)
		}
	}), 0, []string{"/*"}, "pre-proxy"))

	serve(h, "GET", "/api/x", nil, nil)
	second := serve(h, "GET", "/api/x", nil, nil)
	assert.Equal(t, "HIT", second.Header().Get("X-Cache"))
	assert.Equal(t, int64(1), calls.Load(), "a cache hit never reaches pre-proxy plugins")
}

func TestPluginsApplyToMatchingRoutesAndCanBeToggledLive(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	gw, h := pipelineGateway(t, &config.Config{Services: []config.ServiceConfig{
		{Name: "a", BasePath: "/api/a", Targets: []string{be.URL}},
		{Name: "ab", BasePath: "/api/ab", Targets: []string{be.URL}},
	}})
	block := mw("block", func(echo.HandlerFunc) echo.HandlerFunc {
		return func(echo.Context) error { return echo.NewHTTPError(http.StatusTeapot) }
	})
	pm := gw.Plugins()

	require.NoError(t, pm.RegisterMiddleware("block", block, 0, []string{"/api/a/*"}, "post-auth"))
	assert.Equal(t, http.StatusTeapot, serve(h, "GET", "/api/a/x", nil, nil).Code)
	assert.Equal(t, http.StatusTeapot, serve(h, "GET", "/api/a", nil, nil).Code)
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/api/ab/x", nil, nil).Code, "/api/a/* does not match /api/ab")
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/health", nil, nil).Code, "gateway endpoints are never affected")

	require.NoError(t, pm.UnregisterMiddleware("block"))
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/api/a/x", nil, nil).Code, "disabling takes effect immediately")
}

func TestPanickingPluginFailsOnlyItsRequest(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	gw, h := pipelineGateway(t, &config.Config{Services: []config.ServiceConfig{
		{Name: "api", BasePath: "/api", Targets: []string{be.URL}},
	}})
	require.NoError(t, gw.Plugins().RegisterMiddleware("boom", mw("boom", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.QueryParam("panic") != "" {
				panic("plugin bug")
			}
			return next(c)
		}
	}), 0, []string{"*"}, "post-auth"))

	assert.Equal(t, http.StatusInternalServerError, serve(h, "GET", "/api/x?panic=1", nil, nil).Code)
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/api/x", nil, nil).Code)
}

func TestPhaseNames(t *testing.T) {
	for in, want := range map[string]plugins.Phase{
		"":             plugins.PhasePostAuth,
		"pre-auth":     plugins.PhasePreAuth,
		"pre-routing":  plugins.PhasePreAuth, // stored by earlier versions
		"post-route":   plugins.PhasePostAuth,
		"pre-proxy":    plugins.PhasePreProxy,
		"Pre-Response": plugins.PhasePreProxy,
	} {
		got, err := plugins.NormalizePhase(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := plugins.NormalizePhase("sometime")
	assert.Error(t, err)

	gw, _ := pipelineGateway(t, &config.Config{})
	assert.Error(t, gw.Plugins().RegisterMiddleware("x", mw("x", nil), 0, nil, "sometime"))
}

// hookPlugin records which hooks ran.
type hookPlugin struct {
	mu    sync.Mutex
	calls []string
	deny  bool
}

func (p *hookPlugin) record(h string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, h)
}
func (p *hookPlugin) Name() string                            { return "hooks" }
func (p *hookPlugin) Version() string                         { return "test" }
func (p *hookPlugin) Initialize(map[string]interface{}) error { return nil }
func (p *hookPlugin) PreRequest(context.Context, *plugins.PluginContext) error {
	p.record("pre-request")
	if p.deny {
		return echo.NewHTTPError(http.StatusUnauthorized, "denied by plugin")
	}
	return nil
}
func (p *hookPlugin) PostRequest(_ context.Context, c *plugins.PluginContext) error {
	p.record("post-request:" + c.ServiceName)
	return nil
}
func (p *hookPlugin) PreResponse(context.Context, *plugins.PluginContext) error {
	p.record("pre-response")
	return nil
}
func (p *hookPlugin) PostResponse(context.Context, *plugins.PluginContext) error {
	p.record("post-response")
	return nil
}
func (p *hookPlugin) Cleanup() error { return nil }

func TestAllFourHooksRunAndPluginErrorsKeepTheirStatus(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	gw, h := pipelineGateway(t, &config.Config{
		Plugins:  config.PluginsConfig{Enabled: true},
		Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{be.URL}}},
	})
	hp := &hookPlugin{}
	require.NoError(t, gw.Plugins().RegisterPlugin("hooks", hp,
		[]string{"pre-request", "post-request", "pre-response", "post-response"}))
	assert.Error(t, gw.Plugins().RegisterPlugin("hooks", hp, nil), "names are unique")

	require.Equal(t, http.StatusOK, serve(h, "GET", "/api/x", nil, nil).Code)
	assert.Equal(t, []string{"pre-request", "post-request:api", "pre-response", "post-response"}, hp.calls,
		"post-request and pre-response used to never run")

	hp.deny = true
	assert.Equal(t, http.StatusUnauthorized, serve(h, "GET", "/api/x", nil, nil).Code,
		"a plugin's HTTP error keeps its status (it used to become 500)")

	require.NoError(t, gw.Plugins().UnloadPlugin("hooks"))
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/api/x", nil, nil).Code)
}

func TestChainIsSafeUnderConcurrentChanges(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	gw, h := pipelineGateway(t, &config.Config{Services: []config.ServiceConfig{
		{Name: "api", BasePath: "/api", Targets: []string{be.URL}},
	}})
	pass := mw("pass", func(next echo.HandlerFunc) echo.HandlerFunc { return next })

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					serve(h, "GET", "/api/x", nil, nil)
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		_ = gw.Plugins().RegisterMiddleware("pass", pass, i%7, []string{"*"}, "post-auth")
		_ = gw.Plugins().UnregisterMiddleware("pass")
	}
	close(stop)
	wg.Wait()
	// The race detector checks the concurrent reads and writes; the chain must end empty.
	assert.Empty(t, gw.Plugins().GetMiddlewareChain())
}
