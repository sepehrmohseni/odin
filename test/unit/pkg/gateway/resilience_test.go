package gateway_test

import (
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"odin/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimitPerServiceAndClient(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	h := newGateway(t, &config.Config{
		Auth: config.AuthConfig{JWTSecret: testSecret},
		Services: []config.ServiceConfig{
			{Name: "limited", BasePath: "/limited", Targets: []string{be.URL},
				RateLimit: &config.ServiceRateLimitConfig{Limit: 2, Window: time.Hour}},
			{Name: "free", BasePath: "/free", Targets: []string{be.URL}},
		},
	})

	for i := 0; i < 2; i++ {
		rec := serve(h, "GET", "/limited/x", nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "2", rec.Header().Get("X-RateLimit-Limit"))
		assert.Equal(t, strconv.Itoa(1-i), rec.Header().Get("X-RateLimit-Remaining"))
	}
	rec := serve(h, "GET", "/limited/x", nil, nil)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))

	// A spoofed X-Forwarded-For does not buy a fresh quota.
	rec = serve(h, "GET", "/limited/x", nil, map[string]string{"X-Forwarded-For": "203.0.113.9"})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Services without a limit are unaffected.
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/free/x", nil, nil).Code)
}

func TestGlobalRateLimitAndOptOut(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {})
	off := false
	h := newGateway(t, &config.Config{
		RateLimit: config.RateLimitConfig{Enabled: true, Limit: 1, Duration: time.Hour},
		Services: []config.ServiceConfig{
			{Name: "a", BasePath: "/a", Targets: []string{be.URL}},
			{Name: "b", BasePath: "/b", Targets: []string{be.URL}, RateLimit: &config.ServiceRateLimitConfig{Enabled: &off}},
		},
	})
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/a/x", nil, nil).Code)
	assert.Equal(t, http.StatusTooManyRequests, serve(h, "GET", "/a/x", nil, nil).Code)
	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusOK, serve(h, "GET", "/b/x", nil, nil).Code, "b opted out")
	}
}

func TestCircuitBreakerStopsCallingAFailingTarget(t *testing.T) {
	var calls atomic.Int64
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := newGateway(t, &config.Config{Services: []config.ServiceConfig{{
		Name: "flaky", BasePath: "/flaky", Targets: []string{be.URL},
		CircuitBreaker: &config.CircuitBreakerConfig{Enabled: true, MinRequests: 3, FailureRatio: 0.5, Timeout: time.Hour},
	}}})

	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusInternalServerError, serve(h, "GET", "/flaky/x", nil, nil).Code)
	}
	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusServiceUnavailable, serve(h, "GET", "/flaky/x", nil, nil).Code)
	}
	assert.Equal(t, int64(3), calls.Load(), "an open circuit does not reach the upstream")
}

func TestCircuitBreakerRoutesAroundOneBadTarget(t *testing.T) {
	bad := newBackend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	good := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "good") })
	h := newGateway(t, &config.Config{Services: []config.ServiceConfig{{
		Name: "svc", BasePath: "/svc", Targets: []string{bad.URL, good.URL},
		CircuitBreaker: &config.CircuitBreakerConfig{Enabled: true, MinRequests: 2, FailureRatio: 0.5, Timeout: time.Hour},
	}}})

	for i := 0; i < 8; i++ {
		serve(h, "GET", "/svc/x", nil, nil)
	}
	before := bad.calls.Load()
	for i := 0; i < 6; i++ {
		rec := serve(h, "GET", "/svc/x", nil, nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	}
	assert.Equal(t, before, bad.calls.Load(), "the bad target's circuit is open")
}
