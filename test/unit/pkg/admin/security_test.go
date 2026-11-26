package admin_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"odin/pkg/admin"
	"odin/pkg/config"

	gws "github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// The admin loads its templates relative to the working directory, so run from the repo root.
func TestMain(m *testing.M) {
	if err := os.Chdir(filepath.Join("..", "..", "..", "..")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func quietLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(&strings.Builder{})
	return l
}

func newAdmin(t *testing.T, cfg *config.Config) (*echo.Echo, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("server:\n  port: 8080\n"), 0o600))

	e := echo.New()
	h := admin.New(cfg, cfgPath, quietLogger())
	h.Register(e)
	return e, cfgPath
}

func baseConfig() *config.Config {
	return &config.Config{
		Admin: config.AdminConfig{Enabled: true, Username: "root", Password: "s3cret-pass"},
		Auth:  config.AuthConfig{JWTSecret: "jwt-signing-secret"},
		MongoDB: config.MongoDBConfig{
			URI:  "mongodb://odin:mongo-pw@db:27017",
			Auth: config.MongoDBAuth{Password: "mongo-pw"},
		},
		Cache: config.CacheConfig{RedisURL: "redis://:redis-pw@cache:6379"},
	}
}

func do(e *echo.Echo, method, target string, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func login(e *echo.Echo, user, pass string) *httptest.ResponseRecorder {
	form := url.Values{"username": {user}, "password": {pass}}.Encode()
	return do(e, http.MethodPost, "/admin/login", form, nil)
}

func TestAdminRequiresAuthentication(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())

	rec := do(e, http.MethodGet, "/admin/api/settings", "", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = do(e, http.MethodGet, "/admin/dashboard", "", nil)
	assert.Equal(t, http.StatusSeeOther, rec.Code, "browsers are sent to the login page")
	assert.Equal(t, "/admin/login", rec.Header().Get("Location"))
}

func TestOldBase64CookieIsNotAccepted(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())
	// Earlier versions stored base64(user:pass) in an "Authorization" cookie.
	value := base64.StdEncoding.EncodeToString([]byte("root:s3cret-pass"))
	rec := do(e, http.MethodGet, "/admin/api/settings", "", map[string]string{"Cookie": "Authorization=" + value})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestNoBuiltInDefaultPassword(t *testing.T) {
	cfg := baseConfig()
	cfg.Admin.Password = ""
	e, _ := newAdmin(t, cfg)

	for _, pass := range []string{"admin1", "admin", ""} {
		rec := do(e, http.MethodGet, "/admin/api/settings", "", map[string]string{"Authorization": basic("root", pass)})
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "password %q must not work", pass)
	}
}

func TestLoginCreatesHardenedSessionCookie(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())

	rec := login(e, "root", "s3cret-pass")
	require.Equal(t, http.StatusOK, rec.Code)

	res := rec.Result()
	var session *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "odin_admin_session" {
			session = c
		}
	}
	require.NotNil(t, session, "login sets a session cookie")
	assert.True(t, session.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, session.SameSite)
	assert.NotContains(t, session.Value, base64.StdEncoding.EncodeToString([]byte("root:s3cret-pass")))
	assert.GreaterOrEqual(t, len(session.Value), 64, "random 32-byte token")

	rec = do(e, http.MethodGet, "/admin/api/settings", "", map[string]string{"Cookie": "odin_admin_session=" + session.Value})
	assert.Equal(t, http.StatusOK, rec.Code)

	// Logout revokes the session.
	do(e, http.MethodPost, "/admin/logout", "", map[string]string{"Cookie": "odin_admin_session=" + session.Value})
	rec = do(e, http.MethodGet, "/admin/api/settings", "", map[string]string{"Cookie": "odin_admin_session=" + session.Value})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestBcryptPasswordHash(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hashed-pass"), bcrypt.MinCost)
	require.NoError(t, err)
	cfg := baseConfig()
	cfg.Admin.Password = "ignored-when-hash-is-set"
	cfg.Admin.PasswordHash = string(hash)
	e, _ := newAdmin(t, cfg)

	assert.Equal(t, http.StatusOK, do(e, http.MethodGet, "/admin/api/settings", "",
		map[string]string{"Authorization": basic("root", "hashed-pass")}).Code)
	assert.Equal(t, http.StatusUnauthorized, do(e, http.MethodGet, "/admin/api/settings", "",
		map[string]string{"Authorization": basic("root", "ignored-when-hash-is-set")}).Code)
}

