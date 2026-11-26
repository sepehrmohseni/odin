package gateway_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"odin/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With ai.enabled, the gateway feeds every service request (with its real status) to the
// analyzer, and the admin serves the Anomalies page and API.
func TestAnomalyDetectionIsWiredIn(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/broken" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	gw, h := pipelineGateway(t, &config.Config{
		Admin: config.AdminConfig{Enabled: true, Username: "root", Password: "s3cret-pass"},
		AI:    config.AIConfig{Enabled: true, AnalysisInterval: time.Hour, MinSamplesForBaseline: 2},
		Services: []config.ServiceConfig{
			{Name: "api", BasePath: "/api", Targets: []string{be.URL}},
		},
	})
	require.NotNil(t, gw.Analyzer())

	for i := 0; i < 30; i++ {
		serve(h, "GET", "/api/ok", nil, nil)
	}
	serve(h, "GET", "/api/broken", nil, nil)
	serve(h, "GET", "/nowhere", nil, nil) // not a service: not counted
	gw.Analyzer().Analyze()

	bl := gw.Analyzer().Baselines()
	require.Len(t, bl, 1)
	assert.Equal(t, "api", bl[0].Service)
	assert.Equal(t, 1, bl[0].Samples)
	assert.InDelta(t, 100.0/31, bl[0].ServerErrorPct.Mean, 0.01, "the 502 counted as a server error")

	auth := map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("root:s3cret-pass"))}
	rec := serve(h, "GET", "/admin/api/ai/baselines", nil, auth)
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Baselines []struct {
			Service string `json:"service"`
		} `json:"baselines"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Baselines, 1)

	assert.Equal(t, http.StatusOK, serve(h, "GET", "/admin/api/ai/stats", nil, auth).Code)
	assert.Equal(t, http.StatusOK, serve(h, "GET", "/admin/api/ai/anomalies?open=1", nil, auth).Code)
	assert.Equal(t, http.StatusNotFound, serve(h, "POST", "/admin/api/ai/anomalies/nope/resolve", nil, auth).Code)
}
