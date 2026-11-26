package routing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"odin/pkg/canary"
	"odin/pkg/circuit"
	"odin/pkg/service"
	"odin/pkg/tracing"
	"odin/pkg/transform"
	"odin/pkg/websocket"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// maxBufferedResponse bounds a response body that has to be read into memory (to apply response
// transformations). Other responses are streamed.
const maxBufferedResponse = 10 << 20

// HandlerOptions carries what a ServiceHandler needs from the rest of the gateway.
type HandlerOptions struct {
	// Healthy reports whether a target passes its health checks. Unhealthy targets are skipped
	// while a healthy one remains. Nil means every target is healthy.
	Healthy func(target string) bool
	// Transport overrides the upstream transport (tests).
	Transport http.RoundTripper
	// Enrich adds aggregation dependency data to a JSON response body (services with an
	// aggregation section). Nil disables enrichment.
	Enrich func(ctx context.Context, serviceName string, body []byte, authorization string) ([]byte, error)
}

// ServiceHandler forwards requests for one HTTP service to its targets.
type ServiceHandler struct {
	service         *service.Config
	logger          *logrus.Logger
	transport       http.RoundTripper
	targets         map[string]*url.URL // parsed targets, including canary targets
	nextTarget      atomic.Uint64
	canaryRouter    *canary.Router
	transformEngine *transform.Engine
	healthy         func(string) bool
	breakers        map[string]*circuit.CircuitBreaker // per target, when configured
	enrich          func(ctx context.Context, serviceName string, body []byte, authorization string) ([]byte, error)
	wsProxy         *websocket.Proxy
}

// errUpstream5xx marks a 5xx response as a failure for the circuit breaker.
var errUpstream5xx = errors.New("upstream returned a server error")

// NewServiceHandler validates the service's targets and prepares its upstream transport.
func NewServiceHandler(svc *service.Config, logger *logrus.Logger, opts HandlerOptions) (*ServiceHandler, error) {
	if len(svc.Targets) == 0 {
		return nil, fmt.Errorf("service %s has no targets", svc.Name)
	}

	targets := make(map[string]*url.URL)
	all := append([]string(nil), svc.Targets...)
	if svc.Canary != nil {
		all = append(all, svc.Canary.Targets...)
	}
	for _, t := range all {
		u, err := url.Parse(t)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("service %s: invalid target %q (want http(s)://host[:port][/path])", svc.Name, t)
		}
		targets[t] = u
	}

	transport := opts.Transport
	if transport == nil {
		transport = newUpstreamTransport(svc.Timeout)
	}
	healthy := opts.Healthy
	if healthy == nil {
		healthy = func(string) bool { return true }
	}

	var breakers map[string]*circuit.CircuitBreaker
	if cb := svc.CircuitBreaker; cb != nil {
		breakers = make(map[string]*circuit.CircuitBreaker, len(targets))
		for t := range targets {
			breakers[t] = circuit.NewCircuitBreaker(svc.Name+" "+t, circuit.Config{
				MaxRequests:  cb.MaxRequests,
				Interval:     cb.Interval,
				Timeout:      cb.Timeout,
				FailureRatio: cb.FailureRatio,
				MinRequests:  cb.MinRequests,
				// The client giving up is not the upstream's fault.
				IsSuccessful: func(err error) bool { return err == nil || errors.Is(err, context.Canceled) },
			}, logger)
		}
	}

	return &ServiceHandler{
		service:         svc,
		logger:          logger,
		transport:       transport,
		targets:         targets,
		canaryRouter:    canary.NewRouter(),
		transformEngine: transform.NewEngine(logger),
		healthy:         healthy,
		breakers:        breakers,
		enrich:          opts.Enrich,
		wsProxy:         websocket.NewProxy(websocket.Config{}, logger),
	}, nil
}

// newUpstreamTransport pools connections per service. The service timeout bounds the wait for
// the upstream's response headers rather than the whole exchange, so streamed responses
// (downloads, server-sent events) are not cut off midway.
func newUpstreamTransport(timeout time.Duration) *http.Transport {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
}

