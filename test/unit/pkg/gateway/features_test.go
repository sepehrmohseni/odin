package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"odin/pkg/config"

	gws "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadConfig writes yaml to a temp config file and loads it the way the gateway does.
func loadConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	cfg, err := config.Load(path, quietLogger())
	require.NoError(t, err)
	return cfg
}

func TestCanaryFromConfig(t *testing.T) {
	stable := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "stable") })
	canary := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "canary") })

	cfg := loadConfig(t, `
services:
  - name: app
    basePath: /app
    targets: [`+stable.URL+`]
    retryCount: 0
    canary:
      enabled: true
      targets: [`+canary.URL+`]
      weight: 0
      header: X-Canary
      headerValue: "yes"
  - name: all-canary
    basePath: /all
    targets: [`+stable.URL+`]
    canary:
      enabled: true
      targets: [`+canary.URL+`]
      weight: 100
`)
	h := startGateway(t, cfg)

	assert.Equal(t, "stable", serve(h, "GET", "/app/x", nil, nil).Body.String())
	assert.Equal(t, "canary", serve(h, "GET", "/app/x", nil, map[string]string{"X-Canary": "yes"}).Body.String(),
		"canary settings in the config are honoured (they were never read)")
	assert.Equal(t, "canary", serve(h, "GET", "/all/x", nil, nil).Body.String(), "weight 100 sends everything")
}

func TestTemplateTransformationFromConfig(t *testing.T) {
	var gotQuery, gotHeader, gotBody string
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("page")
		gotHeader = r.Header.Get("X-Source")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1}`)
	})
	cfg := loadConfig(t, `
services:
  - name: api
    basePath: /api
    targets: [`+be.URL+`]
    transformation:
      request:
        headers:
          X-Source: "gateway"
        queryParams:
          page: '{{ default "1" .page }}'
        body: '{"user":"{{ upper .name }}"}'
      response:
        headers:
          X-Transformed: "true"
        body: '{"data":{{ toJson . }}}'
`)
	h := startGateway(t, cfg)

	rec := serve(h, "POST", "/api/users", strings.NewReader(`{"name":"ada"}`), map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "1", gotQuery, "queryParams maps from YAML (it needed a yaml tag)")
	assert.Equal(t, "gateway", gotHeader)
	assert.JSONEq(t, `{"user":"ADA"}`, gotBody)
	assert.Equal(t, "true", rec.Header().Get("X-Transformed"))
	assert.JSONEq(t, `{"data":{"id":1,"statusCode":200}}`, rec.Body.String())
}

func TestFieldMappingRules(t *testing.T) {
	var gotBody map[string]interface{}
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = io.WriteString(w, `{"data":[1,2],"meta":{"total":2}}`)
	})
	cfg := loadConfig(t, `
services:
  - name: users
    basePath: /users
    targets: [`+be.URL+`]
    transform:
      request:
        - from: $.username
          to: $.user.name
        - from: $.includeInactive
          to: $.filters.inactive
          default: "false"
      response:
        - from: $.data
          to: $.users
`)
	h := startGateway(t, cfg)

	rec := serve(h, "POST", "/users", strings.NewReader(`{"username":"john"}`), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]interface{}{
		"user":    map[string]interface{}{"name": "john"},
		"filters": map[string]interface{}{"inactive": false},
	}, gotBody, "the transform: rules apply (they were parsed and then ignored)")
	assert.JSONEq(t, `{"users":[1,2],"meta":{"total":2}}`, rec.Body.String())
}

func TestAggregationEnrichesResponses(t *testing.T) {
	var prefsPath string
	prefs := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		prefsPath = r.URL.EscapedPath()
		_, _ = io.WriteString(w, `{"theme":"dark"}`)
	})
	users := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"user":{"id":"7/x"}}`)
	})
	cfg := loadConfig(t, `
services:
  - name: users
    basePath: /users
    targets: [`+users.URL+`]
    aggregation:
      dependencies:
        - service: prefs
          path: /prefs/{user_id}
          parameterMapping:
            - from: $.user.id
              to: "{user_id}"
          resultMapping:
            - from: $
              to: $.preferences
  - name: prefs
    basePath: /prefs
    targets: [`+prefs.URL+`]
`)
	h := startGateway(t, cfg)

	rec := serve(h, "GET", "/users/7", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"user":{"id":"7/x"},"preferences":{"theme":"dark"}}`, rec.Body.String(),
		"aggregation dependencies are fetched (it logged 'not processed yet')")
	assert.Equal(t, "/prefs/7%2Fx", prefsPath, "values from the response are escaped into the path")
}

func TestWebSocketProxying(t *testing.T) {
	upgrader := gws.Upgrader{
		Subprotocols: []string{"chat"},
		CheckOrigin: func(r *http.Request) bool {
			return r.Header.Get("Origin") != "http://evil.example"
		},
	}
	var sawAuth, sawForwarded string
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawForwarded = r.Header.Get("X-Forwarded-For")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.WriteMessage(mt, append([]byte("echo: "), msg...))
		}
	})

	gw := httptest.NewServer(startGateway(t, loadConfig(t, `
services:
  - name: chat
    basePath: /ws
    targets: [`+be.URL+`]
`)))
	defer gw.Close()
	wsURL := "ws" + strings.TrimPrefix(gw.URL, "http") + "/ws/room"

	dialer := gws.Dialer{HandshakeTimeout: 3 * time.Second, Subprotocols: []string{"chat"}}
	conn, resp, err := dialer.Dial(wsURL, http.Header{"Authorization": {"Bearer t"}})
	require.NoError(t, err, "WebSocket upgrades are proxied (they used to go through the plain HTTP client)")
	defer func() { _ = conn.Close() }()
	assert.Equal(t, "chat", resp.Header.Get("Sec-WebSocket-Protocol"), "the negotiated subprotocol is passed back")
	assert.Equal(t, "Bearer t", sawAuth, "client headers reach the upstream")
	assert.NotEmpty(t, sawForwarded)

	for _, msg := range []string{"one", "two"} {
		require.NoError(t, conn.WriteMessage(gws.TextMessage, []byte(msg)))
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, got, err := conn.ReadMessage()
		require.NoError(t, err)
		assert.Equal(t, "echo: "+msg, string(got))
	}

	// The upstream refuses this origin; the client sees the refusal as an HTTP status.
	_, resp, err = dialer.Dial(wsURL, http.Header{"Origin": {"http://evil.example"}})
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}
