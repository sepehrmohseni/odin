package gateway_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"odin/pkg/config"
	"odin/pkg/health"
	"odin/pkg/integrations/postman"
	"odin/pkg/mongodb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsAreLabelledByServiceNotPath(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	h := newGateway(t, &config.Config{
		Monitoring: config.MonitoringConfig{Enabled: true, Path: "/metrics"},
		Services:   []config.ServiceConfig{{Name: "orders", BasePath: "/orders", Targets: []string{be.URL}}},
	})

	serve(h, "GET", "/orders/1", nil, nil)
	serve(h, "GET", "/attacker-chosen-segment-8f3a/x", nil, nil)

	body := serve(h, "GET", "/metrics", nil, nil).Body.String()
	assert.Contains(t, body, `service="orders"`)
	assert.Contains(t, body, `service="gateway"`)
	assert.NotContains(t, body, "attacker-chosen-segment-8f3a", "path segments must not become label values")
}

func TestStopIsIdempotentAndLateAlertsAreDropped(t *testing.T) {
	logger := quietLogger()
	am := health.NewAlertManager(logger)
	am.Start()
	am.Stop()
	assert.NotPanics(t, func() {
		am.Stop()
		am.SendAlert(health.Alert{Type: health.AlertTypeTargetDown, Target: "x", Timestamp: time.Now()})
	})

	checker := health.NewTargetChecker(health.Config{Interval: time.Hour}, logger, am)
	checker.Start()
	checker.Stop()
	assert.NotPanics(t, checker.Stop)
}

func TestGatewayShutdownTwice(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Port: 8080}}
	gw, err := gatewayNew(t, cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	assert.NoError(t, gw.Shutdown(ctx))
	assert.NotPanics(t, func() { _ = gw.Shutdown(ctx) })
}

func TestPostmanSyncIntervalIsValidated(t *testing.T) {
	for _, bad := range []string{"0s", "-1m", "30s", "soon"} {
		_, err := postman.ParseSyncInterval(bad)
		assert.Error(t, err, bad)
	}
	d, err := postman.ParseSyncInterval("5m")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, d)

	engine := postman.NewSyncEngine(nil, &postman.IntegrationConfig{AutoSync: true, SyncInterval: "0s"}, nil, quietLogger())
	assert.Error(t, engine.Start(context.Background()), "a zero interval is refused instead of panicking in the ticker")
	assert.False(t, engine.IsRunning())
}

func TestMongoConnectFailsFastAndCleanly(t *testing.T) {
	start := time.Now()
	_, err := mongodb.NewRepository(&mongodb.Config{
		Enabled:        true,
		URI:            "mongodb://127.0.0.1:1",
		Database:       "odin_test",
		ConnectTimeout: 300 * time.Millisecond,
	}, quietLogger())
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}
