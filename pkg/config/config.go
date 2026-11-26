package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"odin/pkg/transform"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server       ServerConfig       `yaml:"server"`
	Logging      LoggingConfig      `yaml:"logging"`
	Auth         AuthConfig         `yaml:"auth"`
	RateLimit    RateLimitConfig    `yaml:"rateLimit"`
	Cache        CacheConfig        `yaml:"cache"`
	Monitoring   MonitoringConfig   `yaml:"monitoring"`
	Admin        AdminConfig        `yaml:"admin"`
	Plugins      PluginsConfig      `yaml:"plugins"`
	Tracing      TracingConfig      `yaml:"tracing"`
	Services     []ServiceConfig    `yaml:"services"`
	ServiceMesh  ServiceMeshConfig  `yaml:"serviceMesh"`
	WASM         WASMConfig         `yaml:"wasm"`
	MultiCluster MultiClusterConfig `yaml:"multiCluster"`
	OpenAPI      OpenAPIConfig      `yaml:"openapi"`
	MongoDB      MongoDBConfig      `yaml:"mongodb"`
	AI           AIConfig           `yaml:"ai"`
}

type ServerConfig struct {
	// Port to listen on (default 8080; GATEWAY_PORT overrides it).
	Port int `yaml:"port"`
	// Not used by the request path; per-service timeouts are services[].timeout.
	Timeout time.Duration `yaml:"timeout"`
	// Maximum time to read a whole request, including the body (default 30s).
	ReadTimeout time.Duration `yaml:"readTimeout"`
	// Maximum time to write a response (default 30s). Long streams need a larger value.
	WriteTimeout time.Duration `yaml:"writeTimeout"`
	// How long shutdown waits for in-flight requests (default 15s).
	GracefulTimeout time.Duration `yaml:"gracefulTimeout"`
	// ReadHeaderTimeout bounds reading request headers (default 10s); IdleTimeout bounds idle
	// keep-alive connections (default 120s).
	ReadHeaderTimeout time.Duration `yaml:"readHeaderTimeout"`
	// How long idle keep-alive connections are kept (default 120s).
	IdleTimeout time.Duration `yaml:"idleTimeout"`
	// Gzip responses for clients that accept it.
	Compression bool `yaml:"compression"`
	// MaxBodySize limits request bodies on proxied routes, e.g. "10M" (default) or "512K".
	MaxBodySize string `yaml:"maxBodySize"`
	// TrustedProxies lists the proxy addresses or CIDRs whose X-Forwarded-For header is
	// believed. With none, the client IP is the connection's address, so clients cannot spoof
	// it to dodge rate limits or login throttling.
	TrustedProxies []string `yaml:"trustedProxies"`
	// DebugEndpoints exposes /debug/routes and friends. Off by default: they list every route.
	DebugEndpoints bool `yaml:"debugEndpoints"`
}

type LoggingConfig struct {
	// debug, info (default), warn or error.
	Level string `yaml:"level"`
	// Write logs as JSON instead of text.
	JSON bool `yaml:"json"`
}

type AuthConfig struct {
	// HMAC secret for verifying JWTs. ODIN_JWT_SECRET or an auth_secrets.yaml file take precedence.
	JWTSecret string `yaml:"jwtSecret"`
	// Not used by the gateway (it verifies tokens, it does not issue them).
	AccessTokenTTL time.Duration `yaml:"accessTokenTTL"`
	// Not used by the gateway.
	RefreshTokenTTL time.Duration `yaml:"refreshTokenTTL"`
	// Request paths (regular expressions) that skip JWT checks, e.g. ^/health$.
	IgnorePathRegexes []string `yaml:"ignorePathRegexes"`
	// Issuer, when set, must match the token's iss claim.
	Issuer string `yaml:"issuer"`
	// Audience, when set, must be in the token's aud claim.
	Audience string `yaml:"audience"`
	// Leeway tolerates clock skew when checking exp, nbf and iat.
	Leeway time.Duration `yaml:"leeway"`
}

type AdminConfig struct {
	// Serve the admin interface under /admin. Off by default; when off, no admin route exists.
	Enabled bool `yaml:"enabled"`
	// Admin login name (default admin; ODIN_ADMIN_USERNAME overrides it).
	Username string `yaml:"username"`
	// Admin password in plain text. Prefer passwordHash or ODIN_ADMIN_PASSWORD; without any, a random password is logged at startup.
	Password string `yaml:"password"`
	// PasswordHash is a bcrypt hash of the admin password; when set, Password is ignored.
	PasswordHash string `yaml:"passwordHash"`
	// AllowedOrigins lists further origins (scheme://host[:port]) the admin accepts form posts and
	// API writes from, for proxies that change the Host header (for example GitHub Codespaces
	// port forwarding). By default only the admin's own origin is accepted.
	// ODIN_ADMIN_ALLOWED_ORIGINS adds a comma-separated list.
	AllowedOrigins []string `yaml:"allowedOrigins"`
}

