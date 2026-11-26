package middleware

import (
	"time"

	"odin/pkg/admin"

	"github.com/labstack/echo/v4"
)

// MonitoringMiddleware creates middleware that records metrics for the monitoring dashboard
func MonitoringMiddleware() echo.MiddlewareFunc {
	collector := admin.GetCollector()

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Skip monitoring for admin routes to avoid recursive calls
			requestPath := c.Request().URL.Path
			if requestPath == "/admin/ws/monitoring" || requestPath == "/admin/api/monitoring/metrics" {
				return next(c)
			}

			start := time.Now()

			// Execute the next handler
			err := next(c)

			// Record the metrics
			duration := time.Since(start)
			method := c.Request().Method
			path := requestPath
			status := c.Response().Status

			// The service the request was routed to (set by the router), else the gateway itself.
			service, _ := c.Get("service_name").(string)
			if service == "" {
				service = "gateway"
			}

			collector.RecordRequest(method, path, duration, status, service)

			return err
		}
	}
}
