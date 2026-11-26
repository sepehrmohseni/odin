package ai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"odin/pkg/ai"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quiet() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func analyzer() *ai.Analyzer {
	return ai.NewAnalyzer(ai.Options{Interval: time.Minute, Window: 10 * time.Minute, Threshold: 3, MinSamples: 5}, quiet())
}

// window records n requests to svc: errors of them answer 500, the rest 200, each taking lat.
func window(a *ai.Analyzer, svc string, n, errors int, lat time.Duration) []*ai.Anomaly {
	for i := 0; i < n; i++ {
		status := http.StatusOK
		if i < errors {
			status = http.StatusInternalServerError
		}
		a.Record(svc, status, lat, "10.0.0."+strconv.Itoa(i%2))
	}
	return a.Analyze()
}

func learn(a *ai.Analyzer, svc string) {
	for i := 0; i < 6; i++ {
		window(a, svc, 600, 6, 20*time.Millisecond)
	}
}

func TestNoAnomaliesWhileLearningOrSteady(t *testing.T) {
	a := analyzer()
	for i := 0; i < 4; i++ {
		assert.Empty(t, window(a, "api", 600, 300, 20*time.Millisecond), "still learning: nothing is judged")
	}

	b := analyzer()
	learn(b, "steady")
	// The baseline deviation of a perfectly steady service is 0; small changes must not turn
	// into infinite z-scores (the old detector divided by zero).
	assert.Empty(t, window(b, "steady", 620, 7, 21*time.Millisecond))
	for _, bl := range b.Baselines() {
		assert.True(t, bl.Ready)
		assert.InDelta(t, 10, bl.RequestRate.Mean, 0.5)
	}
}

func TestErrorSpikeIsOneOngoingAnomalyUntilItEnds(t *testing.T) {
	a := analyzer()
	learn(a, "orders")

	found := window(a, "orders", 600, 360, 20*time.Millisecond)
	require.Len(t, found, 1)
	assert.Equal(t, ai.TypeErrorSpike, found[0].Type)
	assert.Equal(t, ai.SeverityCritical, found[0].Severity, "60% server errors")
	assert.Contains(t, found[0].Description, "60.0% of responses were server errors")

	again := window(a, "orders", 600, 300, 20*time.Millisecond)
	require.Len(t, again, 1)
	assert.Equal(t, found[0].ID, again[0].ID, "the same ongoing anomaly")
	assert.Equal(t, 2, again[0].Windows)

	assert.Empty(t, window(a, "orders", 600, 6, 20*time.Millisecond))
	list := a.Anomalies(ai.Filter{})
	require.Len(t, list, 1)
	assert.True(t, list[0].Resolved, "resolved when the window is normal again")
	assert.Empty(t, a.Anomalies(ai.Filter{Open: true}))

	// The outage did not become the new normal.
	for _, b := range a.Baselines() {
		assert.Less(t, b.ServerErrorPct.Mean, 5.0)
	}
}

func TestTrafficSpikeDropAndLatency(t *testing.T) {
	a := analyzer()
	learn(a, "users")

	found := window(a, "users", 3000, 30, 20*time.Millisecond)
	require.Len(t, found, 1)
	assert.Equal(t, ai.TypeTrafficSpike, found[0].Type)

	b := analyzer()
	learn(b, "users")
	found = b.Analyze() // a window without a single request
	require.Len(t, found, 1)
	assert.Equal(t, ai.TypeTrafficDrop, found[0].Type)
	assert.InDelta(t, 0, found[0].Observed, 0.001)

	c := analyzer()
	learn(c, "users")
	found = window(c, "users", 600, 6, 400*time.Millisecond)
	require.Len(t, found, 1)
	assert.Equal(t, ai.TypeLatencySpike, found[0].Type)
	assert.Contains(t, found[0].Description, "Average latency 400 ms")
}

func TestDominantClient(t *testing.T) {
	a := analyzer()
	for i := 0; i < 180; i++ {
		a.Record("search", 200, time.Millisecond, "203.0.113.9")
	}
	for i := 0; i < 20; i++ {
		a.Record("search", 200, time.Millisecond, "198.51.100."+strconv.Itoa(i))
	}
	found := a.Analyze()
	require.Len(t, found, 1, "no baseline needed")
	assert.Equal(t, ai.TypeClientDominant, found[0].Type)
	assert.Contains(t, found[0].Description, "One client (203.0.113.9) sent 90% of 200 requests")

	// One client alone (a test, a cron job) is not unusual.
	b := analyzer()
	for i := 0; i < 500; i++ {
		b.Record("batch", 200, time.Millisecond, "10.0.0.1")
	}
	assert.Empty(t, b.Analyze())
}

func TestMarkAndFilter(t *testing.T) {
	a := analyzer()
	for i := 0; i < 6; i++ { // both services in the same windows
		for j := 0; j < 600; j++ {
			a.Record("orders", 200, 20*time.Millisecond, "10.0.0.1")
			a.Record("users", 200, 20*time.Millisecond, "10.0.0.1")
		}
		a.Analyze()
	}
	for j := 0; j < 600; j++ {
		a.Record("users", 200, 20*time.Millisecond, "10.0.0.1")
	}
	window(a, "orders", 600, 360, 20*time.Millisecond)
	list := a.Anomalies(ai.Filter{Service: "orders"})
	require.Len(t, list, 1)
	assert.Empty(t, a.Anomalies(ai.Filter{Service: "users"}))
	assert.Len(t, a.Anomalies(ai.Filter{Severity: ai.SeverityHigh}), 1)

	assert.True(t, a.Mark(list[0].ID, true))
	assert.False(t, a.Mark("nope", false))
	got := a.Anomalies(ai.Filter{})[0]
	assert.True(t, got.FalsePositive)
	assert.Empty(t, a.Anomalies(ai.Filter{Open: true}))
}

func TestWebhookAndExplanation(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]interface{}
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body)
		mu.Unlock()
	}))
	defer hook.Close()
	explain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/analyze", r.URL.Path)
		var in map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&in)
		assert.NotContains(t, in["prompt"], "10.0.0.", "client addresses are not sent")
		_, _ = w.Write([]byte(`{"response":"The upstream is failing.","suggestions":["Check its logs"]}`))
	}))
	defer explain.Close()

	a := analyzer()
	a.SetNotifier(&ai.Webhook{URL: hook.URL})
	a.SetExplainer(&ai.AnalysisService{URL: explain.URL})
	learn(a, "orders")
	window(a, "orders", 600, 360, 20*time.Millisecond)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(sent) == 1
	}, 5*time.Second, 20*time.Millisecond)
	mu.Lock()
	assert.Contains(t, sent[0]["text"], "[CRITICAL] orders:")
	mu.Unlock()
	got := a.Anomalies(ai.Filter{})[0]
	assert.Equal(t, "The upstream is failing.", got.Explanation)
	assert.Equal(t, []string{"Check its logs"}, got.Suggestions)

	// Below the webhook's minimum severity, nothing is sent.
	w := &ai.Webhook{URL: hook.URL, MinSeverity: ai.SeverityCritical}
	require.NoError(t, w.Notify(context.Background(), &ai.Anomaly{Severity: ai.SeverityHigh}))
	mu.Lock()
	assert.Len(t, sent, 1)
	mu.Unlock()
}