type PluginsConfig struct {
	// Run Go plugins (built with -buildmode=plugin) on requests.
	Enabled bool `yaml:"enabled"`
	// Directory plugins are uploaded to and loaded from (default /var/odin/plugins).
	Directory string `yaml:"directory"`
	// Plugins to load at startup.
	Plugins []PluginConfig `yaml:"plugins"`
}

type PluginConfig struct {
	// Unique plugin name.
	Name string `yaml:"name"`
	// Path to the .so file.
	Path string `yaml:"path"`
	// Load this plugin.
	Enabled bool `yaml:"enabled"`
	// Settings passed to the plugin's Initialize.
	Config map[string]interface{} `yaml:"config"`
	Hooks  []string               `yaml:"hooks"` // pre-request, post-request, pre-response, post-response
}

type RateLimitConfig struct {
	// Apply limit per duration to every HTTP service, per client (the JWT subject, or the client IP).
	Enabled bool `yaml:"enabled"`
	// Requests allowed per duration and client. 0 means no limit.
	Limit int `yaml:"limit"`
	// Length of the rate-limit window (default 1m).
	Duration time.Duration `yaml:"duration"`
	Strategy string        `yaml:"strategy"` // local (default) or redis
	// Redis for strategy: redis, e.g. redis://redis:6379/0.
	RedisURL string `yaml:"redisUrl"`
	// Algorithm is fixed_window (default), sliding_window or token_bucket.
	Algorithm string `yaml:"algorithm"`
	// FailClosed rejects requests when Redis is unreachable instead of allowing them.
	FailClosed bool `yaml:"failClosed"`
}

type CacheConfig struct {
	// Cache GET responses of HTTP services (only safe ones: no credentials, no cookies, no private or no-store).
	Enabled bool `yaml:"enabled"`
	// How long entries live, unless the upstream's max-age is shorter.
	TTL time.Duration `yaml:"ttl"`
	// Redis for strategy: redis.
	RedisURL string `yaml:"redisUrl"`
	// local (default, in memory) or redis (shared by all instances).
	Strategy string `yaml:"strategy"`
	// Not enforced yet.
	MaxSizeInMB int `yaml:"maxSizeInMB"`
}

type MonitoringConfig struct {
	// Serve Prometheus metrics.
	Enabled bool `yaml:"enabled"`
	// Metrics path (default /metrics).
	Path       string `yaml:"path"`
	WebhookURL string `yaml:"webhookUrl,omitempty"` // Optional webhook for health alerts
}

type TracingConfig struct {
	// Export OpenTelemetry traces over OTLP/HTTP and propagate trace context to upstreams.
	Enabled bool `yaml:"enabled"`
	// service.name of the spans (default odin-gateway).
	ServiceName string `yaml:"serviceName"`
	// service.version of the spans (default 1.0.0).
	ServiceVersion string `yaml:"serviceVersion"`
	// deployment.environment of the spans (default development).
	Environment string `yaml:"environment"`
	// OTLP/HTTP endpoint, as a URL or host:port (default http://localhost:4318/v1/traces).
	Endpoint string `yaml:"endpoint"`
	// Fraction of traces sampled, 0 to 1 (default 1).
	SampleRate float64 `yaml:"sampleRate"`
	// Use plain HTTP to a host:port endpoint.
	Insecure bool `yaml:"insecure"`
}

type ServiceMeshConfig struct {
	// Add service-mesh headers (trace and request IDs) to proxied requests.
	Enabled bool   `yaml:"enabled"`
	Type    string `yaml:"type"` // istio, linkerd, consul, none
	// Mesh namespace of the gateway.
	Namespace string `yaml:"namespace"`
	// SPIFFE trust domain.
	TrustDomain string `yaml:"trustDomain"`
	// Discovery endpoint (service discovery is not implemented yet).
	DiscoveryAddr string `yaml:"discoveryAddr"`
	// Discovery refresh interval.
	RefreshInterval time.Duration `yaml:"refreshInterval"`
	// Load the mTLS certificates below (not yet used for upstream requests).
	MTLSEnabled bool `yaml:"mtlsEnabled"`
	// mTLS certificate.
	CertFile string `yaml:"certFile"`
	// mTLS key.
	KeyFile string `yaml:"keyFile"`
	// mTLS CA bundle.
	CAFile  string             `yaml:"caFile"`
	Istio   *IstioMeshConfig   `yaml:"istio,omitempty"`
	Linkerd *LinkerdMeshConfig `yaml:"linkerd,omitempty"`
	Consul  *ConsulMeshConfig  `yaml:"consul,omitempty"`
}

