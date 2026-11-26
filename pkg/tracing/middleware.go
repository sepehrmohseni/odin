package tracing

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Middleware starts a server span for every request, continuing the caller's trace when the
// request carries W3C trace context. It replaces the otelecho contrib middleware, which is
// deprecated and whose successor only supports Echo v5.
//
// The span is named "<METHOD> <route>", using the matched route template rather than the raw
// path so span names stay low-cardinality.
func (m *Manager) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !m.config.Enabled {
				return next(c)
			}

			req := c.Request()
			propagator := otel.GetTextMapPropagator()
			ctx := propagator.Extract(req.Context(), propagation.HeaderCarrier(req.Header))

			route := c.Path()
			if route == "" {
				route = "unmatched"
			}
			ctx, span := m.tracer.Start(ctx, req.Method+" "+route,
				oteltrace.WithSpanKind(oteltrace.SpanKindServer),
				oteltrace.WithAttributes(
					attribute.String("http.request.method", req.Method),
					attribute.String("http.route", route),
					attribute.String("url.path", req.URL.Path),
					attribute.String("server.address", req.Host),
					attribute.String("client.address", c.RealIP()),
					attribute.String("user_agent.original", req.UserAgent()),
				),
			)
			defer span.End()

			c.SetRequest(req.WithContext(ctx))

			err := next(c)

			status := c.Response().Status
			if err != nil {
				var he *echo.HTTPError
				if errors.As(err, &he) {
					status = he.Code
				} else {
					status = http.StatusInternalServerError
				}
				span.RecordError(err)
			}
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			if status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(status))
			}

			return err
		}
	}
}

// InjectHTTP writes the trace context of ctx into outgoing request headers, so upstream services
// join the gateway's trace.
func InjectHTTP(ctx context.Context, header http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}
