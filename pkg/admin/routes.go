package admin

import (
	"fmt"
	"time"

	"odin/pkg/config"

	"github.com/labstack/echo/v4"
)

func (h *AdminHandler) Register(e *echo.Echo) {
	// Serve static assets (old assets directory)
	e.Static("/admin/assets", "pkg/admin/assets")

	// Serve new static files (CSS, JS)
	e.Static("/static", "pkg/admin/static")

	h.initTemplates()
	// Plugin pages render with c.Render; there was no renderer, so they answered 500.
	if e.Renderer == nil {
		e.Renderer = pageRenderer{}
	}

	adminGroup := e.Group("/admin")

	adminGroup.GET("", h.handleLogin)
	adminGroup.GET("/login", h.handleLogin)
	adminGroup.POST("/login", h.handleLoginPost)
	adminGroup.POST("/logout", h.handleLogout)
	adminGroup.GET("/logout", h.handleLogout)

	protected := adminGroup.Group("")
	protected.Use(h.basicAuthMiddleware)

	protected.GET("/dashboard", h.handleDashboard)

	// Monitoring routes
	protected.GET("/monitoring", h.handleMonitoring)
	protected.GET("/api/monitoring/metrics", GetMetricsAPI)
	protected.GET("/ws/monitoring", h.webSocketMonitoring)

	// Traces routes
	protected.GET("/traces", h.handleTraces)

	// Middleware chain management
	protected.GET("/middleware-chain", h.handleMiddlewareChain)

	protected.GET("/services", h.handleListServices)
	protected.GET("/services/new", h.handleNewService)
	protected.POST("/services", h.handleAddService)
	protected.GET("/services/:name", h.handleEditService)
	protected.POST("/services/:name", h.handleUpdateService)
	protected.DELETE("/services/:name", h.handleDeleteService)

	// Settings API routes
	settingsHandler := NewSettingsHandler(h.configPath, h.config)
	if h.cacheStore != nil {
		settingsHandler.cacheStore = h.cacheStore
	}

	protected.GET("/settings", h.handleSettings)
	protected.GET("/api/settings", settingsHandler.GetAllSettings)
	protected.GET("/api/settings/info", settingsHandler.GetGatewayInfo)
	protected.GET("/api/settings/stats", settingsHandler.GetSystemStats)

	// Server settings
	protected.GET("/api/settings/server", settingsHandler.GetServerSettings)
	protected.PUT("/api/settings/server", settingsHandler.UpdateServerSettings)

	// Logging settings
	protected.GET("/api/settings/logging", settingsHandler.GetLoggingSettings)
	protected.PUT("/api/settings/logging", settingsHandler.UpdateLoggingSettings)

	// Rate limit settings
	protected.GET("/api/settings/ratelimit", settingsHandler.GetRateLimitSettings)
	protected.PUT("/api/settings/ratelimit", settingsHandler.UpdateRateLimitSettings)

	// Cache settings
	protected.GET("/api/settings/cache", settingsHandler.GetCacheSettings)
	protected.PUT("/api/settings/cache", settingsHandler.UpdateCacheSettings)

	// Monitoring settings
	protected.GET("/api/settings/monitoring", settingsHandler.GetMonitoringSettings)
	protected.PUT("/api/settings/monitoring", settingsHandler.UpdateMonitoringSettings)

	// Tracing settings
	protected.GET("/api/settings/tracing", settingsHandler.GetTracingSettings)
	protected.PUT("/api/settings/tracing", settingsHandler.UpdateTracingSettings)

	// Config management
	protected.GET("/api/settings/export", settingsHandler.ExportConfig)
	protected.POST("/api/settings/validate", settingsHandler.ValidateConfig)
	protected.GET("/api/settings/backups", settingsHandler.GetConfigBackups)
	protected.POST("/api/settings/backups/:name/restore", settingsHandler.RestoreConfigBackup)
	protected.POST("/api/settings/cache/clear", settingsHandler.ClearCache)
	protected.POST("/api/settings/reload", settingsHandler.ReloadConfig)
	protected.GET("/api/settings/json", settingsHandler.GetConfigAsJSON)

	// Register plugin routes if plugin handler is available
	if h.pluginHandler != nil {
		h.pluginHandler.RegisterPluginRoutes(protected)
	}

	h.registerAIRoutes(protected)
	h.registerStoredServiceRoutes(protected)

	// Register middleware API routes if middleware handler is available
	if h.middlewareAPIHandler != nil {
		h.middlewareAPIHandler.RegisterMiddlewareAPIRoutes(protected)
	}

	// Register plugin upload routes if plugin upload handler is available
	if h.pluginUploadHandler != nil {
		h.pluginUploadHandler.RegisterRoutes(protected)
	}

	// Register integration routes if integration handler is available
	if h.integrationHandler != nil {
		protected.GET("/integrations/postman", h.handleIntegrationsPostman)
		h.integrationHandler.RegisterRoutes(protected)
	}

	h.logger.Info("Admin routes registered")
}