// Handle proxies one request.
func (h *ServiceHandler) Handle(c echo.Context) error {
	req := c.Request()
	if websocket.IsUpgradeRequest(req) {
		return h.proxyWebSocket(c)
	}

	ctx := req.Context()
	bufferResponse := h.rewritesResponse()
	retries := h.retriesFor(req)

	// The body is read into memory only when it may have to be sent more than once (retries)
	// or rewritten; otherwise it is streamed to the upstream. Its size is already bounded by
	// the gateway's body limit.
	var body []byte
	bufferBody := retries > 0 || h.rewritesRequestBody()
	if bufferBody && req.Body != nil && req.Body != http.NoBody {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Failed to read request body")
		}
	}
	if rules := legacyRules(h.service.Transform.Request); len(rules) > 0 {
		if mapped, ok := transform.ApplyMappings(body, rules); ok {
			body = mapped
		}
	}

	tried := make(map[string]bool)
	var resp *http.Response
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, h.service.RetryDelay*time.Duration(attempt)); err != nil {
				return nil // the client went away
			}
		}

		target := h.selectTarget(req, tried)
		tried[target] = true

		outReq, err := h.buildRequest(c, target, body, bufferBody, bufferResponse)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create proxy request")
		}

		h.logger.WithFields(logrus.Fields{
			"service": h.service.Name,
			"target":  outReq.URL.Redacted(),
			"method":  outReq.Method,
			"attempt": attempt + 1,
		}).Debug("Forwarding request")

		resp, lastErr = h.roundTrip(target, outReq)
		if lastErr != nil {
			if ctx.Err() != nil {
				return nil // the client went away
			}
			h.logger.WithError(lastErr).WithField("service", h.service.Name).Warnf(
				"Request to %s failed (attempt %d/%d)", target, attempt+1, retries+1)
			continue
		}
		if retryableStatus(resp.StatusCode) && attempt < retries {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			resp = nil
			continue
		}
		break
	}

	if resp == nil {
		return upstreamError(lastErr)
	}
	defer func() { _ = resp.Body.Close() }()

	if bufferResponse {
		return h.writeTransformed(c, resp)
	}
	return h.writeStreamed(c, resp)
}

// rewritesResponse reports whether responses must be buffered to be rewritten: template
// transformations, field-mapping rules or aggregation enrichment.
func (h *ServiceHandler) rewritesResponse() bool {
	if t := h.service.Transformation; t != nil && t.Response != nil && (t.Response.Body != "" || len(t.Response.Headers) > 0) {
		return true
	}
	if len(h.service.Transform.Response) > 0 {
		return true
	}
	return h.enrich != nil && h.service.Aggregation != nil && len(h.service.Aggregation.Dependencies) > 0
}

func (h *ServiceHandler) rewritesRequestBody() bool {
	if t := h.service.Transformation; t != nil && t.Request != nil && t.Request.Body != "" {
		return true
	}
	return len(h.service.Transform.Request) > 0
}

func legacyRules(rules []service.TransformRule) []transform.MappingRule {
	out := make([]transform.MappingRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, transform.MappingRule{From: r.From, To: r.To, Default: r.Default})
	}
	return out
}

// proxyWebSocket relays a WebSocket connection to a target of the service.
func (h *ServiceHandler) proxyWebSocket(c echo.Context) error {
	req := c.Request()
	target := h.selectTarget(req, map[string]bool{})
	u, err := h.upstreamURL(req, target)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "Invalid upstream URL")
	}
	extra := http.Header{}
	h.setForwardingHeaders(c, req.Header, extra)
	return h.wsProxy.Proxy(c, u.String(), extra)
}

// retriesFor returns how many times a request may be retried. Only requests that are safe to
// repeat are retried: GET, HEAD and OPTIONS, or any method carrying an Idempotency-Key header.
// (All methods used to be retried, so a POST that timed out could be applied several times.)
func (h *ServiceHandler) retriesFor(req *http.Request) int {
	if h.service.RetryCount <= 0 {
		return 0
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return h.service.RetryCount
	}
	if req.Header.Get("Idempotency-Key") != "" {
		return h.service.RetryCount
	}
	return 0
}

