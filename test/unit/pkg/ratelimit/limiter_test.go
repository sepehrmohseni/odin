package ratelimit

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"odin/pkg/ratelimit"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLimiter(t *testing.T) {
	config := ratelimit.Config{
		Enabled:       true,
		Algorithm:     ratelimit.AlgorithmFixedWindow,
		DefaultLimit:  100,
		DefaultWindow: time.Minute,
	}

	limiter, err := ratelimit.NewLimiter(config, logrus.New())
	require.NoError(t, err)
	assert.NotNil(t, limiter)
}

func TestLimiter_ShouldSkip(t *testing.T) {
	config := ratelimit.Config{
		Enabled:   true,
		SkipPaths: []string{"/health", "/metrics"},
	}

	limiter, err := ratelimit.NewLimiter(config, logrus.New())
	require.NoError(t, err)

	e := echo.New()

	tests := []struct {
		path       string
		shouldSkip bool
	}{
		{"/health", true},
		{"/metrics", true},
		{"/health/check", true},
		{"/api/users", false},
		{"/healthz", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			middleware := limiter.Middleware()
			nextCalled := false

			next := func(c echo.Context) error {
				nextCalled = true
				return nil
			}

			err := middleware(next)(c)

			if tt.shouldSkip {
				assert.NoError(t, err)
				assert.True(t, nextCalled, "Next should be called for skipped paths")
			} else {
				// For non-skipped paths, the rate limiter should process the request
				// Since we have no Redis and default limits, it should still call next
				assert.NoError(t, err)
				assert.True(t, nextCalled, "Next should be called when rate limit not exceeded")
			}
		})
	}
}

func TestLimiter_GenerateKey(t *testing.T) {
	config := ratelimit.Config{
		HeadersToInclude: []string{"User-Agent"},
	}

	limiter, err := ratelimit.NewLimiter(config, logrus.New())
	require.NoError(t, err)

	e := echo.New()

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-API-Key", "test-key")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	rule := &ratelimit.Rule{
		Path:   "/api/test",
		Method: "GET",
	}

	key := limiter.GenerateKey(c, rule)
	assert.NotEmpty(t, key)
	assert.Contains(t, key, "ratelimit:")

	// Generate another key with same parameters to ensure consistency
	key2 := limiter.GenerateKey(c, rule)
	assert.Equal(t, key, key2, "Keys should be consistent for same request")
}

func TestLimiter_CheckLimit_FixedWindow(t *testing.T) {
	config := ratelimit.Config{
		Algorithm:     ratelimit.AlgorithmFixedWindow,
		DefaultLimit:  2,
		DefaultWindow: time.Minute,
	}

	limiter, err := ratelimit.NewLimiter(config, logrus.New())
	require.NoError(t, err)

	rule := &ratelimit.Rule{
		Limit:  2,
		Window: time.Minute,
	}

	key := "test-key"
	ctx := context.Background()

	// Without Redis the limiter keeps counts in memory (it used to allow everything).
	limitInfo1, allowed1 := limiter.CheckLimit(ctx, key, rule)
	assert.True(t, allowed1)
	assert.Equal(t, 2, limitInfo1.Limit)
	assert.Equal(t, 1, limitInfo1.Remaining)

	limitInfo2, allowed2 := limiter.CheckLimit(ctx, key, rule)
	assert.True(t, allowed2)
	assert.Equal(t, 0, limitInfo2.Remaining)

	limitInfo3, allowed3 := limiter.CheckLimit(ctx, key, rule)
	assert.False(t, allowed3, "the third request exceeds a limit of 2")
	assert.Equal(t, 0, limitInfo3.Remaining)

	// Other keys have their own count.
	_, allowedOther := limiter.CheckLimit(ctx, "other-key", rule)
	assert.True(t, allowedOther)
}

