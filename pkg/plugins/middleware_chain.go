package plugins

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// RegisterMiddleware adds a middleware to the chain, or replaces the one with the same name,
// with its priority (lower runs first), the routes it applies to and its phase (see Phase).
func (pm *PluginManager) RegisterMiddleware(name string, middleware Middleware, priority int, routes []string, phase string) error {
	normalized, err := NormalizePhase(phase)
	if err != nil {
		return err
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.middlewares[name] = middleware

	entry := MiddlewareEntry{
		Name:       name,
		Middleware: middleware,
		Priority:   priority,
		Routes:     append([]string(nil), routes...),
		Phase:      string(normalized),
	}
	pm.updateChain(func(chain []MiddlewareEntry) []MiddlewareEntry {
		out := make([]MiddlewareEntry, 0, len(chain)+1)
		for _, e := range chain {
			if e.Name != name {
				out = append(out, e)
			}
		}
		return append(out, entry)
	})

	pm.logger.WithFields(logrus.Fields{
		"middleware": name,
		"priority":   priority,
		"routes":     routes,
		"phase":      normalized,
	}).Info("Middleware registered")

	return nil
}

// UnregisterMiddleware removes a middleware from the chain
func (pm *PluginManager) UnregisterMiddleware(name string) error {
	pm.mu.Lock()
	middleware, exists := pm.middlewares[name]
	if exists {
		delete(pm.middlewares, name)
		pm.updateChain(func(chain []MiddlewareEntry) []MiddlewareEntry {
			out := make([]MiddlewareEntry, 0, len(chain))
			for _, e := range chain {
				if e.Name != name {
					out = append(out, e)
				}
			}
			return out
		})
	}
	pm.mu.Unlock()

	if !exists {
		return nil // Already unregistered
	}
	// Plugin code runs outside the manager's lock.
	if err := middleware.Cleanup(); err != nil {
		pm.logger.WithError(err).WithField("middleware", name).Warn("Middleware cleanup failed")
	}
	pm.logger.WithField("middleware", name).Info("Middleware unregistered")
	return nil
}

// updateChain publishes a new chain built by fn from the current one. The published slice is
// never modified afterwards, so requests can iterate a snapshot without locking; it used to be
// appended to and sorted in place while requests were reading it.
func (pm *PluginManager) updateChain(fn func([]MiddlewareEntry) []MiddlewareEntry) {
	pm.middlewareChain.mu.Lock()
	defer pm.middlewareChain.mu.Unlock()
	next := fn(pm.middlewareChain.Middlewares)
	sort.SliceStable(next, func(i, j int) bool {
		if next[i].Priority != next[j].Priority {
			return next[i].Priority < next[j].Priority
		}
		return next[i].Name < next[j].Name
	})
	pm.middlewareChain.Middlewares = next
}

// LoadMiddlewareWithChain loads a middleware plugin and adds it to the middleware chain
func (pm *PluginManager) LoadMiddlewareWithChain(name, path string, config map[string]interface{}, priority int, routes []string, phase string) error {
	// First load the middleware using existing LoadMiddleware method
	if err := pm.LoadMiddleware(name, path, config); err != nil {
		return err
	}

	// Then register it in the middleware chain
	middleware, exists := pm.GetMiddleware(name)
	if !exists {
		return fmt.Errorf("middleware %s was loaded but not found", name)
	}

	return pm.RegisterMiddleware(name, middleware, priority, routes, phase)
}

// GetMiddlewareChain returns the current middleware chain
func (pm *PluginManager) GetMiddlewareChain() []MiddlewareEntry {
	pm.middlewareChain.mu.RLock()
	defer pm.middlewareChain.mu.RUnlock()

	// Return a copy to prevent external modification
	chain := make([]MiddlewareEntry, len(pm.middlewareChain.Middlewares))
	copy(chain, pm.middlewareChain.Middlewares)
	return chain
}

// UpdateMiddlewarePriority changes the priority of a middleware and resorts the chain
func (pm *PluginManager) UpdateMiddlewarePriority(name string, newPriority int) error {
	found := false
	pm.updateChain(func(chain []MiddlewareEntry) []MiddlewareEntry {
		out := append([]MiddlewareEntry(nil), chain...)
		for i := range out {
			if out[i].Name == name {
				out[i].Priority = newPriority
				found = true
			}
		}
		return out
	})
	if !found {
		return fmt.Errorf("middleware %s not found in chain", name)
	}
	pm.logger.WithFields(logrus.Fields{"middleware": name, "priority": newPriority}).Info("Middleware priority updated")
	return nil
}

// UpdateMiddlewareRoutes changes the routes a middleware applies to
func (pm *PluginManager) UpdateMiddlewareRoutes(name string, routes []string) error {
	found := false
	pm.updateChain(func(chain []MiddlewareEntry) []MiddlewareEntry {
		out := append([]MiddlewareEntry(nil), chain...)
		for i := range out {
			if out[i].Name == name {
				out[i].Routes = append([]string(nil), routes...)
				found = true
			}
		}
		return out
	})
	if !found {
		return fmt.Errorf("middleware %s not found in chain", name)
	}
	pm.logger.WithFields(logrus.Fields{"middleware": name, "routes": routes}).Info("Middleware routes updated")
	return nil
}

// DynamicMiddleware runs every phase's middlewares in pipeline order. The gateway mounts the
// phases separately (PhaseMiddleware); this remains for callers that want them all at one point.
func (pm *PluginManager) DynamicMiddleware() echo.MiddlewareFunc {
	pre, post, proxy := pm.PhaseMiddleware(PhasePreAuth), pm.PhaseMiddleware(PhasePostAuth), pm.PhaseMiddleware(PhasePreProxy)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return pre(post(proxy(next)))
	}
}

// matchesRoute checks if a request path matches any of the route patterns
func (pm *PluginManager) matchesRoute(requestPath string, routePatterns []string) bool {
	if len(routePatterns) == 0 {
		return false
	}

	for _, pattern := range routePatterns {
		// Handle wildcard "*" - matches all routes
		if pattern == "*" {
			return true
		}

		// Handle exact match
		if pattern == requestPath {
			return true
		}

		// Prefix match with wildcard: "/api/*" matches /api and /api/..., not /apiary.
		if strings.HasSuffix(pattern, "/*") {
			prefix := strings.TrimSuffix(pattern, "/*")
			if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") || prefix == "" {
				return true
			}
		}

		// Handle glob-style match using filepath.Match
		matched, err := path.Match(pattern, requestPath)
		if err == nil && matched {
			return true
		}
	}

	return false
}