type IstioMeshConfig struct {
	PilotAddr         string   `yaml:"pilotAddr"`
	MixerAddr         string   `yaml:"mixerAddr"`
	EnableTelemetry   bool     `yaml:"enableTelemetry"`
	EnablePolicyCheck bool     `yaml:"enablePolicyCheck"`
	CustomHeaders     []string `yaml:"customHeaders"`
	InjectSidecar     bool     `yaml:"injectSidecar"`
}

type LinkerdMeshConfig struct {
	ControlPlaneAddr string `yaml:"controlPlaneAddr"`
	TapAddr          string `yaml:"tapAddr"`
	EnableTap        bool   `yaml:"enableTap"`
	ProfileNamespace string `yaml:"profileNamespace"`
}

type ConsulMeshConfig struct {
	HTTPAddr      string `yaml:"httpAddr"`
	Datacenter    string `yaml:"datacenter"`
	Token         string `yaml:"token"`
	EnableConnect bool   `yaml:"enableConnect"`
}

type WASMConfig struct {
	// Load the WebAssembly middleware plugins below into the request pipeline.
	Enabled bool `yaml:"enabled"`
	// Directory that relative plugin paths are resolved against.
	PluginDir string `yaml:"pluginDir"`
	// The plugins.
	Plugins []WASMPluginConfig `yaml:"plugins"`
	// Memory limit of each plugin instance in 64 KiB pages (default 512, 32 MiB).
	MaxMemoryPages int `yaml:"maxMemoryPages"`
	// Instances per plugin, which is how many requests one plugin handles at a time (default 4).
	MaxInstances int `yaml:"maxInstances"`
}

// WASMPluginConfig is one WebAssembly middleware plugin (see docs/plugins.md for the ABI).
type WASMPluginConfig struct {
	// Unique name, shown on the admin's Middleware page.
	Name string `yaml:"name"`
	// The .wasm file, absolute or relative to pluginDir.
	Path string `yaml:"path"`
	// Load it; disabled plugins are skipped.
	Enabled bool `yaml:"enabled"`
	// Pipeline phase: pre-auth, post-auth (default) or pre-proxy.
	Phase string `yaml:"phase"`
	// Deprecated: use phase. "auth" means pre-auth; other values mean post-auth.
	Type string `yaml:"type"`
	// Order within the phase; lower runs first.
	Priority int `yaml:"priority"`
	// Path patterns it applies to (default ["*"], every service route).
	Routes []string `yaml:"routes"`
	// Service names it applies to; empty means every service its routes match.
	Services []string `yaml:"services"`
	// Handed to the plugin with every request.
	Config map[string]interface{} `yaml:"config"`
	// Per-request limit; the call is interrupted when it is exceeded (default 100ms).
	Timeout time.Duration `yaml:"timeout"`
	// Hand the request body (up to 1 MiB) to the plugin.
	ReadBody bool `yaml:"readBody"`
	// On a plugin error or timeout, let the request continue instead of answering 500.
	FailOpen bool `yaml:"failOpen"`
}

type MultiClusterConfig struct {
	// Let services that list clusters (services[].clusters) use them.
	Enabled bool `yaml:"enabled"`
	// This gateway's cluster name, sent with requests forwarded to another cluster (default odin).
	LocalCluster string `yaml:"localCluster"`
	// Other gateways serving the same routes.
	Clusters []ClusterConfig `yaml:"clusters"`
	// failover (default): a service uses its clusters only when none of its own targets is
	// available, most preferred first. active: clusters share the traffic with its targets.
	FailoverStrategy string `yaml:"failoverStrategy"`
}

// ClusterConfig is another gateway that serves the same routes.
type ClusterConfig struct {
	// Name that services list in services[].clusters.
	Name string `yaml:"name"`
	// Base URL, for example https://eu-west.gateway.example.com.
	Endpoint string `yaml:"endpoint"`
	// Informational, shown in the admin.
	Region string `yaml:"region"`
	// Preference for failover: higher is tried first.
	Priority int `yaml:"priority"`
	// Use this cluster.
	Enabled bool `yaml:"enabled"`
	// Health check of the endpoint; an unhealthy cluster gets no requests.
	HealthCheck ClusterHealthCheck `yaml:"healthCheck"`
	// Credentials for the health check request.
	Auth ClusterAuth `yaml:"auth"`
	// TLS for the health check and for requests forwarded to the cluster.
	TLS ClusterTLS `yaml:"tls"`
}

