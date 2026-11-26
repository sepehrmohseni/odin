package graphql

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	gocache "github.com/patrickmn/go-cache"
	"github.com/sirupsen/logrus"
)

// GraphQLRequest represents a GraphQL request
type GraphQLRequest struct {
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables,omitempty"`
	OperationName string                 `json:"operationName,omitempty"`
}

// GraphQLResponse represents a GraphQL response
type GraphQLResponse struct {
	Data   interface{}    `json:"data,omitempty"`
	Errors []GraphQLError `json:"errors,omitempty"`
}

// GraphQLError represents a GraphQL error
type GraphQLError struct {
	Message    string                 `json:"message"`
	Locations  []GraphQLLocation      `json:"locations,omitempty"`
	Path       []interface{}          `json:"path,omitempty"`
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// GraphQLLocation represents a location in a GraphQL query
type GraphQLLocation struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// ProxyConfig holds GraphQL proxy configuration
type ProxyConfig struct {
	Endpoint            string        `yaml:"endpoint"`
	MaxQueryDepth       int           `yaml:"maxQueryDepth"`
	MaxQueryComplexity  int           `yaml:"maxQueryComplexity"`
	EnableIntrospection bool          `yaml:"enableIntrospection"`
	Timeout             time.Duration `yaml:"timeout"`
	EnableQueryCaching  bool          `yaml:"enableQueryCaching"`
	CacheTTL            time.Duration `yaml:"cacheTTL"`
}

// Proxy handles GraphQL requests and forwards them to backend services
type Proxy struct {
	config *ProxyConfig
	logger *logrus.Logger
	client *http.Client
	cache  *gocache.Cache // nil unless EnableQueryCaching
}

// maxCachedQueries bounds the query cache.
const maxCachedQueries = 10000

// NewProxy creates a new GraphQL proxy
func NewProxy(config *ProxyConfig, logger *logrus.Logger) *Proxy {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.MaxQueryDepth == 0 {
		config.MaxQueryDepth = 10
	}
	if config.MaxQueryComplexity == 0 {
		config.MaxQueryComplexity = 1000
	}
	if config.CacheTTL == 0 {
		config.CacheTTL = 5 * time.Minute
	}

	p := &Proxy{
		config: config,
		logger: logger,
		client: &http.Client{
			Timeout: config.Timeout,
		},
	}
	if config.EnableQueryCaching {
		p.cache = gocache.New(config.CacheTTL, time.Minute)
	}
	return p
}

// Handle processes GraphQL requests
func (p *Proxy) Handle(c echo.Context) error {
	// Only handle POST requests for GraphQL
	if c.Request().Method != http.MethodPost {
		return c.JSON(http.StatusMethodNotAllowed, map[string]string{
			"error": "GraphQL endpoint only accepts POST requests",
		})
	}

	// Parse GraphQL request
	var req GraphQLRequest
	if err := c.Bind(&req); err != nil {
		p.logger.WithError(err).Error("Failed to parse GraphQL request")
		return c.JSON(http.StatusBadRequest, GraphQLResponse{
			Errors: []GraphQLError{{
				Message: "Invalid GraphQL request format",
			}},
		})
	}

	// Validate query
	if req.Query == "" {
		return c.JSON(http.StatusBadRequest, GraphQLResponse{
			Errors: []GraphQLError{{
				Message: "Query is required",
			}},
		})
	}

	// Check if introspection query and if it's allowed
	if !p.config.EnableIntrospection && p.isIntrospectionQuery(req.Query) {
		return c.JSON(http.StatusForbidden, GraphQLResponse{
			Errors: []GraphQLError{{
				Message: "Introspection is disabled",
			}},
		})
	}

	// Validate query complexity and depth
	if err := p.validateQuery(req.Query); err != nil {
		return c.JSON(http.StatusBadRequest, GraphQLResponse{
			Errors: []GraphQLError{{
				Message: err.Error(),
			}},
		})
	}

	cacheKey := ""
	if p.cache != nil && cacheable(c.Request(), &req) {
		cacheKey = queryKey(&req)
		if hit, ok := p.cache.Get(cacheKey); ok {
			if r, ok := hit.(*upstreamResponse); ok {
				c.Response().Header().Set("X-Cache", "HIT")
				return r.write(c)
			}
		}
	}

	resp, err := p.forwardRequest(c, &req)
	if err != nil {
		p.logger.WithError(err).Error("Failed to forward GraphQL request")
		status := http.StatusBadGateway
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			status = http.StatusGatewayTimeout
		}
		return c.JSON(status, GraphQLResponse{Errors: []GraphQLError{{Message: http.StatusText(status)}}})
	}
	if cacheKey != "" {
		c.Response().Header().Set("X-Cache", "MISS")
		if resp.storable() && p.cache.ItemCount() < maxCachedQueries {
			p.cache.SetDefault(cacheKey, resp)
		}
	}
	// The upstream's status, headers and body as they are. (They used to be re-encoded with
	// status 200, which dropped error extensions and hid upstream failures.)
	return resp.write(c)
}

