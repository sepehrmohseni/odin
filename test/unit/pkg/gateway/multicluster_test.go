package gateway_test

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"odin/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// remoteCluster is another gateway: it answers its health check and says who it is.
func remoteCluster(t *testing.T, healthy *atomic.Bool) *backend {
	return newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if healthy != nil && !healthy.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-Served-By", "remote")
		w.Header().Set("X-Seen-Hop", r.Header.Get("X-Odin-Cluster-Hop"))
		w.WriteHeader(http.StatusOK)
	})
}

func clusterConfig(endpoint, strategy string) config.MultiClusterConfig {
	return config.MultiClusterConfig{
		Enabled:          true,
		LocalCluster:     "eu-central",
		FailoverStrategy: strategy,
		Clusters: []config.ClusterConfig{{
			Name: "us-east", Endpoint: endpoint, Enabled: true, Priority: 10,
			HealthCheck: config.ClusterHealthCheck{Enabled: true, Interval: 30 * time.Millisecond, UnhealthyThreshold: 1},
		}},
	}
}

func TestClusterFailoverWhenNoOwnTargetIsAvailable(t *testing.T) {
	var localUp atomic.Bool
	localUp.Store(true)
	local := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if !localUp.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Served-By", "local")
		w.WriteHeader(http.StatusOK)
	})
	remote := remoteCluster(t, nil)

	_, h := pipelineGateway(t, &config.Config{
		MultiCluster: clusterConfig(remote.URL, ""),
		Services: []config.ServiceConfig{{
			Name: "orders", BasePath: "/api/orders", Targets: []string{local.URL}, Clusters: []string{"us-east"},
			HealthCheck: &config.HealthCheckConfig{Enabled: true, Interval: 30 * time.Millisecond, UnhealthyThreshold: 1, HealthyThreshold: 1, Path: "/"},
		}},
	})

	rec := serve(h, "GET", "/api/orders/1", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "local", rec.Header().Get("X-Served-By"), "own targets first")

	localUp.Store(false) // its health check fails from now on
	require.Eventually(t, func() bool {
		return serve(h, "GET", "/api/orders/1", nil, nil).Header().Get("X-Served-By") == "remote"
	}, 3*time.Second, 30*time.Millisecond, "fails over to the other cluster")
	rec = serve(h, "GET", "/api/orders/1", nil, nil)
	assert.Equal(t, "eu-central", rec.Header().Get("X-Seen-Hop"), "the remote cluster sees where the request came from")

	// A request that already came from another cluster is not sent on.
	rec = serve(h, "GET", "/api/orders/1", nil, map[string]string{"X-Odin-Cluster-Hop": "us-east"})
	assert.NotEqual(t, "remote", rec.Header().Get("X-Served-By"))

	localUp.Store(true)
	require.Eventually(t, func() bool {
		return serve(h, "GET", "/api/orders/1", nil, nil).Header().Get("X-Served-By") == "local"
	}, 3*time.Second, 30*time.Millisecond, "back home once its own target is healthy")
}

func TestUnhealthyClusterIsNotUsed(t *testing.T) {
	var up atomic.Bool
	up.Store(false)
	remote := remoteCluster(t, &up)
	_, h := pipelineGateway(t, &config.Config{
		MultiCluster: clusterConfig(remote.URL, ""),
		Services: []config.ServiceConfig{{
			Name: "orders", BasePath: "/api/orders", Targets: []string{"http://127.0.0.1:1"}, Clusters: []string{"*"},
			HealthCheck: &config.HealthCheckConfig{Enabled: true, Interval: 30 * time.Millisecond, UnhealthyThreshold: 1, Path: "/"},
		}},
	})
	time.Sleep(150 * time.Millisecond) // both health checks have run
	assert.NotEqual(t, "remote", serve(h, "GET", "/api/orders", nil, nil).Header().Get("X-Served-By"))

	up.Store(true)
	require.Eventually(t, func() bool {
		return serve(h, "GET", "/api/orders", nil, nil).Header().Get("X-Served-By") == "remote"
	}, 3*time.Second, 30*time.Millisecond)
}

func TestActiveClustersShareTraffic(t *testing.T) {
	local := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "local")
	})
	remote := remoteCluster(t, nil)
	_, h := pipelineGateway(t, &config.Config{
		MultiCluster: clusterConfig(remote.URL, "active"),
		Services: []config.ServiceConfig{{
			Name: "orders", BasePath: "/api/orders", Targets: []string{local.URL}, Clusters: []string{"us-east"},
		}},
	})
	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		seen[serve(h, "GET", "/api/orders", nil, nil).Header().Get("X-Served-By")]++
	}
	assert.Equal(t, 5, seen["local"])
	assert.Equal(t, 5, seen["remote"])
}

func TestClusterConfigErrors(t *testing.T) {
	svc := config.ServiceConfig{Name: "orders", BasePath: "/api/orders", Targets: []string{"http://127.0.0.1:1"}, Clusters: []string{"mars"}}

	_, err := gatewayNew(t, &config.Config{Server: config.ServerConfig{Port: 8080}, Services: []config.ServiceConfig{svc}})
	assert.ErrorContains(t, err, "multiCluster.enabled is false")

	_, err = gatewayNew(t, &config.Config{Server: config.ServerConfig{Port: 8080}, MultiCluster: clusterConfig("http://127.0.0.1:2", ""), Services: []config.ServiceConfig{svc}})
	assert.ErrorContains(t, err, `cluster "mars"`)

	mc := clusterConfig("http://127.0.0.1:2", "sideways")
	_, err = gatewayNew(t, &config.Config{Server: config.ServerConfig{Port: 8080}, MultiCluster: mc})
	assert.ErrorContains(t, err, "failoverStrategy")
}