type ClusterHealthCheck struct {
	// Check the cluster's health.
	Enabled bool `yaml:"enabled"`
	// How often (default 10s).
	Interval time.Duration `yaml:"interval"`
	// Per check (default 3s).
	Timeout time.Duration `yaml:"timeout"`
	// Passing checks before an unhealthy cluster is used again (default 1).
	HealthyThreshold int `yaml:"healthyThreshold"`
	// Failing checks before a cluster is skipped (default 2).
	UnhealthyThreshold int `yaml:"unhealthyThreshold"`
	// Path checked; any 2xx passes (default /health).
	Path string `yaml:"path"`
}

type ClusterAuth struct {
	// none (default), token (a Bearer token) or basic.
	Type string `yaml:"type"`
	// Bearer token for type token.
	Token string `yaml:"token"`
	// User for type basic.
	Username string `yaml:"username"`
	// Password for type basic.
	Password string `yaml:"password"`
}

type ClusterTLS struct {
	// Use these TLS settings (an https endpoint works without them, with the system CAs).
	Enabled bool `yaml:"enabled"`
	// Client certificate, for clusters that require mutual TLS.
	CertFile string `yaml:"certFile"`
	// Key of certFile.
	KeyFile string `yaml:"keyFile"`
	// CA bundle that the cluster's certificate is checked against.
	CAFile string `yaml:"caFile"`
	// Do not check the cluster's certificate (testing only).
	Insecure bool `yaml:"insecure"`
}

type OpenAPIConfig struct {
	// Serve an OpenAPI 3 document of the service routes at /openapi.json and /openapi.yaml.
	// It is public: it lists every service's base path and whether it needs a token.
	Enabled bool `yaml:"enabled"`
	// Document title (default "Odin API Gateway").
	Title string `yaml:"title"`
	// Document version (default 1.0.0).
	Version string `yaml:"version"`
	// Document description.
	Description string `yaml:"description"`
	// Generate the document from the services (default true). Set false to serve the
	// hand-written document at outputPath instead.
	AutoGenerate *bool `yaml:"autoGenerate"`
	// With autoGenerate, also write the generated document here at startup (YAML for .yaml or
	// .yml, else JSON); without it, the document (JSON or YAML) to serve.
	OutputPath string `yaml:"outputPath"`
	// Serve Swagger UI for the document at uiPath. The page loads Swagger UI from jsDelivr
	// (pinned release, integrity-checked), so browsers need to reach cdn.jsdelivr.net.
	UIEnabled bool `yaml:"uiEnabled"`
	// Path of the Swagger UI page (default /openapi).
	UIPath string `yaml:"uiPath"`
}

type MongoDBConfig struct {
	// Store plugins, integration settings and anomalies in MongoDB.
	Enabled bool `yaml:"enabled"`
	// Connection string, e.g. mongodb://user:${MONGO_PASSWORD}@mongo:27017/?authSource=admin.
	URI string `yaml:"uri"`
	// Database name.
	Database string `yaml:"database"`
	// Maximum connections.
	MaxPoolSize int `yaml:"maxPoolSize"`
	// Minimum idle connections.
	MinPoolSize int `yaml:"minPoolSize"`
	// Timeout for connecting and the startup ping (default 10s).
	ConnectTimeout time.Duration `yaml:"connectTimeout"`
	// Also serve the services stored in MongoDB (written by odin-migrate or the admin API); a
	// service in the config file wins over a stored one with the same name. Startup fails when
	// they cannot be read.
	LoadServices bool        `yaml:"loadServices"`
	Auth         MongoDBAuth `yaml:"auth"`
	TLS          MongoDBTLS  `yaml:"tls"`
}

type MongoDBAuth struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	AuthDB   string `yaml:"authDB"`
}

type MongoDBTLS struct {
	Enabled  bool   `yaml:"enabled"`
	CAFile   string `yaml:"caFile"`
	CertFile string `yaml:"certFile"`
	KeyFile  string `yaml:"keyFile"`
}

