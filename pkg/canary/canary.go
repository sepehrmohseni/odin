package canary

import (
	"hash/fnv"
	"net/http"
	"odin/pkg/service"
	"strings"
)

// Router handles canary deployment routing decisions. It is stateless: the weight-based split
// hashes the client IP, so a client keeps landing on the same side.
type Router struct{}

// NewRouter creates a new canary router
func NewRouter() *Router {
	return &Router{}
}

// ShouldUseCanary determines if a request should be routed to canary
func (r *Router) ShouldUseCanary(req *http.Request, config *service.CanaryConfig) bool {
	if config == nil || !config.Enabled {
		return false
	}

	// Check header-based routing
	if config.Header != "" && config.HeaderValue != "" {
		headerValue := req.Header.Get(config.Header)
		if headerValue == config.HeaderValue {
			return true
		}
	}

	// Check cookie-based routing
	if config.CookieName != "" && config.CookieValue != "" {
		if cookie, err := req.Cookie(config.CookieName); err == nil {
			if cookie.Value == config.CookieValue {
				return true
			}
		}
	}

	// Weight-based routing (sticky by IP for consistent routing)
	if config.Weight > 0 && config.Weight < 100 {
		return r.shouldRouteByWeight(req, config.Weight)
	}

	return false
}

// shouldRouteByWeight determines routing based on weight and client IP for sticky sessions
func (r *Router) shouldRouteByWeight(req *http.Request, weight int) bool {
	// Get client IP for consistent hashing
	clientIP := getClientIP(req)

	// Hash the IP for a consistent, evenly spread split. (The previous version used one byte
	// of an MD5 sum, so values 0-55 were twice as likely as 56-99 and weights were skewed.)
	h := fnv.New32a()
	_, _ = h.Write([]byte(clientIP))
	percentage := int(h.Sum32() % 100)

	return percentage < weight
}

// GetTargets returns the appropriate target list based on canary decision
func (r *Router) GetTargets(req *http.Request, config *service.Config) []string {
	if config.Canary != nil && config.Canary.Enabled {
		if r.ShouldUseCanary(req, config.Canary) {
			if len(config.Canary.Targets) > 0 {
				return config.Canary.Targets
			}
		}
	}
	return config.Targets
}

// getClientIP extracts the client IP from the request
func getClientIP(req *http.Request) string {
	// Check X-Forwarded-For header
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP in the list
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP header
	if xri := req.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fallback to RemoteAddr
	ip := req.RemoteAddr
	// Remove port if present
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}

	return ip
}