func retryableStatus(code int) bool {
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// roundTrip sends one attempt, through the target's circuit breaker when there is one.
func (h *ServiceHandler) roundTrip(target string, req *http.Request) (*http.Response, error) {
	cb := h.breakers[target]
	if cb == nil {
		return h.transport.RoundTrip(req)
	}
	var resp *http.Response
	_, err := cb.Execute(func() (interface{}, error) {
		r, err := h.transport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		resp = r
		if r.StatusCode >= http.StatusInternalServerError {
			return nil, errUpstream5xx
		}
		return nil, nil
	})
	if errors.Is(err, errUpstream5xx) {
		return resp, nil
	}
	return resp, err
}

// available reports whether a target may be tried: healthy, and its circuit not open.
func (h *ServiceHandler) available(target string) bool {
	if !h.healthy(target) {
		return false
	}
	if cb := h.breakers[target]; cb != nil && cb.State() == circuit.StateOpen {
		return false
	}
	return true
}

func upstreamError(err error) error {
	if errors.Is(err, circuit.ErrCircuitOpen) || errors.Is(err, circuit.ErrTooManyRequests) {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "Service temporarily unavailable (circuit open)")
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return echo.NewHTTPError(http.StatusGatewayTimeout, "Upstream timed out")
	}
	return echo.NewHTTPError(http.StatusBadGateway, "Service unavailable")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// selectTarget picks a target for this attempt: canary or production per the canary rules, then
// healthy targets not tried yet in this request, then load balancing among those.
func (h *ServiceHandler) selectTarget(req *http.Request, tried map[string]bool) string {
	candidates := h.canaryRouter.GetTargets(req, h.service)

	pick := make([]string, 0, len(candidates))
	for _, t := range candidates {
		if !tried[t] && h.available(t) {
			pick = append(pick, t)
		}
	}
	if len(pick) == 0 { // everything tried or unhealthy: still try an untried one
		for _, t := range candidates {
			if !tried[t] {
				pick = append(pick, t)
			}
		}
	}
	if len(pick) == 0 {
		pick = candidates
	}
	if len(pick) == 1 {
		return pick[0]
	}

	switch h.service.LoadBalancing {
	case "random":
		return pick[rand.IntN(len(pick))] //nolint:gosec // load balancing, not security
	default: // round-robin
		n := h.nextTarget.Add(1) - 1
		return pick[n%uint64(len(pick))]
	}
}

// hopByHopHeaders apply to a single connection and must not be forwarded (RFC 9110 7.6.1).
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopByHop(h http.Header) {
	for _, field := range h.Values("Connection") {
		for _, name := range strings.Split(field, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}

// upstreamURL maps the request onto a target, keeping the escaped form of the path (so %2F and
// friends survive) and joining the target's own path without doubling slashes.
func (h *ServiceHandler) upstreamURL(req *http.Request, target string) (*url.URL, error) {
	base, ok := h.targets[target]
	if !ok {
		var err error
		if base, err = url.Parse(target); err != nil {
			return nil, err
		}
	}

	escaped := req.URL.EscapedPath()
	if h.service.StripBasePath {
		prefix := strings.TrimSuffix(h.service.BasePath, "/")
		if escaped == prefix || strings.HasPrefix(escaped, prefix+"/") {
			escaped = strings.TrimPrefix(escaped, prefix)
		}
		if escaped == "" {
			escaped = "/"
		}
	}

	joined := joinURLPath(base.EscapedPath(), escaped)
	unescaped, err := url.PathUnescape(joined)
	if err != nil {
		return nil, err
	}

	u := *base
	u.Path = unescaped
	u.RawPath = joined
	switch {
	case base.RawQuery == "":
		u.RawQuery = req.URL.RawQuery
	case req.URL.RawQuery == "":
		u.RawQuery = base.RawQuery
	default:
		u.RawQuery = base.RawQuery + "&" + req.URL.RawQuery
	}
	return &u, nil
}

func joinURLPath(a, b string) string {
	switch {
	case a == "" || a == "/":
		return b
	case strings.HasSuffix(a, "/") && strings.HasPrefix(b, "/"):
		return a + b[1:]
	case !strings.HasSuffix(a, "/") && !strings.HasPrefix(b, "/"):
		return a + "/" + b
	}
	return a + b
}

func (h *ServiceHandler) buildRequest(c echo.Context, target string, body []byte, buffered, bufferResponse bool) (*http.Request, error) {
	in := c.Request()
	ctx := in.Context()

	u, err := h.upstreamURL(in, target)
	if err != nil {
		return nil, err
	}

	var reqBody io.Reader
	switch {
	case buffered && len(body) > 0:
		reqBody = bytes.NewReader(body)
	case !buffered && in.Body != nil && in.Body != http.NoBody:
		reqBody = in.Body
	}

	out, err := http.NewRequestWithContext(ctx, in.Method, u.String(), reqBody)
	if err != nil {
		return nil, err
	}
	if !buffered {
		out.ContentLength = in.ContentLength
	}

	out.Header = in.Header.Clone()
	removeHopByHop(out.Header)
	if bufferResponse {
		// Let the transport negotiate (and transparently decode) compression, so response
		// transformations see plain bytes.
		out.Header.Del("Accept-Encoding")
	}

	h.setForwardingHeaders(c, in.Header, out.Header)

	// Join the caller's trace.
	tracing.InjectHTTP(ctx, out.Header)

	if t := h.service.Transformation; t != nil && t.Request != nil {
		if err := h.transformEngine.TransformRequest(out, t.Request); err != nil {
			h.logger.WithError(err).WithField("service", h.service.Name).Warn("Failed to transform request")
		} else if out.Body != nil && out.ContentLength >= 0 {
			// The body may have been replaced; let the transport recompute framing.
			out.Header.Del("Content-Length")
		}
	}
	return out, nil
}

// setForwardingHeaders sets X-Forwarded-*, X-Real-IP and the service's static headers on out.
// X-Forwarded-For is only extended when the request came through a trusted proxy (the echo IP
// extractor already decided that); otherwise a client could prepend addresses of its choosing.
func (h *ServiceHandler) setForwardingHeaders(c echo.Context, in, out http.Header) {
	req := c.Request()
	clientIP := c.RealIP()
	remoteIP := req.RemoteAddr
	if host, _, err := net.SplitHostPort(remoteIP); err == nil {
		remoteIP = host
	}
	if prior := in.Get("X-Forwarded-For"); prior != "" && clientIP != remoteIP {
		out.Set("X-Forwarded-For", prior+", "+remoteIP)
	} else {
		out.Set("X-Forwarded-For", clientIP)
	}
	out.Set("X-Real-IP", clientIP)
	out.Set("X-Forwarded-Proto", c.Scheme())
	out.Set("X-Forwarded-Host", req.Host)

	// Static headers from the service config (these were configured but never sent).
	for k, v := range h.service.Headers {
		out.Set(k, v)
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vals := range src {
		dst[k] = append(dst[k], vals...)
	}
}

// writeStreamed copies the upstream response through as it arrives, flushing when the response
// has no fixed length (chunked or server-sent events).
func (h *ServiceHandler) writeStreamed(c echo.Context, resp *http.Response) error {
	removeHopByHop(resp.Header)
	copyResponseHeaders(c.Response().Header(), resp.Header)
	c.Response().WriteHeader(resp.StatusCode)

	flush := resp.ContentLength < 0 || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := c.Response().Write(buf[:n]); err != nil {
				return nil // client went away; nothing left to report
			}
			if flush {
				c.Response().Flush()
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			h.logger.WithError(readErr).WithField("service", h.service.Name).Warn("Upstream response ended early")
			return nil // headers are sent; the client sees a truncated body
		}
	}
}

// writeTransformed reads the whole response, applies the response transformation and writes
// it with a correct Content-Length.
func (h *ServiceHandler) writeTransformed(c echo.Context, resp *http.Response) error {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBufferedResponse+1))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "Failed to read upstream response")
	}
	if len(data) > maxBufferedResponse {
		return echo.NewHTTPError(http.StatusBadGateway, "Upstream response too large to transform")
	}

	headers := resp.Header
	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	if rules := legacyRules(h.service.Transform.Response); len(rules) > 0 && success {
		if mapped, ok := transform.ApplyMappings(data, rules); ok {
			data = mapped
		}
	}
	if h.enrich != nil && h.service.Aggregation != nil && success {
		enriched, err := h.enrich(c.Request().Context(), h.service.Name, data, c.Request().Header.Get("Authorization"))
		if err != nil {
			h.logger.WithError(err).WithField("service", h.service.Name).Warn("Failed to enrich response")
		} else {
			data = enriched
		}
	}
	if t := h.service.Transformation; t != nil && t.Response != nil {
		newBody, newHeaders, err := h.transformEngine.TransformResponse(data, resp.StatusCode, resp.Header, t.Response)
		if err != nil {
			h.logger.WithError(err).WithField("service", h.service.Name).Warn("Failed to transform response")
		} else {
			data, headers = newBody, newHeaders
		}
	}

	removeHopByHop(headers)
	headers.Del("Content-Length")
	copyResponseHeaders(c.Response().Header(), headers)
	c.Response().Header().Set("Content-Length", strconv.Itoa(len(data)))
	c.Response().WriteHeader(resp.StatusCode)
	if c.Request().Method == http.MethodHead {
		return nil
	}
	_, err = c.Response().Write(data)
	return err
}
