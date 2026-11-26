package middleware

import (
	"net/http"
	"odin/pkg/auth"
	"odin/pkg/ratelimit"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

func RateLimiterMiddleware(limiter ratelimit.RateLimiter, logger *logrus.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key := c.RealIP()

			// The JWT middleware stores *auth.JWTClaims (this used to look for a map, so the
			// per-user key never applied).
			if claims, ok := c.Get("user").(*auth.JWTClaims); ok && claims.Subject() != "" {
				key = "user:" + claims.Subject()
			}

			if !limiter.Allow(key) {
				logger.WithFields(logrus.Fields{
					"ip":  c.RealIP(),
					"uri": c.Request().RequestURI,
				}).Warn("Rate limit exceeded")
				return echo.NewHTTPError(http.StatusTooManyRequests, "Rate limit exceeded")
			}

			return next(c)
		}
	}
}
