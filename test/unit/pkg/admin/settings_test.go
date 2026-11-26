package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The settings API returns what the settings page and the update handlers use: camelCase names
// and duration strings. (It returned raw structs, {"Port": 8080, "Timeout": 30000000000}, so the
// page showed empty fields.) Saving the values back keeps the secrets that were masked.
func TestSettingsAPIRoundTrip(t *testing.T) {
	cfg := baseConfig()
	cfg.Server.Port = 8080
	cfg.Server.ReadTimeout = 30 * time.Second
	cfg.RateLimit.Enabled = true
	cfg.RateLimit.Limit = 100
	cfg.RateLimit.Duration = time.Minute
	cfg.RateLimit.Strategy = "redis"
	cfg.RateLimit.RedisURL = "redis://:limit-pw@cache:6379"
	e, _ := newAdmin(t, cfg)
	auth := basic("root", "s3cret-pass")

	get := func(path string) map[string]interface{} {
		rec := do(e, http.MethodGet, path, "", map[string]string{"Authorization": auth})
		require.Equal(t, http.StatusOK, rec.Code, path)
		var out map[string]interface{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}

	server := get("/admin/api/settings/server")["server"].(map[string]interface{})
	assert.EqualValues(t, 8080, server["port"])
	assert.Equal(t, "30s", server["readTimeout"])

	rl := get("/admin/api/settings/ratelimit")["rateLimit"].(map[string]interface{})
	assert.Equal(t, "1m0s", rl["duration"])
	assert.NotContains(t, rl["redisUrl"], "limit-pw")

	// Save the form as the page would, with the masked URL it was shown.
	body, err := json.Marshal(rl)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/admin/api/settings/ratelimit", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "redis://:limit-pw@cache:6379", cfg.RateLimit.RedisURL, "the masked secret is not saved over the real one")
	assert.Equal(t, time.Minute, cfg.RateLimit.Duration)

	for _, path := range []string{"logging", "cache", "monitoring", "tracing"} {
		out := get("/admin/api/settings/" + path)
		assert.Len(t, out, 1, path)
	}
}
