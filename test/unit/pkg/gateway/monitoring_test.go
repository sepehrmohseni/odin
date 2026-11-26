package gateway_test

import (
	"net/http"
	"testing"
	"time"

	"odin/pkg/admin"
	"odin/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trafficOf(name string) admin.ServiceTraffic {
	for _, t := range admin.GetCollector().GetMetrics().Traffic {
		if t.Name == name {
			return t
		}
	}
	return admin.ServiceTraffic{}
}

// The admin's monitoring numbers come from the request path: rejected requests count with their
// real status (they used to be recorded as 200, because the error handler runs afterwards), admin
// traffic is not gateway traffic, the rate is measured, and checked targets report their health.
func TestMonitoringRecordsRealTraffic(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	_, h := pipelineGateway(t, &config.Config{
		Auth: config.AuthConfig{JWTSecret: testSecret},
		Services: []config.ServiceConfig{
			{Name: "mon-open", BasePath: "/mon-open", Targets: []string{be.URL},
				HealthCheck: &config.HealthCheckConfig{Enabled: true, Interval: 50 * time.Millisecond, Timeout: time.Second, Path: "/"}},
			{Name: "mon-auth", BasePath: "/mon-auth", Targets: []string{be.URL}, Authentication: true},
		},
	})

	before := admin.GetCollector().GetMetrics().TotalRequests
	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusOK, serve(h, "GET", "/mon-open/x", nil, nil).Code)
	}
	for i := 0; i < 2; i++ {
		require.Equal(t, http.StatusUnauthorized, serve(h, "GET", "/mon-auth/x", nil, nil).Code)
	}
	serve(h, "GET", "/admin/login", nil, nil)
	serve(h, "GET", "/static/css/odin.css", nil, nil)
	serve(h, "GET", "/health", nil, nil)
	serve(h, "GET", "/metrics", nil, nil)

	m := admin.GetCollector().GetMetrics()
	assert.Equal(t, before+5, m.TotalRequests, "admin pages, assets, health probes and scrapes are not counted")

	open, auth := trafficOf("mon-open"), trafficOf("mon-auth")
	assert.EqualValues(t, 3, open.Requests)
	assert.EqualValues(t, 0, open.ClientErrors)
	assert.Equal(t, http.StatusOK, open.LastStatus)
	assert.EqualValues(t, 2, auth.Requests)
	assert.EqualValues(t, 2, auth.ClientErrors, "401s are client errors, not 200s")
	assert.Equal(t, http.StatusUnauthorized, auth.LastStatus)

	assert.Greater(t, m.RequestRate, 0.0)
	assert.Less(t, m.RequestRate, float64(m.TotalRequests), "a per-second rate over the last minute")
	var inTimeline int64
	for _, p := range m.Timeline {
		inTimeline += p.Requests
	}
	assert.GreaterOrEqual(t, inTimeline, int64(5))
	assert.Len(t, m.Timeline, 60, "five minutes in 5-second steps")

	require.Eventually(t, func() bool {
		for _, tg := range admin.GetCollector().GetMetrics().Targets {
			if tg.Service == "mon-open" && tg.Status == "healthy" {
				return true
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond, "the checked target's health is reported")
	for _, s := range admin.GetCollector().GetMetrics().Services {
		if s.Name == "mon-open" {
			assert.True(t, s.Healthy)
		}
	}
}
