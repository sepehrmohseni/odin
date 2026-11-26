package gateway_test

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"odin/pkg/config"
	"odin/pkg/service"
	"odin/pkg/transform"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetriesReplayTheBodyAndOnlyForIdempotentRequests(t *testing.T) {
	var calls atomic.Int64
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(body)
	})
	cfg := &config.Config{Services: []config.ServiceConfig{{
		Name: "svc", BasePath: "/svc", Targets: []string{be.URL},
	}}}
	cfg.Services[0].SetDefaults()
	h := newGatewayWithRetries(t, cfg, 2)

	// An idempotent request (Idempotency-Key) is retried, with its body sent again.
	rec := serve(h, "PUT", "/svc/item", strings.NewReader(`{"v":1}`), map[string]string{"Idempotency-Key": "k1"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"v":1}`, rec.Body.String(), "the retry carries the original body (it used to be empty)")
	assert.Equal(t, int64(2), calls.Load())

	// A plain POST is not retried: the upstream's 503 is returned after one call.
	calls.Store(0)
	rec = serve(h, "POST", "/svc/item", strings.NewReader(`{"v":2}`), nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, int64(1), calls.Load())
}

// newGatewayWithRetries is newGateway without its RetryCount reset.
func newGatewayWithRetries(t *testing.T, cfg *config.Config, retries int) http.Handler {
	t.Helper()
	for i := range cfg.Services {
		cfg.Services[i].SetDefaults()
		cfg.Services[i].RetryCount = retries
		cfg.Services[i].RetryDelay = time.Millisecond
	}
	return startGateway(t, cfg)
}

func TestFailoverToAnotherTarget(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // connection refused

	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "alive") })
	cfg := &config.Config{Services: []config.ServiceConfig{{
		Name: "svc", BasePath: "/svc", Targets: []string{deadURL, be.URL},
	}}}
	h := newGatewayWithRetries(t, cfg, 1)

	for i := 0; i < 4; i++ {
		rec := serve(h, "GET", "/svc/x", nil, nil)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "alive", rec.Body.String())
	}
}

func TestForwardedHeaders(t *testing.T) {
	var seen http.Header
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
	})
	svc := config.ServiceConfig{
		Name: "svc", BasePath: "/svc", Targets: []string{be.URL},
		Headers: map[string]string{"X-Gateway": "odin"},
	}

	h := newGateway(t, &config.Config{Services: []config.ServiceConfig{svc}})
	serve(h, "GET", "/svc/x", nil, map[string]string{
		"X-Forwarded-For": "1.2.3.4", // spoofed by the client
		"Connection":      "X-Secret-Hop",
		"X-Secret-Hop":    "1",
		"Keep-Alive":      "timeout=5",
	})
	require.NotNil(t, seen)
	assert.Equal(t, "192.0.2.1", seen.Get("X-Forwarded-For"), "an untrusted client cannot inject addresses")
	assert.Equal(t, "192.0.2.1", seen.Get("X-Real-IP"))
	assert.Equal(t, "http", seen.Get("X-Forwarded-Proto"))
	assert.Equal(t, "example.com", seen.Get("X-Forwarded-Host"))
	assert.Equal(t, "odin", seen.Get("X-Gateway"), "configured service headers are sent")
	assert.Empty(t, seen.Get("X-Secret-Hop"), "headers named in Connection are hop-by-hop")
	assert.Empty(t, seen.Get("Keep-Alive"))

	// Behind a trusted proxy the chain is kept and extended.
	h = newGateway(t, &config.Config{
		Server:   config.ServerConfig{TrustedProxies: []string{"192.0.2.0/24"}},
		Services: []config.ServiceConfig{svc},
	})
	serve(h, "GET", "/svc/x", nil, map[string]string{"X-Forwarded-For": "1.2.3.4"})
	assert.Equal(t, "1.2.3.4, 192.0.2.1", seen.Get("X-Forwarded-For"))
	assert.Equal(t, "1.2.3.4", seen.Get("X-Real-IP"))
}

func TestUpstreamPathJoining(t *testing.T) {
	var gotPath, gotRaw, gotQuery string
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotRaw, gotQuery = r.URL.Path, r.URL.EscapedPath(), r.URL.RawQuery
	})
	h := newGateway(t, &config.Config{Services: []config.ServiceConfig{{
		Name: "svc", BasePath: "/api", Targets: []string{be.URL + "/v1/?key=k"}, StripBasePath: true,
	}}})

	serve(h, "GET", "/api/files/a%2Fb?x=1", nil, nil)
	assert.Equal(t, "/v1/files/a/b", gotPath)
	assert.Equal(t, "/v1/files/a%2Fb", gotRaw, "encoded slashes survive")
	assert.Equal(t, "key=k&x=1", gotQuery)

	serve(h, "GET", "/api", nil, nil)
	assert.Equal(t, "/v1/", gotPath, "no doubled slash")
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "followed")
	})
	h := newGateway(t, &config.Config{Services: []config.ServiceConfig{{
		Name: "svc", BasePath: "/old", Targets: []string{be.URL},
	}}})
	rec := serve(h, "GET", "/old", nil, nil)
	assert.Equal(t, http.StatusFound, rec.Code, "the client gets the redirect, as from the upstream")
	assert.Equal(t, "/new", rec.Header().Get("Location"))
}

func TestResponseTransformKeepsContentLengthRight(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"name":"odin"}`)
	})
	svc := &service.Config{
		Name: "svc", BasePath: "/svc", Targets: []string{be.URL}, Timeout: time.Second,
		Transformation: &service.TransformationConfig{
			Response: &transform.ResponseTransform{Body: `{"wrapped":{{ toJson . }}}`},
		},
	}
	h := newRouterGateway(t, svc)

	rec := serve(h, "GET", "/svc/x", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"wrapped":{"name":"odin","statusCode":200}}`, rec.Body.String())
	assert.Equal(t, rec.Body.Len(), atoi(t, rec.Header().Get("Content-Length")))
}

func TestServerSentEventsAreStreamed(t *testing.T) {
	release := make(chan struct{})
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release // hold the stream open
		_, _ = io.WriteString(w, "data: second\n\n")
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	gw := httptest.NewServer(newGateway(t, &config.Config{Services: []config.ServiceConfig{{
		Name: "events", BasePath: "/events", Targets: []string{be.URL},
	}}}))
	defer gw.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(gw.URL + "/events")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		assert.Equal(t, "data: first\n", l, "the first event arrives while the upstream is still sending")
	case <-time.After(3 * time.Second):
		t.Fatal("the event was buffered instead of streamed")
	}
	close(release)
}

func TestUnhealthyTargetsAreSkipped(t *testing.T) {
	var sick, well atomic.Int64
	sickBE := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		sick.Add(1)
	})
	wellBE := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			well.Add(1)
		}
	})
	h := newGateway(t, &config.Config{Services: []config.ServiceConfig{{
		Name: "svc", BasePath: "/svc", Targets: []string{sickBE.URL, wellBE.URL},
		HealthCheck: &config.HealthCheckConfig{
			Enabled: true, Path: "/healthz", Interval: 20 * time.Millisecond, UnhealthyThreshold: 1,
		},
	}}})

	require.Eventually(t, func() bool {
		sick.Store(0)
		for i := 0; i < 6; i++ {
			serve(h, "GET", "/svc/x", nil, nil)
		}
		return sick.Load() == 0
	}, 3*time.Second, 50*time.Millisecond, "requests stop reaching the unhealthy target")
	assert.Positive(t, well.Load())
}

func TestInvalidTargetFailsStartup(t *testing.T) {
	cfg := &config.Config{Services: []config.ServiceConfig{{
		Name: "svc", BasePath: "/svc", Targets: []string{"localhost:8080"},
	}}}
	cfg.Server.Port = 8080
	cfg.Services[0].SetDefaults()
	_, err := gatewayNew(t, cfg)
	assert.ErrorContains(t, err, "invalid target")
}
