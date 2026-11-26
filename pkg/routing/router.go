package routing

import (
	"time"

	"odin/pkg/cache"
	"odin/pkg/middleware"
	"odin/pkg/service"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

type Router struct {
	echo           *echo.Echo
	registry       *service.Registry
	logger         *logrus.Logger
	cacheStore     cache.Store
	cacheTTL       time.Duration
	authMiddleware echo.MiddlewareFunc
	routeMW        []echo.MiddlewareFunc
	healthy        func(target string) bool
}

// SetHealthCheck lets service handlers skip targets that fail their health checks.
func (r *Router) SetHealthCheck(healthy func(target string) bool) {
	r.healthy = healthy
}

func NewRouter(e *echo.Echo, registry *service.Registry, logger *logrus.Logger) *Router {
	return &Router{
		echo:     e,
		registry: registry,
		logger:   logger,
	}
}

// SetCacheStore enables response caching on HTTP service routes with the given default TTL.
func (r *Router) SetCacheStore(store cache.Store, ttl time.Duration) {
	r.cacheStore = store
	r.cacheTTL = ttl
}

// Use adds middleware that runs on every service route, after authentication.
func (r *Router) Use(mw ...echo.MiddlewareFunc) {
	r.routeMW = append(r.routeMW, mw...)
}

func (r *Router) SetAuthMiddleware(middleware echo.MiddlewareFunc) {
	r.authMiddleware = middleware
}

func (r *Router) RegisterRoutes() error {
	// Register HTTP service routes (GraphQL and gRPC are handled separately)
	services := r.registry.GetAllServices()

	for _, svc := range services {
		// Skip non-HTTP services as they have their own handlers
		if svc.Protocol != "" && svc.Protocol != "http" {
			continue
		}

		r.logger.WithFields(logrus.Fields{
			"name":     svc.Name,
			"basePath": svc.BasePath,
			"protocol": svc.Protocol,
		}).Info("Registering HTTP service route")

		// Create service handler
		handler, err := NewServiceHandler(svc, r.logger, HandlerOptions{Healthy: r.healthy})
		if err != nil {
			return err
		}

		// Create route group
		group := r.echo.Group(svc.BasePath)

		// Authentication runs first, so nothing below (including the cache) can answer an
		// unauthenticated request.
		if svc.Authentication && r.authMiddleware != nil {
			group.Use(r.authMiddleware)
		}
		group.Use(r.routeMW...)
		if r.cacheStore != nil && !svc.CacheDisabled {
			ttl := r.cacheTTL
			if svc.CacheTTL > 0 {
				ttl = svc.CacheTTL
			}
			group.Use(middleware.NewResponseCache(r.cacheStore, middleware.ResponseCacheOptions{
				TTL:       ttl,
				KeyPrefix: svc.Name,
			}, r.logger))
		}

		// Register routes
		group.Any("", handler.Handle)
		group.Any("/*", handler.Handle)
	}

	return nil
}

func (r *Router) RegisterHealthRoutes() {
	r.echo.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]string{
			"status": "UP",
		})
	})

	r.echo.GET("/routes", func(c echo.Context) error {
		all := r.echo.Routes()
		routes := make([]map[string]string, 0, len(all))
		for _, route := range all {
			routes = append(routes, map[string]string{
				"method": route.Method,
				"path":   route.Path,
			})
		}
		return c.JSON(200, routes)
	})
}