func TestLoginIsThrottled(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())

	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusUnauthorized, login(e, "root", "wrong").Code)
	}
	rec := login(e, "root", "s3cret-pass")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "locked out after repeated failures, even with the right password")
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
}

func TestCrossOriginWritesAreRejected(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())
	auth := basic("root", "s3cret-pass")

	rec := do(e, http.MethodPost, "/admin/services", "name=x", map[string]string{
		"Authorization": auth,
		"Origin":        "https://evil.example",
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = login(e, "root", "s3cret-pass")
	require.Equal(t, http.StatusOK, rec.Code)
	form := url.Values{"username": {"root"}, "password": {"s3cret-pass"}}.Encode()
	rec = do(e, http.MethodPost, "/admin/login", form, map[string]string{"Origin": "https://evil.example"})
	assert.Equal(t, http.StatusForbidden, rec.Code, "login CSRF is rejected too")

	// Same-origin requests pass the check (example.com is httptest's default Host).
	rec = do(e, http.MethodPost, "/admin/services", "name=x", map[string]string{
		"Authorization": auth,
		"Origin":        "http://example.com",
	})
	assert.NotEqual(t, http.StatusForbidden, rec.Code)
}

// Behind a proxy that rewrites Host (GitHub Codespaces forwards https://<name>-8080.app.github.dev
// to localhost:8080), the browser's Origin never equals Host; admin.allowedOrigins and
// ODIN_ADMIN_ALLOWED_ORIGINS name the origins to accept.
func TestAllowedOriginsBehindAHostRewritingProxy(t *testing.T) {
	const forwarded = "https://demo-8080.app.github.dev"
	form := url.Values{"username": {"root"}, "password": {"s3cret-pass"}}.Encode()

	e, _ := newAdmin(t, baseConfig())
	rec := do(e, http.MethodPost, "/admin/login", form, map[string]string{"Origin": forwarded})
	assert.Equal(t, http.StatusForbidden, rec.Code, "not accepted by default")

	cfg := baseConfig()
	cfg.Admin.AllowedOrigins = []string{"HTTPS://Demo-8080.app.github.dev/", "not an origin", "ftp://x"}
	e, _ = newAdmin(t, cfg)
	rec = do(e, http.MethodPost, "/admin/login", form, map[string]string{"Origin": forwarded})
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = do(e, http.MethodPost, "/admin/login", form, map[string]string{"Origin": "http://demo-8080.app.github.dev"})
	assert.Equal(t, http.StatusForbidden, rec.Code, "the scheme is part of the origin")
	rec = do(e, http.MethodPost, "/admin/login", form, map[string]string{"Origin": "https://evil.example"})
	assert.Equal(t, http.StatusForbidden, rec.Code)

	t.Setenv("ODIN_ADMIN_ALLOWED_ORIGINS", " https://a.example , "+forwarded)
	e, _ = newAdmin(t, baseConfig())
	rec = do(e, http.MethodPost, "/admin/login", form, map[string]string{"Origin": forwarded})
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = do(e, http.MethodPost, "/admin/services", "name=x", map[string]string{
		"Authorization": basic("root", "s3cret-pass"),
		"Referer":       "https://a.example/admin/services/new",
	})
	assert.NotEqual(t, http.StatusForbidden, rec.Code, "the Referer is checked when Origin is absent")
}

func TestSettingsAPIRedactsSecrets(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())
	auth := map[string]string{"Authorization": basic("root", "s3cret-pass")}

	for _, path := range []string{"/admin/api/settings", "/admin/api/settings/json", "/admin/api/settings/export", "/admin/api/settings/cache"} {
		rec := do(e, http.MethodGet, path, "", auth)
		require.Equal(t, http.StatusOK, rec.Code, path)
		body := rec.Body.String()
		for _, secret := range []string{"jwt-signing-secret", "s3cret-pass", "mongo-pw", "redis-pw"} {
			assert.NotContains(t, body, secret, "%s leaks %q", path, secret)
		}
	}
}

