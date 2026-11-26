// Package multicluster lets services fail over to, or share traffic with, the same service in
// other clusters: other gateways (another Odin, an ingress) that serve the same routes.
//
// A cluster is an endpoint plus a health check. A service that lists clusters gets their
// endpoints as extra targets (see service.Config.ClusterTargets); the Manager checks their
// health and builds their TLS settings. Requests sent to another cluster carry HopHeader, and a
// request that arrives with it is not sent on to a further cluster, so two clusters that fail
// over to each other cannot bounce a request between them.
package multicluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"odin/pkg/config"

	"github.com/sirupsen/logrus"
)

// HopHeader marks a request forwarded to another cluster; its value is the sending cluster.
const HopHeader = "X-Odin-Cluster-Hop"

// Status is one cluster's health, for the admin.
type Status struct {
	Name      string    `json:"name"`
	Endpoint  string    `json:"endpoint"`
	Region    string    `json:"region,omitempty"`
	Priority  int       `json:"priority"`
	Healthy   bool      `json:"healthy"`
	Checked   bool      `json:"checked"` // health checks are on
	LastCheck time.Time `json:"lastCheck"`
	LatencyMs float64   `json:"latencyMs"`
	LastError string    `json:"lastError,omitempty"`
}

type cluster struct {
	cfg    config.ClusterConfig
	client *http.Client
	tls    *tls.Config

	mu        sync.Mutex
	healthy   bool
	fails     int
	passes    int
	lastCheck time.Time
	latency   time.Duration
	lastErr   string
}

// Manager health-checks the configured clusters.
type Manager struct {
	local    string
	clusters map[string]*cluster // by name
	byURL    map[string]*cluster // by endpoint
	logger   *logrus.Logger
	stop     chan struct{}
	wg       sync.WaitGroup
}

// NewManager validates the clusters and prepares their health checks and TLS settings.
func NewManager(cfg config.MultiClusterConfig, logger *logrus.Logger) (*Manager, error) {
	m := &Manager{local: cfg.LocalCluster, clusters: map[string]*cluster{}, byURL: map[string]*cluster{}, logger: logger}
	if m.local == "" {
		m.local = "odin"
	}
	for _, cc := range cfg.Clusters {
		if !cc.Enabled {
			continue
		}
		if cc.Name == "" || cc.Endpoint == "" {
			return nil, fmt.Errorf("multiCluster: every cluster needs a name and an endpoint")
		}
		if _, dup := m.clusters[cc.Name]; dup {
			return nil, fmt.Errorf("multiCluster: cluster %q is listed twice", cc.Name)
		}
		cc.Endpoint = strings.TrimRight(cc.Endpoint, "/")
		if !strings.HasPrefix(cc.Endpoint, "http://") && !strings.HasPrefix(cc.Endpoint, "https://") {
			return nil, fmt.Errorf("multiCluster: cluster %s: endpoint %q must be an http(s) URL", cc.Name, cc.Endpoint)
		}
		tlsCfg, err := tlsConfig(cc.TLS)
		if err != nil {
			return nil, fmt.Errorf("multiCluster: cluster %s: %w", cc.Name, err)
		}
		timeout := cc.HealthCheck.Timeout
		if timeout <= 0 {
			timeout = 3 * time.Second
		}
		c := &cluster{
			cfg:     cc,
			tls:     tlsCfg,
			client:  &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment}},
			healthy: true, // until a check says otherwise
		}
		m.clusters[cc.Name] = c
		m.byURL[cc.Endpoint] = c
	}
	return m, nil
}

// Local is this gateway's cluster name, sent in HopHeader.
func (m *Manager) Local() string { return m.local }

// Start runs the health checks of clusters that have them.
func (m *Manager) Start() {
	m.stop = make(chan struct{})
	for _, c := range m.clusters {
		if !c.cfg.HealthCheck.Enabled {
			continue
		}
		m.wg.Add(1)
		go func(c *cluster) {
			defer m.wg.Done()
			interval := c.cfg.HealthCheck.Interval
			if interval <= 0 {
				interval = 10 * time.Second
			}
			m.check(c)
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-m.stop:
					return
				case <-t.C:
					m.check(c)
				}
			}
		}(c)
	}
}

// Close stops the health checks.
func (m *Manager) Close() error {
	if m.stop != nil {
		close(m.stop)
		m.wg.Wait()
		m.stop = nil
	}
	return nil
}

