// A middleware plugin used by plugin_so_test.go: it rejects requests without the configured
// X-API-Key. Built with -buildmode=plugin by the test.
package main

import (
	"net/http"

	"odin/pkg/plugins"

	"github.com/labstack/echo/v4"
)

type apiKey struct{ key string }

func (a *apiKey) Name() string                            { return "api-key" }
func (a *apiKey) Version() string                         { return "1.0.0" }
func (a *apiKey) Initialize(map[string]interface{}) error { return nil }
func (a *apiKey) Cleanup() error                          { return nil }
func (a *apiKey) Handle(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Header.Get("X-API-Key") != a.key {
			return echo.NewHTTPError(http.StatusUnauthorized, "missing or wrong API key")
		}
		return next(c)
	}
}

// New is the entry point the gateway looks for.
func New(config map[string]interface{}) (plugins.Middleware, error) {
	key, _ := config["key"].(string)
	return &apiKey{key: key}, nil
}