func TestRestoreRejectsPathsInBackupName(t *testing.T) {
	e, _ := newAdmin(t, baseConfig())
	auth := map[string]string{"Authorization": basic("root", "s3cret-pass")}

	rec := do(e, http.MethodPost, "/admin/api/settings/backups/..%2F..%2Fetc%2Fpasswd/restore", "", auth)
	assert.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, rec.Code)
	rec = do(e, http.MethodPost, "/admin/api/settings/backups/.hidden/restore", "", auth)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestServiceInputIsValidatedAndEscaped(t *testing.T) {
	cfg := baseConfig()
	cfg.Services = []config.ServiceConfig{{
		Name:     "legacy<b>",
		BasePath: "/x",
		Targets:  []string{"http://a<script>"},
	}}
	e, cfgPath := newAdmin(t, cfg)
	auth := map[string]string{"Authorization": basic("root", "s3cret-pass")}

	rec := do(e, http.MethodGet, "/admin/services", "", auth)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "<script>")
	assert.NotContains(t, rec.Body.String(), "legacy<b>")
	assert.Contains(t, rec.Body.String(), "legacy&lt;b&gt;")

	bad := url.Values{"name": {`<img src=x onerror=alert(1)>`}, "basePath": {"/x"}, "targets": {"http://a"}}.Encode()
	rec = do(e, http.MethodPost, "/admin/services", bad, auth)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	badTarget := url.Values{"name": {"svc"}, "basePath": {"/x"}, "targets": {"javascript:alert(1)"}}.Encode()
	rec = do(e, http.MethodPost, "/admin/services", badTarget, auth)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	good := url.Values{"name": {"svc"}, "basePath": {"/svc"}, "targets": {"http://backend:8080"}}.Encode()
	rec = do(e, http.MethodPost, "/admin/services", good, auth)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	info, err := os.Stat(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "config holds secrets")
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "http://backend:8080")
}

func TestServicesFromServicesYAMLAreNotCopiedIntoConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
server:
  port: 8080
admin:
  enabled: true
  username: root
  password: s3cret-pass
services:
  - name: main
    basePath: /main
    targets: ["http://main:80"]
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "services.yaml"), []byte(`
services:
  - name: main
    basePath: /dup
    targets: ["http://dup:80"]
  - name: extra
    basePath: /extra
    targets: ["http://extra:80"]
`), 0o600))

	cfg, err := config.Load(cfgPath, quietLogger())
	require.NoError(t, err)
	require.Len(t, cfg.Services, 2, "the duplicate from services.yaml is skipped")
	assert.Equal(t, "/main", cfg.Services[0].BasePath)

	e := echo.New()
	admin.New(cfg, cfgPath, quietLogger()).Register(e)
	auth := map[string]string{"Authorization": basic("root", "s3cret-pass")}

	good := url.Values{"name": {"added"}, "basePath": {"/added"}, "targets": {"http://added:80"}}.Encode()
	require.Equal(t, http.StatusOK, do(e, http.MethodPost, "/admin/services", good, auth).Code)

	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "extra", "services.yaml entries stay in services.yaml")
	assert.Contains(t, string(data), "added")

	rec := do(e, http.MethodDelete, "/admin/services/extra", "", auth)
	assert.Equal(t, http.StatusConflict, rec.Code, "cannot delete a service owned by services.yaml from the admin")
}

// The monitoring WebSocket accepts the admin's own origin and admin.allowedOrigins only.
func TestMonitoringSocketChecksTheOrigin(t *testing.T) {
	cfg := baseConfig()
	cfg.Admin.AllowedOrigins = []string{"https://demo-8080.app.github.dev"}
	e, _ := newAdmin(t, cfg)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/admin/ws/monitoring"

	dial := func(origin string) (int, error) {
		h := http.Header{"Authorization": {basic("root", "s3cret-pass")}, "Origin": {origin}}
		conn, resp, err := gws.DefaultDialer.Dial(wsURL, h)
		if conn != nil {
			_ = conn.Close()
		}
		if resp == nil {
			return 0, err
		}
		_ = resp.Body.Close()
		return resp.StatusCode, err
	}

	code, err := dial(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, http.StatusSwitchingProtocols, code)

	code, err = dial("https://demo-8080.app.github.dev")
	require.NoError(t, err)
	assert.Equal(t, http.StatusSwitchingProtocols, code)

	code, err = dial("https://evil.example")
	assert.Error(t, err)
	assert.Equal(t, http.StatusForbidden, code)
}
