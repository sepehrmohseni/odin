package middleware

import (
	"strings"
	"time"

	"odin/pkg/admin"
	gwerrors "odin/pkg/errors"

	"github.com/labstack/echo/v4"
)

// MonitoringMiddleware creates middleware that records metrics for the monitoring dashboard
func MonitoringMiddleware() echo.MiddlewareFunc {
	collector := admin.GetCollector()

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// The admin's own pages, API and assets are operator traffic, not gateway traffic.
			requestPath := c.Request().URL.Path
			if requestPath == "/admin" || strings.HasPrefix(requestPath, "/admin/") || strings.HasPrefix(requestPath, "/static/") {
				return next(c)
			}

			start := time.Now()

			// Execute the next handler
			err := next(c)

			// Record the metrics
			duration := time.Since(start)
			method := c.Request().Method
			path := requestPath
			// An error is turned into a response by the error handler only after this returns,
			// so the response still says 200; take the status from the error instead. (Every
			// rejected request used to be counted as a 200.)
			status := c.Response().Status
			if err != nil && !c.Response().Committed {
				status = gwerrors.StatusCodeFromError(err)
			}

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