func (m *Manager) check(c *cluster) {
	hc := c.cfg.HealthCheck
	path := hc.Path
	if path == "" {
		path = "/health"
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Endpoint+path, nil)
	var status int
	start := time.Now()
	if err == nil {
		switch c.cfg.Auth.Type {
		case "token":
			req.Header.Set("Authorization", "Bearer "+c.cfg.Auth.Token)
		case "basic":
			req.SetBasicAuth(c.cfg.Auth.Username, c.cfg.Auth.Password)
		}
		req.Header.Set(HopHeader, m.local)
		var resp *http.Response
		if resp, err = c.client.Do(req); err == nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
			if status < 200 || status >= 300 {
				err = fmt.Errorf("health check answered %d", status)
			}
		}
	}
	elapsed := time.Since(start)

	unhealthyAfter, healthyAfter := hc.UnhealthyThreshold, hc.HealthyThreshold
	if unhealthyAfter <= 0 {
		unhealthyAfter = 2
	}
	if healthyAfter <= 0 {
		healthyAfter = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastCheck, c.latency = time.Now(), elapsed
	was := c.healthy
	if err != nil {
		c.fails, c.passes, c.lastErr = c.fails+1, 0, err.Error()
		if c.fails >= unhealthyAfter {
			c.healthy = false
		}
	} else {
		c.passes, c.fails, c.lastErr = c.passes+1, 0, ""
		if c.passes >= healthyAfter {
			c.healthy = true
		}
	}
	if was != c.healthy {
		m.logger.WithFields(logrus.Fields{"cluster": c.cfg.Name, "healthy": c.healthy, "error": c.lastErr}).Warn("Cluster health changed")
	}
}

// Has reports whether name is an enabled cluster.
func (m *Manager) Has(name string) bool {
	_, ok := m.clusters[name]
	return ok
}

// Healthy reports whether endpoint is a cluster that passes its health check. The second
// result is false when endpoint is not a cluster at all.
func (m *Manager) Healthy(endpoint string) (healthy, isCluster bool) {
	c, ok := m.byURL[endpoint]
	if !ok {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.healthy, true
}

// Endpoints returns the endpoints of the named clusters ("*" for all), most preferred first:
// higher priority, then name.
func Endpoints(cfg config.MultiClusterConfig, names []string) []string {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var list []config.ClusterConfig
	for _, cc := range cfg.Clusters {
		if cc.Enabled && (want["*"] || want[cc.Name]) {
			list = append(list, cc)
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Priority != list[j].Priority {
			return list[i].Priority > list[j].Priority
		}
		return list[i].Name < list[j].Name
	})
	out := make([]string, len(list))
	for i, cc := range list {
		out[i] = strings.TrimRight(cc.Endpoint, "/")
	}
	return out
}

// TLS returns the TLS settings for requests to the cluster at endpoint, or nil when endpoint is
// not a cluster or has none of its own.
func (m *Manager) TLS(endpoint string) *tls.Config {
	if c, ok := m.byURL[endpoint]; ok {
		return c.tls
	}
	return nil
}

// Status returns every cluster's health, by priority.
func (m *Manager) Status() []Status {
	out := make([]Status, 0, len(m.clusters))
	for _, c := range m.clusters {
		c.mu.Lock()
		out = append(out, Status{
			Name:      c.cfg.Name,
			Endpoint:  c.cfg.Endpoint,
			Region:    c.cfg.Region,
			Priority:  c.cfg.Priority,
			Healthy:   c.healthy,
			Checked:   c.cfg.HealthCheck.Enabled,
			LastCheck: c.lastCheck,
			LatencyMs: float64(c.latency.Microseconds()) / 1000,
			LastError: c.lastErr,
		})
		c.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func tlsConfig(t config.ClusterTLS) (*tls.Config, error) {
	if !t.Enabled && !t.Insecure && t.CAFile == "" && t.CertFile == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: t.Insecure} //nolint:gosec // insecure is an explicit opt-in
	if t.CAFile != "" {
		pem, err := os.ReadFile(filepath.Clean(t.CAFile))
		if err != nil {
			return nil, fmt.Errorf("reading tls.caFile: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls.caFile has no PEM certificates")
		}
		cfg.RootCAs = pool
	}
	if t.CertFile != "" || t.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading tls.certFile and tls.keyFile: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}
