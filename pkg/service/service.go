package service

import (
	"fmt"
	"odin/pkg/transform"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type Config struct {
	Name           string                `yaml:"name"`
	BasePath       string                `yaml:"basePath"`
	Targets        []string              `yaml:"targets"`
	StripBasePath  bool                  `yaml:"stripBasePath"`
	Timeout        time.Duration         `yaml:"timeout"`
	RetryCount     int                   `yaml:"retryCount"`
	RetryDelay     time.Duration         `yaml:"retryDelay"`
	Authentication bool                  `yaml:"authentication"`
	LoadBalancing  string                `yaml:"loadBalancing"`
	Headers        map[string]string     `yaml:"headers"`
	Protocol       string                `yaml:"protocol"`
	Canary         *CanaryConfig         `yaml:"canary,omitempty"`
	Transformation *TransformationConfig `yaml:"transformation,omitempty"`
	Transform      struct {
		Request  []TransformRule `yaml:"request"`
		Response []TransformRule `yaml:"response"`
	} `yaml:"transform"` // Legacy field, kept for backward compatibility
	Aggregation *AggregationConfig `yaml:"aggregation,omitempty"`
	HealthCheck *HealthCheckConfig `yaml:"healthCheck,omitempty"`

	// CacheDisabled turns response caching off for this service; CacheTTL overrides the global
	// cache TTL.
	CacheDisabled bool          `yaml:"-"`
	CacheTTL      time.Duration `yaml:"-"`

	// RateLimit requests per RateLimitWindow per client; 0 means no limit.
	RateLimit       int           `yaml:"-"`
	RateLimitWindow time.Duration `yaml:"-"`

	// CircuitBreaker, when set, trips per target.
	CircuitBreaker *CircuitBreakerSettings `yaml:"-"`
	// Endpoints of other clusters serving this service, most preferred first (multi-cluster).
	// With ClusterFailover they are tried only when no target is available; otherwise they
	// share the traffic with Targets.
	ClusterTargets  []string `yaml:"-"`
	ClusterFailover bool     `yaml:"-"`
}

// CircuitBreakerSettings mirrors config.CircuitBreakerConfig with defaults applied.
type CircuitBreakerSettings struct {
	FailureRatio float64
	MinRequests  uint32
	Interval     time.Duration
	Timeout      time.Duration
	MaxRequests  uint32
}

// TransformationConfig holds the new template-based transformation settings
type TransformationConfig struct {
	Request  *transform.RequestTransform  `yaml:"request,omitempty"`
	Response *transform.ResponseTransform `yaml:"response,omitempty"`
}

type CanaryConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Targets     []string `yaml:"targets"`
	Weight      int      `yaml:"weight"` // Percentage of traffic (0-100)
	Header      string   `yaml:"header,omitempty"`
	HeaderValue string   `yaml:"headerValue,omitempty"`
	CookieName  string   `yaml:"cookieName,omitempty"`
	CookieValue string   `yaml:"cookieValue,omitempty"`
}

// HealthCheckConfig holds health check settings for backend targets
type HealthCheckConfig struct {
	Enabled            bool          `yaml:"enabled"`
	Interval           time.Duration `yaml:"interval"`           // How often to check (default: 30s)
	Timeout            time.Duration `yaml:"timeout"`            // Request timeout (default: 5s)
	UnhealthyThreshold int           `yaml:"unhealthyThreshold"` // Failures before unhealthy (default: 3)
	HealthyThreshold   int           `yaml:"healthyThreshold"`   // Successes before healthy (default: 2)
	ExpectedStatus     []int         `yaml:"expectedStatus"`     // Expected HTTP status codes (default: [200, 204])
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify"` // Skip TLS verification
}

type AggregationConfig struct {
	Dependencies []DependencyConfig `yaml:"dependencies"`
}

type DependencyConfig struct {
	Service          string          `yaml:"service"`
	Path             string          `yaml:"path"`
	ParameterMapping []MappingConfig `yaml:"parameterMapping"`
	ResultMapping    []MappingConfig `yaml:"resultMapping"`
}

type MappingConfig struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

type TransformRule struct {
	From    string `yaml:"from"`
	To      string `yaml:"to"`
	Default string `yaml:"default"`
}

type Registry struct {
	services map[string]*Config
	logger   *logrus.Logger
}

func NewRegistry(logger *logrus.Logger) *Registry {
	return &Registry{
		services: make(map[string]*Config),
		logger:   logger,
	}
}

func (r *Registry) Register(svc *Config) error {
	if svc.Name == "" {
		return fmt.Errorf("service name cannot be empty")
	}

	if _, exists := r.services[svc.Name]; exists {
		return fmt.Errorf("service %s already registered", svc.Name)
	}

	r.services[svc.Name] = svc
	r.logger.WithFields(logrus.Fields{
		"name":           svc.Name,
		"base_path":      svc.BasePath,
		"targets":        svc.Targets,
		"authentication": svc.Authentication,
	}).Info("Service registered")

	return nil
}

func (r *Registry) GetService(name string) (*Config, bool) {
	svc, ok := r.services[name]
	return svc, ok
}

func (r *Registry) GetAllServices() []*Config {
	services := make([]*Config, 0, len(r.services))
	for _, svc := range r.services {
		services = append(services, svc)
	}
	return services
}

func (r *Registry) GetServiceByPath(path string) (*Config, bool) {
	for _, svc := range r.services {
		if path == svc.BasePath || (path != "/" && svc.BasePath != "/" &&
			(path == svc.BasePath || strings.HasPrefix(path, svc.BasePath+"/"))) {
			return svc, true
		}
	}
	return nil, false
}
