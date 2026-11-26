package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"odin/pkg/auth"
	"odin/pkg/config"
	"odin/pkg/gateway"
	"odin/pkg/routing"
	"odin/pkg/service"
	"odin/pkg/servicemesh"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-signing-secret-0123456789"

func quietLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

// backend counts calls and echoes what it saw.
type backend struct {
	*httptest.Server
	calls atomic.Int64
}

func newBackend(t *testing.T, handler http.HandlerFunc) *backend {
	t.Helper()
	b := &backend{}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(b.Close)
	return b
}

// newGateway builds a gateway for cfg, with retries off unless a test sets them.
func newGateway(t *testing.T, cfg *config.Config) http.Handler {
	t.Helper()
	for i := range cfg.Services {
		cfg.Services[i].SetDefaults()
		cfg.Services[i].RetryCount = 0
	}
	return startGateway(t, cfg)
}

func startGateway(t *testing.T, cfg *config.Config) http.Handler {
	t.Helper()
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	gw, err := gateway.New(cfg, t.TempDir()+"/config.yaml", quietLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = gw.Shutdown(ctx)
	})
	return gw.Handler()
}

func serve(h http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func token(t *testing.T, claims jwt.Claims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	require.NoError(t, err)
	return s
}

func validToken(t *testing.T) string {
	s, err := auth.GenerateToken("u1", "alice", "user", testSecret, time.Hour)
	require.NoError(t, err)
	return s
}

func TestCacheNeverAnswersBeforeAuthentication(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "private data for "+r.Header.Get("Authorization"))
	})
	h := newGateway(t, &config.Config{
		Auth:  config.AuthConfig{JWTSecret: testSecret},
		Cache: config.CacheConfig{Enabled: true, TTL: time.Minute},
		Services: []config.ServiceConfig{{
			Name: "private", BasePath: "/private", Targets: []string{be.URL}, Authentication: true,
		}},
	})

	bearer := map[string]string{"Authorization": "Bearer " + validToken(t)}
	rec := serve(h, "GET", "/private/data", nil, bearer)
	require.Equal(t, http.StatusOK, rec.Code)

	// The old global cache served this exact response to anyone.
	rec = serve(h, "GET", "/private/data", nil, nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), "private data")

	// Requests with credentials are not cached at all.
	serve(h, "GET", "/private/data", nil, bearer)
	assert.Equal(t, int64(2), be.calls.Load())
}

func TestCacheServesPublicResponsesSafely(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cookie":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
		case "/nostore":
			w.Header().Set("Cache-Control", "no-store")
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	})
	h := newGateway(t, &config.Config{
		Cache: config.CacheConfig{Enabled: true, TTL: time.Minute},
		Services: []config.ServiceConfig{{
			Name: "public", BasePath: "/public", Targets: []string{be.URL}, StripBasePath: true,
		}},
	})

	first := serve(h, "GET", "/public/page", nil, nil)
	second := serve(h, "GET", "/public/page", nil, nil)
	assert.Equal(t, "MISS", first.Header().Get("X-Cache"))
	assert.Equal(t, "HIT", second.Header().Get("X-Cache"))
	assert.Equal(t, "hello /page", second.Body.String())
	assert.Equal(t, "text/plain", second.Header().Get("Content-Type"), "headers are kept")
	assert.Equal(t, int64(1), be.calls.Load())

	for _, path := range []string{"/public/cookie", "/public/nostore", "/public/error"} {
		before := be.calls.Load()
		serve(h, "GET", path, nil, nil)
		serve(h, "GET", path, nil, nil)
		assert.Equal(t, before+2, be.calls.Load(), "%s must not be cached", path)
	}

	// A different query string is a different entry.
	serve(h, "GET", "/public/page?x=1", nil, nil)
	assert.Equal(t, "MISS", serve(h, "GET", "/public/page?x=2", nil, nil).Header().Get("X-Cache"))
}

func TestServiceCanOptOutOfCaching(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "x") })
	off := false
	h := newGateway(t, &config.Config{
		Cache: config.CacheConfig{Enabled: true, TTL: time.Minute},
		Services: []config.ServiceConfig{{
			Name: "live", BasePath: "/live", Targets: []string{be.URL}, Cache: &config.ServiceCacheConfig{Enabled: &off},
		}},
	})
	serve(h, "GET", "/live/x", nil, nil)
	serve(h, "GET", "/live/x", nil, nil)
	assert.Equal(t, int64(2), be.calls.Load())
}

func TestJWTFailsClosed(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	svc := config.ServiceConfig{Name: "api", BasePath: "/api", Targets: []string{be.URL}, Authentication: true}

	// No secret configured: reject instead of verifying against an empty key.
	h := newGateway(t, &config.Config{Services: []config.ServiceConfig{svc}})
	emptyKeyToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(""))
	if err == nil {
		rec := serve(h, "GET", "/api/x", nil, map[string]string{"Authorization": "Bearer " + emptyKeyToken})
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	}
	assert.Equal(t, http.StatusServiceUnavailable, serve(h, "GET", "/api/x", nil, nil).Code)

	h = newGateway(t, &config.Config{
		Auth:     config.AuthConfig{JWTSecret: testSecret},
		Services: []config.ServiceConfig{svc},
	})
	cases := map[string]string{
		"no token":       "",
		"no expiry":      "Bearer " + token(t, jwt.MapClaims{"user_id": "u1"}),
		"expired":        "Bearer " + token(t, jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()}),
		"wrong secret":   "Bearer " + mustSign(t, jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()}, "other"),
		"alg none":       "Bearer " + noneToken(t),
		"basic scheme":   "Basic dXNlcjpwYXNz",
		"garbage":        "Bearer not.a.jwt",
		"missing secret": "Bearer ",
	}
	for name, header := range cases {
		headers := map[string]string{}
		if header != "" {
			headers["Authorization"] = header
		}
		assert.Equal(t, http.StatusUnauthorized, serve(h, "GET", "/api/x", nil, headers).Code, name)
	}
	assert.Equal(t, int64(0), be.calls.Load(), "no rejected request reaches the backend")

	rec := serve(h, "GET", "/api/x", nil, map[string]string{"Authorization": "bearer " + validToken(t)})
	assert.Equal(t, http.StatusOK, rec.Code, "the scheme is case-insensitive")
}