// upstreamResponse is a GraphQL server's answer, passed to the client unchanged.
type upstreamResponse struct {
	status int
	header http.Header
	body   []byte
}

func (r *upstreamResponse) write(c echo.Context) error {
	for k, vs := range r.header {
		for _, v := range vs {
			c.Response().Header().Add(k, v)
		}
	}
	ct := r.header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	return c.Blob(r.status, ct, r.body)
}

// storable: a 200 without GraphQL errors that the upstream did not mark private or no-store.
func (r *upstreamResponse) storable() bool {
	if r.status != http.StatusOK || r.header.Get("Set-Cookie") != "" {
		return false
	}
	cc := strings.ToLower(r.header.Get("Cache-Control"))
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "private") || strings.Contains(cc, "no-cache") {
		return false
	}
	var parsed struct {
		Errors []json.RawMessage `json:"errors"`
	}
	return json.Unmarshal(r.body, &parsed) == nil && len(parsed.Errors) == 0
}

var mutationOrSubscription = regexp.MustCompile(`(?i)\b(mutation|subscription)\b`)

// cacheable: only anonymous queries are cached. A request with credentials may return
// per-user data, and mutations and subscriptions must always reach the server.
func cacheable(r *http.Request, req *GraphQLRequest) bool {
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
		return false
	}
	return !mutationOrSubscription.MatchString(req.Query)
}

func queryKey(req *GraphQLRequest) string {
	vars, _ := json.Marshal(req.Variables) // map keys are sorted
	sum := sha256.Sum256([]byte(req.OperationName + "\x00" + req.Query + "\x00" + string(vars)))
	return hex.EncodeToString(sum[:])
}

// hopByHop headers belong to one connection and are not forwarded.
var hopByHop = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHopByHop(h http.Header) {
	for _, f := range h.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range hopByHop {
		h.Del(name)
	}
}

// forwardRequest sends the query upstream with the client's headers (Authorization and the
// rest; they used to be dropped, so authenticated GraphQL servers saw anonymous requests).
func (p *Proxy) forwardRequest(c echo.Context, req *GraphQLRequest) (*upstreamResponse, error) {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	in := c.Request()
	httpReq, err := http.NewRequestWithContext(in.Context(), http.MethodPost, p.config.Endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header = in.Header.Clone()
	stripHopByHop(httpReq.Header)
	httpReq.Header.Del("Content-Length")
	httpReq.Header.Del("Accept-Encoding") // let the transport negotiate and decode compression
	httpReq.Header.Set("Content-Type", "application/json")
	if httpReq.Header.Get("Accept") == "" {
		httpReq.Header.Set("Accept", "application/json")
	}
	prior := in.Header.Get("X-Forwarded-For")
	if prior != "" {
		prior += ", "
	}
	httpReq.Header.Set("X-Forwarded-For", prior+c.RealIP())
	httpReq.Header.Set("X-Forwarded-Host", in.Host)
	httpReq.Header.Set("X-Forwarded-Proto", c.Scheme())

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	header := httpResp.Header.Clone()
	stripHopByHop(header)
	header.Del("Content-Length")
	return &upstreamResponse{status: httpResp.StatusCode, header: header, body: body}, nil
}

// introspection matches the schema introspection fields, __schema and __type(...). __typename
// is not introspection: clients such as Apollo add it to every query, and counting it (it also
// matched __type as a substring) broke them whenever introspection was off.
var introspection = regexp.MustCompile(`\b__schema\b|\b__type\s*\(`)

// isIntrospectionQuery checks if the query is an introspection query
func (p *Proxy) isIntrospectionQuery(query string) bool {
	return introspection.MatchString(query)
}

// validateQuery performs basic validation on the GraphQL query
func (p *Proxy) validateQuery(query string) error {
	// Simple depth check by counting nested braces
	depth := 0
	maxDepth := 0

	for _, char := range query {
		switch char {
		case '{':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
		case '}':
			depth--
		}
	}

	if maxDepth > p.config.MaxQueryDepth {
		return fmt.Errorf("query depth %d exceeds maximum allowed depth %d", maxDepth, p.config.MaxQueryDepth)
	}

	// Simple complexity check by counting fields (approximate)
	fieldCount := strings.Count(query, "{") + strings.Count(query, "}")
	if fieldCount > p.config.MaxQueryComplexity {
		return fmt.Errorf("query complexity %d exceeds maximum allowed complexity %d", fieldCount, p.config.MaxQueryComplexity)
	}

	return nil
}

// RegisterRoutes registers GraphQL proxy routes. mw (for example the authentication
// middleware of a service with authentication: true) runs before the handler.
func (p *Proxy) RegisterRoutes(e *echo.Echo, basePath string, mw ...echo.MiddlewareFunc) {
	e.POST(basePath, p.Handle, mw...)
	e.GET(basePath, func(c echo.Context) error {
		// GraphQL Playground or similar can be served here
		return c.JSON(http.StatusOK, map[string]string{
			"message": "GraphQL endpoint is available at POST " + basePath,
		})
	}, mw...)
}
