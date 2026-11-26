package admin

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// MetricsData represents the structure of monitoring data
type MetricsData struct {
	TotalRequests     int64   `json:"totalRequests"`
	AvgResponseTime   float64 `json:"avgResponseTime"`
	ActiveConnections int     `json:"activeConnections"`
	SuccessRate       float64 `json:"successRate"`
	// RequestRate is requests per second over the last minute.
	RequestRate  float64                 `json:"requestRate"`
	ResponseTime ResponseTimePercentiles `json:"responseTime"`
	StatusCodes  StatusCodeDistribution  `json:"statusCodes"`
	Services     []ServiceStatus         `json:"services"`
	Traces       []TraceInfo             `json:"traces"`
	// Traffic is per service (the gateway's own endpoints are "gateway").
	Traffic []ServiceTraffic `json:"traffic"`
	// Timeline is the last five minutes in 5-second steps, oldest first.
	Timeline []TimelinePoint `json:"timeline"`
	// Targets is the active health check state of every checked target.
	Targets   []TargetStatus `json:"targets"`
	StartedAt time.Time      `json:"startedAt"`
}

// ServiceTraffic counts one service's requests since the gateway started.
type ServiceTraffic struct {
	Name         string    `json:"name"`
	Requests     int64     `json:"requests"`
	ClientErrors int64     `json:"clientErrors"`
	ServerErrors int64     `json:"serverErrors"`
	AvgMs        float64   `json:"avgMs"`
	LastStatus   int       `json:"lastStatus"`
	LastSeen     time.Time `json:"lastSeen"`
}

// TimelinePoint is the traffic in one step of the timeline.
type TimelinePoint struct {
	Time         int64 `json:"time"` // Unix seconds at the start of the step
	Requests     int64 `json:"requests"`
	ClientErrors int64 `json:"clientErrors"`
	ServerErrors int64 `json:"serverErrors"`
}

// TargetStatus is one upstream target's health check state.
type TargetStatus struct {
	Service    string    `json:"service"`
	URL        string    `json:"url"`
	Status     string    `json:"status"` // healthy, degraded or unhealthy
	LastCheck  time.Time `json:"lastCheck"`
	ResponseMs float64   `json:"responseMs"`
	LastError  string    `json:"lastError,omitempty"`
}

const (
	timelineSeconds = 300
	timelineStep    = 5
)

type secondBucket struct {
	sec                                  int64
	requests, clientErrors, serverErrors int64
}

type serviceCounters struct {
	requests, clientErrors, serverErrors int64
	totalMs                              float64
	lastStatus                           int
	lastSeen                             time.Time
}

type ResponseTimePercentiles struct {
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
}

type StatusCodeDistribution struct {
	Success     int `json:"success"`     // 2xx
	Redirect    int `json:"redirect"`    // 3xx
	ClientError int `json:"clientError"` // 4xx
	ServerError int `json:"serverError"` // 5xx
}

type ServiceStatus struct {
	Name         string  `json:"name"`
	Protocol     string  `json:"protocol"`
	Healthy      bool    `json:"healthy"`
	Warning      bool    `json:"warning"`
	ResponseTime float64 `json:"responseTime"`
	LastCheck    string  `json:"lastCheck"`
}