func mustSign(t *testing.T, claims jwt.Claims, secret string) string {
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return s
}

func noneToken(t *testing.T) string {
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)
	return s
}

func TestGraphQLServiceRequiresAuthentication(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"me":"alice"}}`)
	})
	h := newGateway(t, &config.Config{
		Auth: config.AuthConfig{JWTSecret: testSecret},
		Services: []config.ServiceConfig{{
			Name: "gql", BasePath: "/graphql", Targets: []string{be.URL}, Protocol: "graphql",
			Authentication: true, GraphQL: &config.GraphQLConfig{},
		}},
	})

	body := `{"query":"{ me }"}`
	rec := serve(h, "POST", "/graphql", strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, int64(0), be.calls.Load())

	rec = serve(h, "POST", "/graphql", strings.NewReader(body), map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + validToken(t),
	})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAggregateEnforcesPerServiceAuthentication(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) })
	h := newGateway(t, &config.Config{
		Auth: config.AuthConfig{JWTSecret: testSecret, IgnorePathRegexes: []string{"^/aggregate$"}},
		Services: []config.ServiceConfig{
			{Name: "private", BasePath: "/private", Targets: []string{be.URL}, Authentication: true},
			{Name: "public", BasePath: "/public", Targets: []string{be.URL}},
		},
	})

	var out struct {
		Results map[string]struct {
			Status int `json:"status"`
		} `json:"results"`
	}
	rec := serve(h, "GET", "/aggregate?services=private,public", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, http.StatusUnauthorized, out.Results["private"].Status)
	assert.Equal(t, http.StatusOK, out.Results["public"].Status)
	assert.Equal(t, int64(1), be.calls.Load(), "only the public service was called")

	rec = serve(h, "GET", "/aggregate?services=private", nil, map[string]string{"Authorization": "Bearer " + validToken(t)})
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, http.StatusOK, out.Results["private"].Status)

	many := strings.Repeat("public,", 10) + "a,b,c,d,e,f,g,h,i,j,k"
	assert.Equal(t, http.StatusBadRequest, serve(h, "GET", "/aggregate?services="+many, nil, nil).Code)
}

func TestDebugEndpointsAreOffByDefault(t *testing.T) {
	h := newGateway(t, &config.Config{})
	assert.Equal(t, http.StatusNotFound, serve(h, "GET", "/debug/routes", nil, nil).Code)
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/health", nil, nil).Code)

	h = newGateway(t, &config.Config{Server: config.ServerConfig{DebugEndpoints: true}})
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/debug/routes", nil, nil).Code)
}

func TestAdminIsNotMountedWhenDisabled(t *testing.T) {
	h := newGateway(t, &config.Config{})
	for _, path := range []string{"/admin/api/settings", "/admin/dashboard", "/admin/login"} {
		assert.Equal(t, http.StatusNotFound, serve(h, "GET", path, nil, nil).Code, path)
	}
}

func TestRequestBodyIsLimited(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body) })
	h := newGateway(t, &config.Config{
		Server:   config.ServerConfig{MaxBodySize: "1K"},
		Services: []config.ServiceConfig{{Name: "up", BasePath: "/up", Targets: []string{be.URL}}},
	})
	rec := serve(h, "POST", "/up/x", strings.NewReader(strings.Repeat("x", 4096)), nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestConsulTokenIsNotForwarded(t *testing.T) {
	mesh, err := servicemesh.NewConsulMesh(servicemesh.Config{
		Enabled: true,
		Type:    servicemesh.MeshTypeConsul,
		Consul:  &servicemesh.ConsulConfig{Token: "acl-secret", HTTPAddr: "http://127.0.0.1:1"},
	}, quietLogger())
	require.NoError(t, err)

	h := http.Header{}
	h.Set("X-Consul-Token", "client-sent")
	require.NoError(t, mesh.InjectHeaders(h))
	assert.Empty(t, h.Get("X-Consul-Token"))
}

func gatewayNew(t testing.TB, cfg *config.Config) (*gateway.Gateway, error) {
	t.Helper()
	return gateway.New(cfg, t.TempDir()+"/config.yaml", quietLogger())
}

// newRouterGateway serves one service.Config through the router, for options the YAML config
// does not reach.
func newRouterGateway(t *testing.T, svc *service.Config) http.Handler {
	t.Helper()
	e := echo.New()
	registry := service.NewRegistry(quietLogger())
	require.NoError(t, registry.Register(svc))
	require.NoError(t, routing.NewRouter(e, registry, quietLogger()).RegisterRoutes())
	return e
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err)
	return n
}