// AIConfig configures traffic anomaly detection (see docs/observability.md).
type AIConfig struct {
	// Learn each service's normal traffic and report anomalies (admin: Anomalies).
	Enabled bool `yaml:"enabled"`
	// Length of each analysed window (default 1m).
	AnalysisInterval time.Duration `yaml:"analysisInterval"`
	// Roughly how far back the baseline reaches (default 1h).
	BaselineWindow time.Duration `yaml:"baselineWindow"`
	// How many standard deviations from the baseline count as an anomaly (default 3).
	AnomalyThreshold float64 `yaml:"anomalyThreshold"`
	// Windows to learn from before anomalies are reported (default 10).
	MinSamplesForBaseline int `yaml:"minSamplesForBaseline"`
	// Ask the analysis service at grokServiceUrl to explain new anomalies.
	UseGrokModel bool `yaml:"useGrokModel"`
	// Base URL of the analysis service (POST /analyze), such as deployments/grok.
	GrokServiceURL string `yaml:"grokServiceUrl"`
	// Timeout for the analysis service (default 30s).
	GrokTimeout time.Duration `yaml:"grokTimeout"`
	// POST new anomalies to alertWebhookUrl.
	EnableAlerts bool `yaml:"enableAlerts"`
	// Webhook for anomaly alerts (JSON with a Slack-compatible "text").
	AlertWebhookURL string `yaml:"alertWebhookUrl"`
	// Lowest severity that is sent: low, medium (default), high or critical.
	AlertMinSeverity string `yaml:"alertMinSeverity"`
	// With MongoDB, how long anomalies are kept (default 30 days).
	RetentionDays int `yaml:"retentionDays"`
	// Added to every anomaly, for example the environment.
	Tags map[string]string `yaml:"tags"`
}

type ServiceConfig struct {
	// Unique service name; used in metrics, logs and rate-limit keys.
	Name string `yaml:"name"`
	// Path prefix routed to this service, e.g. /api/users (matches /api/users and /api/users/...).
	BasePath string `yaml:"basePath"`
	// Upstream base URLs, http(s)://host[:port][/path]. Requests are spread across them.
	Targets []string `yaml:"targets"`
	// Remove basePath before forwarding (/api/users/7 is sent as /7).
	StripBasePath bool `yaml:"stripBasePath"`
	// How long to wait for an upstream's response headers (default 30s). Streams are not cut off.
	Timeout time.Duration `yaml:"timeout"`
	// Retries for GET, HEAD and OPTIONS (or requests with an Idempotency-Key) on errors and 502/503/504, each on another target. 0 means the default (3); -1 disables retries.
	RetryCount int `yaml:"retryCount"`
	// Delay before a retry, multiplied by the attempt number (default 1s).
	RetryDelay time.Duration `yaml:"retryDelay"`
	// Require a valid JWT (Authorization: Bearer ...) for this service.
	Authentication bool `yaml:"authentication"`
	// round-robin (default) or random.
	LoadBalancing string `yaml:"loadBalancing"`
	// Headers set on every request sent to the upstream.
	Headers  map[string]string `yaml:"headers"`
	Protocol string            `yaml:"protocol"` // http, graphql, grpc
	// Field-mapping rules that move JSON values in request and response bodies.
	Transform TransformConfig `yaml:"transform"`
	// Add data from other services to this service's JSON responses.
	Aggregation *AggregationConfig `yaml:"aggregation,omitempty"`
	// Limits for protocol: graphql.
	GraphQL *GraphQLConfig `yaml:"graphql,omitempty"`
	// Settings for protocol: grpc.
	GRPC *GRPCConfig `yaml:"grpc,omitempty"`
	// Active health checks; unhealthy targets get no traffic while a healthy one remains.
	HealthCheck *HealthCheckConfig `yaml:"healthCheck,omitempty"`
	// Other clusters (multiCluster.clusters) serving this service, or ["*"] for all of them:
	// failover targets, or extra targets with multiCluster.failoverStrategy active.
	Clusters       []string                `yaml:"clusters,omitempty"`
	Cache          *ServiceCacheConfig     `yaml:"cache,omitempty"`
	RateLimit      *ServiceRateLimitConfig `yaml:"rateLimit,omitempty"`
	CircuitBreaker *CircuitBreakerConfig   `yaml:"circuitBreaker,omitempty"`
	// Canary sends part of the traffic to other targets; see docs/canary-deployments.md.
	Canary *CanaryConfig `yaml:"canary,omitempty"`
	// Transformation rewrites requests and responses with Go templates; see
	// docs/transformations.md. (transform: holds the older field-mapping rules.)
	Transformation *TransformationConfig `yaml:"transformation,omitempty"`

	// Source is the file a service was loaded from when it is not the main config file (for
	// example services.yaml). It is not part of the YAML; the admin uses it to avoid writing
	// those services into config.yaml.
	Source string `yaml:"-" json:"-"`
}

