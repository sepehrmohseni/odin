package plugins

import (
	"errors"
	"fmt"
	"net/http"
	"plugin"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// Phase is where a middleware plugin runs in a service's request pipeline:
//
//	service label -> pre-auth -> JWT check -> post-auth -> rate limit -> cache -> pre-proxy -> proxy
//
// Middlewares in a phase run in priority order (lower first, then by name). They run on the
// routes of HTTP services only, never on the gateway's own endpoints (/admin, /health, ...).
type Phase string

const (
	// PhasePreAuth runs before the service's JWT check: custom authentication plugins.
	PhasePreAuth Phase = "pre-auth"
	// PhasePostAuth runs after authentication, before rate limiting and the cache (the
	// default).
	PhasePostAuth Phase = "post-auth"
	// PhasePreProxy runs after the cache, right before the request is proxied; it sees every
	// request that reaches an upstream and can wrap its response.
	PhasePreProxy Phase = "pre-proxy"
)

// Phases lists the phases in pipeline order.
var Phases = []Phase{PhasePreAuth, PhasePostAuth, PhasePreProxy}

// NormalizePhase maps a phase name, including the names stored by earlier versions, to a
// Phase. An empty name is post-auth; an unknown one is an error.
func NormalizePhase(name string) (Phase, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "post-auth", "access", "post-routing", "pre-route", "post-route":
		return PhasePostAuth, nil
	case "pre-auth", "pre-routing", "auth":
		return PhasePreAuth, nil
	case "pre-proxy", "pre-response", "proxy":
		return PhasePreProxy, nil
	}
	return "", fmt.Errorf("unknown middleware phase %q (use pre-auth, post-auth or pre-proxy)", name)
}

// PhaseMiddleware runs the middleware plugins registered for phase whose routes match the
// request path. The chain is read per request, so enabling or disabling a plugin takes effect
// on the next request without a restart.
func (pm *PluginManager) PhaseMiddleware(phase Phase) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			entries := pm.chainSnapshot()
			path := c.Request().URL.Path

			handler := next
			for i := len(entries) - 1; i >= 0; i-- {
				e := entries[i]
				if Phase(e.Phase) != phase || !pm.matchesRoute(path, e.Routes) {
					continue
				}
				handler = guard(e.Name, e.Middleware, handler, pm.logger)
			}
			return handler(c)
		}
	}
}

// chainSnapshot returns the current chain. The slice is never modified after it is published
// (see updateChain), so callers may iterate it without holding the lock.
func (pm *PluginManager) chainSnapshot() []MiddlewareEntry {
	pm.middlewareChain.mu.RLock()
	defer pm.middlewareChain.mu.RUnlock()
	return pm.middlewareChain.Middlewares
}

// guard wraps one plugin so that a panic in it becomes a 500 for that request, logged with the
// plugin's name, instead of a dropped connection.
func guard(name string, mw Middleware, next echo.HandlerFunc, logger *logrus.Logger) echo.HandlerFunc {
	var h echo.HandlerFunc
	built := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("building middleware: %v", r)
			}
		}()
		h = mw.Handle(next)
		return nil
	}
	if err := built(); err != nil || h == nil {
		return func(echo.Context) error {
			logger.WithField("middleware", name).Errorf("Middleware plugin is broken: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "Middleware plugin failed")
		}
	}
	return func(c echo.Context) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.WithField("middleware", name).Errorf("Middleware plugin panicked: %v", r)
				err = echo.NewHTTPError(http.StatusInternalServerError, "Middleware plugin failed")
			}
		}()
		return h(c)
	}
}

// resolveMiddleware gets a Middleware from an opened plugin. Two conventions are accepted:
//
//	func New(config map[string]interface{}) (plugins.Middleware, error)   // preferred
//	var Middleware plugins.Middleware                                      // Initialize(config) is called
func resolveMiddleware(p *plugin.Plugin, config map[string]interface{}) (Middleware, error) {
	if sym, err := p.Lookup("New"); err == nil {
		newFn, ok := sym.(func(map[string]interface{}) (Middleware, error))
		if !ok {
			return nil, fmt.Errorf("plugin's New has type %T, want func(map[string]interface{}) (plugins.Middleware, error)", sym)
		}
		mw, err := newFn(config)
		if err != nil {
			return nil, fmt.Errorf("plugin's New failed: %w", err)
		}
		if mw == nil {
			return nil, errors.New("plugin's New returned no middleware")
		}
		return mw, nil
	}

	sym, err := p.Lookup("Middleware")
	if err != nil {
		return nil, errors.New("plugin exports neither New nor Middleware")
	}
	var mw Middleware
	switch v := sym.(type) {
	case Middleware:
		mw = v
	case *Middleware:
		if v != nil {
			mw = *v
		}
	}
	if mw == nil {
		return nil, fmt.Errorf("plugin's Middleware has type %T, want plugins.Middleware", sym)
	}
	if err := mw.Initialize(config); err != nil {
		return nil, fmt.Errorf("failed to initialize middleware: %w", err)
	}
	return mw, nil
}