type TraceInfo struct {
	TraceID   string    `json:"traceId"`
	SpanID    string    `json:"spanId"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Service   string    `json:"service"`
	Duration  float64   `json:"duration"`
	Status    int       `json:"status"`
	Timestamp time.Time `json:"timestamp"`
}

// MonitoringCollector collects and aggregates monitoring metrics
type MonitoringCollector struct {
	mu             sync.RWMutex
	requestCount   int64
	responseTimes  []float64
	statusCodes    map[int]int
	activeConns    int
	traces         []TraceInfo
	services       map[string]ServiceStatus
	wsClients      map[*wsClient]bool
	wsClientsMutex sync.RWMutex

	seconds      [timelineSeconds]secondBucket
	perService   map[string]*serviceCounters
	healthSource func() []TargetStatus
	startedAt    time.Time
	now          func() time.Time
}

// NewMonitoringCollector creates a new monitoring collector
func NewMonitoringCollector() *MonitoringCollector {
	return &MonitoringCollector{
		responseTimes: make([]float64, 0),
		statusCodes:   make(map[int]int),
		traces:        make([]TraceInfo, 0),
		services:      make(map[string]ServiceStatus),
		wsClients:     make(map[*wsClient]bool),
		perService:    make(map[string]*serviceCounters),
		startedAt:     time.Now(),
		now:           time.Now,
	}
}

// SetHealthSource gives the collector the gateway's active health checks; GetMetrics reports
// them as Targets and derives each checked service's status from its targets.
func (mc *MonitoringCollector) SetHealthSource(fn func() []TargetStatus) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.healthSource = fn
}

var collector *MonitoringCollector

func init() {
	collector = NewMonitoringCollector()
}

// GetCollector returns the global monitoring collector instance
func GetCollector() *MonitoringCollector {
	return collector
}

// RecordRequest records a new request metric
func (mc *MonitoringCollector) RecordRequest(method, path string, duration time.Duration, statusCode int, service string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.requestCount++
	durationMs := float64(duration.Nanoseconds()) / 1e6
	mc.responseTimes = append(mc.responseTimes, durationMs) // Keep only last 1000 response times for memory efficiency
	if len(mc.responseTimes) > 1000 {
		mc.responseTimes = mc.responseTimes[len(mc.responseTimes)-1000:]
	}

	mc.statusCodes[statusCode]++

	now := mc.now()
	sec := now.Unix()
	b := &mc.seconds[sec%timelineSeconds]
	if b.sec != sec {
		*b = secondBucket{sec: sec}
	}
	b.requests++
	sc := mc.perService[service]
	if sc == nil {
		sc = &serviceCounters{}
		mc.perService[service] = sc
	}
	sc.requests++
	sc.totalMs += durationMs
	sc.lastStatus = statusCode
	sc.lastSeen = now
	switch {
	case statusCode >= 500:
		b.serverErrors++
		sc.serverErrors++
	case statusCode >= 400:
		b.clientErrors++
		sc.clientErrors++
	}

	// Add trace info
	trace := TraceInfo{
		TraceID:   generateTraceID(),
		SpanID:    generateSpanID(),
		Method:    method,
		Path:      path,
		Service:   service,
		Duration:  durationMs,
		Status:    statusCode,
		Timestamp: now,
	}

	mc.traces = append(mc.traces, trace)
	// Keep only last 100 traces
	if len(mc.traces) > 100 {
		mc.traces = mc.traces[len(mc.traces)-100:]
	}
}

// UpdateActiveConnections updates the active connections count
func (mc *MonitoringCollector) UpdateActiveConnections(count int) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.activeConns = count
}

// UpdateServiceStatus updates the status of a service
func (mc *MonitoringCollector) UpdateServiceStatus(name, protocol string, healthy bool, responseTime float64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.services[name] = ServiceStatus{
		Name:         name,
		Protocol:     protocol,
		Healthy:      healthy,
		Warning:      responseTime > 1000, // Consider >1s as warning
		ResponseTime: responseTime,
		LastCheck:    time.Now().Format("15:04:05"),
	}
}

// GetMetrics returns current aggregated metrics
func (mc *MonitoringCollector) GetMetrics() MetricsData {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	// Calculate response time percentiles
	percentiles := mc.calculatePercentiles()

	// Calculate success rate
	successRate := mc.calculateSuccessRate()

	// Calculate request rate (requests per minute)
	requestRate := mc.calculateRequestRate()

	var targets []TargetStatus
	if mc.healthSource != nil {
		targets = mc.healthSource()
	}

	// Services reported with UpdateServiceStatus, overridden by the state of their checked
	// targets (nothing called UpdateServiceStatus, so this list used to be always empty).
	byName := make(map[string]ServiceStatus, len(mc.services))
	for name, service := range mc.services {
		byName[name] = service
	}
	for name, st := range serviceHealth(targets) {
		byName[name] = st
	}
	services := make([]ServiceStatus, 0, len(byName))
	for _, service := range byName {
		services = append(services, service)
	}

	// Sort services by name
	sort.Slice(services, func(i, j int) bool {
		return services[i].Name < services[j].Name
	})

	// Calculate status code distribution
	statusDist := StatusCodeDistribution{
		Success:     mc.statusCodes[200] + mc.statusCodes[201] + mc.statusCodes[204],
		Redirect:    mc.statusCodes[301] + mc.statusCodes[302] + mc.statusCodes[304],
		ClientError: mc.statusCodes[400] + mc.statusCodes[401] + mc.statusCodes[403] + mc.statusCodes[404],
		ServerError: mc.statusCodes[500] + mc.statusCodes[502] + mc.statusCodes[503] + mc.statusCodes[504],
	}

	// Add up all other status codes
	for code, count := range mc.statusCodes {
		if code >= 200 && code < 300 && code != 200 && code != 201 && code != 204 {
			statusDist.Success += count
		} else if code >= 300 && code < 400 && code != 301 && code != 302 && code != 304 {
			statusDist.Redirect += count
		} else if code >= 400 && code < 500 && code != 400 && code != 401 && code != 403 && code != 404 {
			statusDist.ClientError += count
		} else if code >= 500 && code != 500 && code != 502 && code != 503 && code != 504 {
			statusDist.ServerError += count
		}
	}

	avgResponseTime := 0.0
	if len(mc.responseTimes) > 0 {
		sum := 0.0
		for _, rt := range mc.responseTimes {
			sum += rt
		}
		avgResponseTime = sum / float64(len(mc.responseTimes))
	}

	return MetricsData{
		TotalRequests:     mc.requestCount,
		AvgResponseTime:   avgResponseTime,
		ActiveConnections: mc.activeConns,
		SuccessRate:       successRate,
		RequestRate:       requestRate,
		ResponseTime:      percentiles,
		StatusCodes:       statusDist,
		Services:          services,
		Traces:            append([]TraceInfo(nil), mc.traces...),
		Traffic:           mc.traffic(),
		Timeline:          mc.timeline(),
		Targets:           targets,
		StartedAt:         mc.startedAt,
	}
}

func (mc *MonitoringCollector) traffic() []ServiceTraffic {
	out := make([]ServiceTraffic, 0, len(mc.perService))
	for name, sc := range mc.perService {
		t := ServiceTraffic{
			Name:         name,
			Requests:     sc.requests,
			ClientErrors: sc.clientErrors,
			ServerErrors: sc.serverErrors,
			LastStatus:   sc.lastStatus,
			LastSeen:     sc.lastSeen,
		}
		if sc.requests > 0 {
			t.AvgMs = sc.totalMs / float64(sc.requests)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// timeline sums the per-second buckets of the last five minutes into 5-second steps.
func (mc *MonitoringCollector) timeline() []TimelinePoint {
	end := mc.now().Unix()
	last := end - end%timelineStep // the step that holds the current second
	first := last - (timelineSeconds/timelineStep-1)*timelineStep
	points := make([]TimelinePoint, 0, timelineSeconds/timelineStep)
	for start := first; start <= last; start += timelineStep {
		p := TimelinePoint{Time: start}
		for sec := start; sec < start+timelineStep && sec <= end; sec++ {
			if b := mc.seconds[sec%timelineSeconds]; b.sec == sec {
				p.Requests += b.requests
				p.ClientErrors += b.clientErrors
				p.ServerErrors += b.serverErrors
			}
		}
		points = append(points, p)
	}
	return points
}

// serviceHealth summarises targets per service: healthy when all are, a warning when some are
// degraded or unhealthy, unhealthy when none is healthy.
func serviceHealth(targets []TargetStatus) map[string]ServiceStatus {
	type agg struct {
		total, healthy, bad int
		ms                  float64
		last                time.Time
	}
	by := map[string]*agg{}
	for _, t := range targets {
		a := by[t.Service]
		if a == nil {
			a = &agg{}
			by[t.Service] = a
		}
		a.total++
		a.ms += t.ResponseMs
		if t.Status == "healthy" {
			a.healthy++
		} else {
			a.bad++
		}
		if t.LastCheck.After(a.last) {
			a.last = t.LastCheck
		}
	}
	out := make(map[string]ServiceStatus, len(by))
	for name, a := range by {
		st := ServiceStatus{
			Name:         name,
			Protocol:     "http",
			Healthy:      a.healthy > 0,
			Warning:      a.bad > 0,
			ResponseTime: a.ms / float64(a.total),
		}
		if !a.last.IsZero() {
			st.LastCheck = a.last.Format("15:04:05")
		}
		out[name] = st
	}
	return out
}

// Calculate percentiles for response times
func (mc *MonitoringCollector) calculatePercentiles() ResponseTimePercentiles {
	if len(mc.responseTimes) == 0 {
		return ResponseTimePercentiles{}
	}

	// Copy and sort response times
	sorted := make([]float64, len(mc.responseTimes))
	copy(sorted, mc.responseTimes)
	sort.Float64s(sorted)

	n := len(sorted)
	return ResponseTimePercentiles{
		P50: sorted[int(float64(n)*0.5)],
		P90: sorted[int(float64(n)*0.9)],
		P99: sorted[int(float64(n)*0.99)],
	}
}

// Calculate success rate (2xx status codes)
func (mc *MonitoringCollector) calculateSuccessRate() float64 {
	total := 0
	success := 0

	for code, count := range mc.statusCodes {
		total += count
		if code >= 200 && code < 300 {
			success += count
		}
	}

	if total == 0 {
		return 0
	}

	return float64(success) / float64(total)
}

// calculateRequestRate is requests per second over the last minute. (It used to be the total
// request count divided by 60, whatever the time.)
func (mc *MonitoringCollector) calculateRequestRate() float64 {
	now := mc.now().Unix()
	var n int64
	for _, b := range &mc.seconds {
		if b.sec > now-60 && b.sec <= now {
			n += b.requests
		}
	}
	return float64(n) / 60
}

// BroadcastMetrics sends metrics to all connected WebSocket clients
func (mc *MonitoringCollector) BroadcastMetrics() {
	metrics := mc.GetMetrics()
	data, err := json.Marshal(metrics)
	if err != nil {
		log.Printf("Error marshaling metrics: %v", err)
		return
	}
	mc.wsClientsMutex.RLock()
	defer mc.wsClientsMutex.RUnlock()
	for client := range mc.wsClients {
		client.offer(data)
	}
}

// wsClient is one monitoring WebSocket. Only its writer goroutine writes to the connection
// (gorilla/websocket allows one writer at a time; the first snapshot and a broadcast used to
// race), and a broadcast never waits for it: a client too slow to keep up misses an update
// instead of holding up every other client.
type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

func newWSClient(conn *websocket.Conn) *wsClient {
	c := &wsClient{conn: conn, send: make(chan []byte, 4)}
	go c.writeLoop()
	return c
}

func (c *wsClient) offer(data []byte) {
	select {
	case c.send <- data:
	default: // the client is behind; it gets the next update
	}
}

func (c *wsClient) writeLoop() {
	defer c.conn.Close()
	for data := range c.send {
		_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
	_ = c.conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(time.Second))
}

// StartMetricsBroadcaster starts broadcasting metrics to WebSocket clients
func (mc *MonitoringCollector) StartMetricsBroadcaster() {
	ticker := time.NewTicker(5 * time.Second) // Broadcast every 5 seconds
	go func() {
		for range ticker.C {
			mc.BroadcastMetrics()
		}
	}()
}

// HTTP Handlers

// GetMonitoringPage serves the monitoring dashboard HTML
func GetMonitoringPage(c echo.Context) error {
	return c.Render(http.StatusOK, "monitoring.html", nil)
}

// GetMetricsAPI returns metrics as JSON
func GetMetricsAPI(c echo.Context) error {
	metrics := collector.GetMetrics()
	return c.JSON(http.StatusOK, metrics)
}

// webSocketMonitoring streams live metrics to the monitoring page.
func (h *AdminHandler) webSocketMonitoring(c echo.Context) error {
	upgrader := websocket.Upgrader{
		// Only the admin's own pages (or admin.allowedOrigins) may open the monitoring socket;
		// otherwise any site could read live traffic data with the admin's cookie (cross-site
		// WebSocket hijacking).
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // not a browser
			}
			return h.trustedOrigin(origin, r.Host)
		},
	}
	ws, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return err
	}
	defer ws.Close()

	client := newWSClient(ws)
	collector.wsClientsMutex.Lock()
	collector.wsClients[client] = true
	collector.wsClientsMutex.Unlock()

	// Remove the client when the connection closes; closing send ends its writer.
	defer func() {
		collector.wsClientsMutex.Lock()
		delete(collector.wsClients, client)
		collector.wsClientsMutex.Unlock()
		close(client.send)
	}()

	// The first snapshot goes through the writer too.
	if data, err := json.Marshal(collector.GetMetrics()); err == nil {
		client.offer(data)
	}

	// Keep connection alive and handle ping/pong
	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
	}

	return nil
}

// Utility functions for generating trace/span IDs
func generateTraceID() string {
	return time.Now().Format("20060102150405") + "000000"
}

func generateSpanID() string {
	return time.Now().Format("150405") + "00"
}