// CanaryConfig routes a share of requests to canary targets: those with Header: HeaderValue or
// cookie CookieName=CookieValue always, and Weight percent of the rest (sticky per client IP).
type CanaryConfig struct {
	// Turn canary routing on.
	Enabled bool `yaml:"enabled"`
	// Canary upstreams.
	Targets []string `yaml:"targets"`
	// Percentage (0-100) of other clients sent to the canary, sticky per client IP.
	Weight int `yaml:"weight"`
	// Requests with this header set to headerValue always go to the canary.
	Header string `yaml:"header,omitempty"`
	// Value for header.
	HeaderValue string `yaml:"headerValue,omitempty"`
	// Requests with this cookie set to cookieValue always go to the canary.
	CookieName string `yaml:"cookieName,omitempty"`
	// Value for cookieName.
	CookieValue string `yaml:"cookieValue,omitempty"`
}

// TransformationConfig holds the template-based request and response transformations.
type TransformationConfig struct {
	// Templates for request headers, query parameters and body.
	Request *transform.RequestTransform `yaml:"request,omitempty"`
	// Templates for response headers and body.
	Response *transform.ResponseTransform `yaml:"response,omitempty"`
}

// ServiceRateLimitConfig sets a service's own rate limit. When rateLimit.enabled is true every
// service gets the global limit; enabled: false exempts a service, and limit/window override.
type ServiceRateLimitConfig struct {
	// Set false to exempt this service from the global limit.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Requests per window and client for this service; overrides rateLimit.limit.
	Limit int `yaml:"limit,omitempty"`
	// Window for limit; overrides rateLimit.duration.
	Window time.Duration `yaml:"window,omitempty"`
}

// CircuitBreakerConfig stops sending requests to a target that keeps failing. A target's
// circuit opens when, within interval, at least minRequests were made and failureRatio of them
// failed (transport error or 5xx); after timeout, up to maxRequests trial requests are let
// through, and one failure re-opens it.
type CircuitBreakerConfig struct {
	// Give each target its own circuit breaker.
	Enabled      bool          `yaml:"enabled"`
	FailureRatio float64       `yaml:"failureRatio"` // default 0.5
	MinRequests  uint32        `yaml:"minRequests"`  // default 10
	Interval     time.Duration `yaml:"interval"`     // default 60s
	Timeout      time.Duration `yaml:"timeout"`      // default 30s
	MaxRequests  uint32        `yaml:"maxRequests"`  // default 1
}

// ServiceCacheConfig tunes response caching for one service. Caching applies to every HTTP
// service when cache.enabled is true; a service can opt out with enabled: false.
type ServiceCacheConfig struct {
	// Set false to never cache this service when cache.enabled is true.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Overrides cache.ttl for this service.
	TTL time.Duration `yaml:"ttl,omitempty"`
}

type TransformConfig struct {
	// Rules applied to request bodies, in order.
	Request []TransformRule `yaml:"request"`
	// Rules applied to successful response bodies, in order.
	Response []TransformRule `yaml:"response"`
}

type TransformRule struct {
	// Source path, e.g. $.username.
	From string `yaml:"from"`
	// Destination path, e.g. $.user.name; the value is moved there.
	To string `yaml:"to"`
	// Value (JSON, or a string) used when from is missing.
	Default string `yaml:"default"`
}

type AggregationConfig struct {
	// Services to call for each response.
	Dependencies []DependencyConfig `yaml:"dependencies"`
}

type GraphQLConfig struct {
	// Maximum nesting depth (default 10).
	MaxQueryDepth int `yaml:"maxQueryDepth"`
	// Maximum number of braces, a rough size limit (default 1000).
	MaxQueryComplexity int `yaml:"maxQueryComplexity"`
	// Allow __schema and __type queries.
	EnableIntrospection bool `yaml:"enableIntrospection"`
	// Cache answers to anonymous queries (no Authorization or cookies; never mutations or
	// subscriptions; only 200s without errors that are not private or no-store), in memory.
	EnableQueryCaching bool `yaml:"enableQueryCaching"`
	// How long cached answers live (default 5m).
	CacheTTL time.Duration `yaml:"cacheTTL"`
}

type GRPCConfig struct {
	// Not used yet (gRPC transcoding is not implemented; see the roadmap).
	ProtoFiles []string `yaml:"protoFiles"`
	// Not used yet.
	ImportPaths []string `yaml:"importPaths"`
	// Not used yet.
	EnableReflection bool `yaml:"enableReflection"`
	// Largest response message in bytes (default 4 MiB).
	MaxMessageSize int `yaml:"maxMessageSize"`
	// Connect to the gRPC upstream over TLS, verified against the system roots.
	EnableTLS bool `yaml:"enableTLS"`
	// Client certificate for upstream mTLS (with tlsKeyFile).
	TLSCertFile string `yaml:"tlsCertFile"`
	// Client key for upstream mTLS.
	TLSKeyFile string `yaml:"tlsKeyFile"`
}