func TestLimiter_MatchesRule(t *testing.T) {
	config := ratelimit.Config{}

	limiter, err := ratelimit.NewLimiter(config, logrus.New())
	require.NoError(t, err)

	e := echo.New()

	tests := []struct {
		name        string
		rule        ratelimit.Rule
		method      string
		path        string
		headers     map[string]string
		expectMatch bool
	}{
		{
			name: "exact path match",
			rule: ratelimit.Rule{
				Method: "GET",
				Path:   "/api/users",
			},
			method:      "GET",
			path:        "/api/users",
			expectMatch: true,
		},
		{
			name: "prefix path match",
			rule: ratelimit.Rule{
				Method: "GET",
				Path:   "/api/",
			},
			method:      "GET",
			path:        "/api/users",
			expectMatch: true,
		},
		{
			name: "method mismatch",
			rule: ratelimit.Rule{
				Method: "POST",
				Path:   "/api/users",
			},
			method:      "GET",
			path:        "/api/users",
			expectMatch: false,
		},
		{
			name: "header rule match",
			rule: ratelimit.Rule{
				Path: "/api/admin",
				Headers: []ratelimit.HeaderRule{
					{Name: "X-Admin", Value: "true"},
				},
			},
			method:      "GET",
			path:        "/api/admin",
			headers:     map[string]string{"X-Admin": "true"},
			expectMatch: true,
		},
		{
			name: "header rule mismatch",
			rule: ratelimit.Rule{
				Path: "/api/admin",
				Headers: []ratelimit.HeaderRule{
					{Name: "X-Admin", Value: "true"},
				},
			},
			method:      "GET",
			path:        "/api/admin",
			headers:     map[string]string{"X-Admin": "false"},
			expectMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			matches := limiter.MatchesRule(c, &tt.rule)
			assert.Equal(t, tt.expectMatch, matches)
		})
	}
}

func TestLimiter_IPMatching(t *testing.T) {
	config := ratelimit.Config{}

	limiter, err := ratelimit.NewLimiter(config, logrus.New())
	require.NoError(t, err)

	tests := []struct {
		clientIP    string
		ruleIP      string
		expectMatch bool
	}{
		{"192.168.1.100", "192.168.1.100", true},
		{"192.168.1.100", "192.168.1.101", false},
		{"192.168.1.100", "192.168.1.0/24", true},
		{"10.0.0.1", "192.168.1.0/24", false},
		{"192.168.1.255", "192.168.1.0/24", true},
	}

	for _, tt := range tests {
		t.Run(tt.clientIP+"_vs_"+tt.ruleIP, func(t *testing.T) {
			matches := limiter.MatchesIP(tt.clientIP, tt.ruleIP)
			assert.Equal(t, tt.expectMatch, matches)
		})
	}
}

func TestLimiter_LocalAlgorithms(t *testing.T) {
	ctx := context.Background()
	for _, algo := range []ratelimit.Algorithm{
		ratelimit.AlgorithmFixedWindow, ratelimit.AlgorithmSlidingWindow, ratelimit.AlgorithmTokenBucket,
	} {
		t.Run(string(algo), func(t *testing.T) {
			limiter, err := ratelimit.NewLimiter(ratelimit.Config{Algorithm: algo}, logrus.New())
			require.NoError(t, err)
			rule := &ratelimit.Rule{Limit: 3, Window: time.Hour}

			for i := 0; i < 3; i++ {
				_, ok := limiter.CheckLimit(ctx, "k", rule)
				assert.True(t, ok, "request %d", i+1)
			}
			_, ok := limiter.CheckLimit(ctx, "k", rule)
			assert.False(t, ok, "over the limit")
		})
	}
}

func TestLimiter_SubSecondWindowAndRecovery(t *testing.T) {
	ctx := context.Background()
	limiter, err := ratelimit.NewLimiter(ratelimit.Config{Algorithm: ratelimit.AlgorithmTokenBucket}, logrus.New())
	require.NoError(t, err)
	rule := &ratelimit.Rule{Limit: 2, Window: 100 * time.Millisecond} // used to divide by zero

	limiter.CheckLimit(ctx, "k", rule)
	limiter.CheckLimit(ctx, "k", rule)
	_, ok := limiter.CheckLimit(ctx, "k", rule)
	assert.False(t, ok)

	assert.Eventually(t, func() bool {
		_, ok := limiter.CheckLimit(ctx, "k", rule)
		return ok
	}, time.Second, 10*time.Millisecond, "tokens refill")

	fixed, err := ratelimit.NewLimiter(ratelimit.Config{Algorithm: ratelimit.AlgorithmFixedWindow}, logrus.New())
	require.NoError(t, err)
	assert.NotPanics(t, func() { fixed.CheckLimit(ctx, "k", &ratelimit.Rule{Limit: 1, Window: 10 * time.Millisecond}) })
}