// dashboardService is one row of the dashboard's service table: what the gateway enforces for
// the service, in the words the page shows.
type dashboardService struct {
	Name, BasePath, Protocol string
	Targets                  []string
	Authentication           bool
	StripBasePath            bool
	RateLimit                string // "600 / min", empty when unlimited
	Cache                    string // "30s", empty when not cached
	HealthChecked            bool
	Timeout                  string
	Stored                   bool // from MongoDB (mongodb.loadServices), managed through its API
}

// handleDashboard shows the gateway at a glance: live traffic (filled in by the page from the
// metrics API) and the configured services. (The service list used to render without data, so
// it always said "No services configured".)
func (h *AdminHandler) handleDashboard(c echo.Context) error {
	configMu.Lock()
	cfg := *h.config
	services := append([]config.ServiceConfig(nil), h.config.Services...)
	configMu.Unlock()

	stored := map[string]bool{}
	for _, svc := range h.storedServed {
		services = append(services, svc)
		stored[svc.Name] = true
	}
	rows := make([]dashboardService, 0, len(services))
	for _, svc := range services {
		row := dashboardService{
			Name:           svc.Name,
			BasePath:       svc.BasePath,
			Protocol:       svc.Protocol,
			Targets:        svc.Targets,
			Authentication: svc.Authentication,
			StripBasePath:  svc.StripBasePath,
			HealthChecked:  svc.HealthCheck != nil && svc.HealthCheck.Enabled,
			Stored:         stored[svc.Name],
		}
		if row.Protocol == "" {
			row.Protocol = "http"
		}
		if svc.Timeout > 0 {
			row.Timeout = svc.Timeout.String()
		}
		if limit, window := config.EffectiveRateLimit(cfg.RateLimit, svc.RateLimit); limit > 0 {
			row.RateLimit = fmt.Sprintf("%d / %s", limit, shortWindow(window))
		}
		if on, ttl := config.EffectiveCache(cfg.Cache, svc.Cache); on {
			row.Cache = ttl.String()
		}
		rows = append(rows, row)
	}
	return h.renderTemplate(c, "dashboard.html", map[string]interface{}{
		"Services":   rows,
		"ConfigPath": h.configPath,
	})
}

// shortWindow writes a rate-limit window as "min", "10 s" or "2 h".
func shortWindow(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "min"
	case d == time.Second:
		return "s"
	case d == time.Hour:
		return "h"
	case d%time.Hour == 0:
		return fmt.Sprintf("%d h", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%d min", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%d s", d/time.Second)
	}
	return d.String()
}

func (h *AdminHandler) handleMonitoring(c echo.Context) error {
	return h.renderTemplate(c, "monitoring.html", nil)
}

func (h *AdminHandler) handleTraces(c echo.Context) error {
	return h.renderTemplate(c, "traces.html", nil)
}

func (h *AdminHandler) handleMiddlewareChain(c echo.Context) error {
	return h.renderTemplate(c, "middleware_chain.html", nil)
}

func (h *AdminHandler) handleSettings(c echo.Context) error {
	return h.renderTemplate(c, "settings.html", nil)
}