type DependencyConfig struct {
	// Name of the service to call (its first target is used).
	Service string `yaml:"service"`
	// Path to call, with {placeholders} filled from parameterMapping.
	Path string `yaml:"path"`
	// Values taken from this service's response to fill the path.
	ParameterMapping []MappingConfig `yaml:"parameterMapping"`
	// Where the dependency's data goes in the response (from $ takes all of it).
	ResultMapping []MappingConfig `yaml:"resultMapping"`
}

type MappingConfig struct {
	// Source: a $.path, or {name} in parameterMapping.to.
	From string `yaml:"from"`
	// Destination: a $.path, or the {placeholder} to fill.
	To string `yaml:"to"`
}

// HealthCheckConfig holds health check configuration for backend targets
type HealthCheckConfig struct {
	// Check this service's targets periodically.
	Enabled            bool          `yaml:"enabled"`
	Interval           time.Duration `yaml:"interval"`           // How often to check (default: 30s)
	Timeout            time.Duration `yaml:"timeout"`            // Request timeout (default: 5s)
	UnhealthyThreshold int           `yaml:"unhealthyThreshold"` // Failures before unhealthy (default: 3)
	HealthyThreshold   int           `yaml:"healthyThreshold"`   // Successes before healthy (default: 2)
	ExpectedStatus     []int         `yaml:"expectedStatus"`     // Expected HTTP status codes (default: [200, 204])
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify"` // Skip TLS verification
	Path               string        `yaml:"path"`               // Checked path (default: /health)
}

// SetDefaults sets default values for ServiceConfig
func (s *ServiceConfig) SetDefaults() {
	if s.LoadBalancing == "" {
		s.LoadBalancing = "round-robin"
	}
	if s.Timeout == 0 {
		s.Timeout = 30 * time.Second
	}
	if s.RetryCount == 0 {
		s.RetryCount = 3
	}
	if s.RetryDelay == 0 {
		s.RetryDelay = 1 * time.Second
	}
	if s.Protocol == "" {
		s.Protocol = "http"
	}
}

func Load(configPath string, logger *logrus.Logger) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	if data, err = ExpandEnv(data); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	warnUnknownKeys(logger, configPath, data, &Config{})

	// Set defaults
	if config.Server.Port == 0 {
		config.Server.Port = 8080
	}
	if config.Server.ReadTimeout == 0 {
		config.Server.ReadTimeout = 30 * time.Second
	}
	if config.Server.WriteTimeout == 0 {
		config.Server.WriteTimeout = 30 * time.Second
	}
	if config.Server.GracefulTimeout == 0 {
		config.Server.GracefulTimeout = 15 * time.Second
	}
	if config.Server.Timeout == 0 {
		config.Server.Timeout = 30 * time.Second
	}

	if config.Logging.Level == "" {
		config.Logging.Level = "info"
	}

	if config.Monitoring.Path == "" {
		config.Monitoring.Path = "/metrics"
	}

	// Set tracing defaults
	if config.Tracing.ServiceName == "" {
		config.Tracing.ServiceName = "odin-gateway"
	}
	if config.Tracing.ServiceVersion == "" {
		config.Tracing.ServiceVersion = "1.0.0"
	}
	if config.Tracing.Environment == "" {
		config.Tracing.Environment = "development"
	}
	if config.Tracing.Endpoint == "" {
		config.Tracing.Endpoint = "http://localhost:4318/v1/traces"
	}
	if config.Tracing.SampleRate == 0 {
		config.Tracing.SampleRate = 1.0
	}

	// Load services from external file if available
	servicesPath := filepath.Join(filepath.Dir(configPath), "services.yaml")
	if _, err := os.Stat(servicesPath); err == nil {
		servicesData, err := os.ReadFile(servicesPath)
		if err == nil {
			if servicesData, err = ExpandEnv(servicesData); err != nil {
				return nil, fmt.Errorf("%s: %w", servicesPath, err)
			}
			var servicesConfig struct {
				Services []ServiceConfig `yaml:"services"`
			}
			if err := yaml.Unmarshal(servicesData, &servicesConfig); err != nil {
				return nil, fmt.Errorf("failed to parse %s: %w", servicesPath, err)
			}
			warnUnknownKeys(logger, servicesPath, servicesData, &struct {
				Services []ServiceConfig `yaml:"services"`
			}{})
			// A service defined in both files keeps its config.yaml definition. (Older admin
			// versions copied services.yaml entries into config.yaml on every save.)
			defined := make(map[string]bool, len(config.Services))
			for _, svc := range config.Services {
				defined[svc.Name] = true
			}
			for _, svc := range servicesConfig.Services {
				if defined[svc.Name] {
					if logger != nil {
						logger.Warnf("Service %q is defined in both %s and %s; using the one in %s",
							svc.Name, configPath, servicesPath, configPath)
					}
					continue
				}
				svc.Source = servicesPath
				config.Services = append(config.Services, svc)
			}
		}
	}

	// Set defaults for services
	for i := range config.Services {
		config.Services[i].SetDefaults()
	}

	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

func validateConfig(config *Config) error {
	if config.Server.Port <= 0 || config.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", config.Server.Port)
	}

	for _, service := range config.Services {
		if service.Name == "" {
			return fmt.Errorf("service name cannot be empty")
		}
		if service.BasePath == "" {
			return fmt.Errorf("service %s: basePath cannot be empty", service.Name)
		}
		if len(service.Targets) == 0 {
			return fmt.Errorf("service %s: at least one target must be specified", service.Name)
		}
	}

	return nil
}

// EffectiveRateLimit is the limit and window the gateway applies to svc: its own rateLimit over
// the global one; a limit of 0 means none.
func EffectiveRateLimit(global RateLimitConfig, svc *ServiceRateLimitConfig) (int, time.Duration) {
	limit, window := 0, global.Duration
	if global.Enabled {
		limit = global.Limit
	}
	if svc != nil {
		if svc.Enabled != nil && !*svc.Enabled {
			return 0, 0
		}
		if svc.Limit > 0 {
			limit = svc.Limit
		}
		if svc.Window > 0 {
			window = svc.Window
		}
	}
	if window <= 0 {
		window = time.Minute
	}
	return limit, window
}

// EffectiveCache reports whether responses of svc are cached, and for how long.
func EffectiveCache(global CacheConfig, svc *ServiceCacheConfig) (bool, time.Duration) {
	if !global.Enabled {
		return false, 0
	}
	ttl := global.TTL
	if svc != nil {
		if svc.Enabled != nil && !*svc.Enabled {
			return false, 0
		}
		if svc.TTL > 0 {
			ttl = svc.TTL
		}
	}
	return true, ttl
}

// MergeServices adds stored services (from MongoDB) to the config file's. A file service wins
// over a stored one with the same name, and stored services without a name, base path or target
// are skipped; skipped says why, for the log.
func MergeServices(file, stored []ServiceConfig) (merged, added []ServiceConfig, skipped []string) {
	merged = append([]ServiceConfig(nil), file...)
	names := map[string]bool{}
	for _, s := range file {
		names[s.Name] = true
	}
	for _, s := range stored {
		switch {
		case s.Name == "" || s.BasePath == "" || len(s.Targets) == 0:
			skipped = append(skipped, fmt.Sprintf("skipping stored service %q: it needs a name, a base path and a target", s.Name))
			continue
		case names[s.Name]:
			skipped = append(skipped, fmt.Sprintf("service %s is in the config file and in MongoDB; using the config file's", s.Name))
			continue
		}
		s.SetDefaults()
		names[s.Name] = true
		merged = append(merged, s)
		added = append(added, s)
	}
	return merged, added, skipped
}

var unknownField = regexp.MustCompile(`line (\d+): field (\S+) not found in type \S+`)

// UnknownKeys lists the keys in data that into's schema does not have, as
// "line N: unknown key name". YAML is decoded leniently (unknown keys are ignored, so older and
// newer files load), which made a misspelt key silently do nothing (AUDIT COR-24).
func UnknownKeys(data []byte, into interface{}) []string {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var te *yaml.TypeError
	if err := dec.Decode(into); !errors.As(err, &te) {
		return nil
	}
	var out []string
	for _, e := range te.Errors {
		if m := unknownField.FindStringSubmatch(e); m != nil {
			out = append(out, fmt.Sprintf("line %s: unknown key %q", m[1], m[2]))
		}
	}
	return out
}

func warnUnknownKeys(logger *logrus.Logger, path string, data []byte, into interface{}) {
	if logger == nil {
		return
	}
	for _, k := range UnknownKeys(data, into) {
		logger.Warnf("%s %s: it has no effect; check its spelling against the configuration reference", path, k)
	}
}
