package gateway_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"odin/pkg/config"
	gwerrors "odin/pkg/errors"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gateway's error handler answers every route: bodies keep Echo's {"message": ...} shape,
// wrapped sentinels and deadlines get their status, and raw error text is never sent.
func TestGatewayErrorResponses(t *testing.T) {
	be := newBackend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	gw, h := pipelineGateway(t, &config.Config{
		Services: []config.ServiceConfig{{Name: "api", BasePath: "/api", Targets: []string{be.URL}}},
	})

	fail := func(err error) func(echo.HandlerFunc) echo.HandlerFunc {
		return func(echo.HandlerFunc) echo.HandlerFunc {
			return func(echo.Context) error { return err }
		}
	}
	require.NoError(t, gw.Plugins().RegisterMiddleware("missing", mw("missing", fail(fmt.Errorf("order 7: %w", gwerrors.ErrNotFound))), 0, []string{"/api/missing"}, "pre-auth"))
	require.NoError(t, gw.Plugins().RegisterMiddleware("raw", mw("raw", fail(fmt.Errorf("dial tcp 10.1.2.3:5432: connection refused"))), 0, []string{"/api/raw"}, "pre-auth"))
	require.NoError(t, gw.Plugins().RegisterMiddleware("slow", mw("slow", fail(fmt.Errorf("lookup: %w", context.DeadlineExceeded))), 0, []string{"/api/slow"}, "pre-auth"))

	tests := []struct {
		path string
		code int
		body string
	}{
		{"/nowhere", http.StatusNotFound, `{"message":"Not Found"}`},
		{"/api/missing", http.StatusNotFound, `{"message":"Not Found"}`},
		{"/api/raw", http.StatusInternalServerError, `{"message":"Internal Server Error"}`},
		{"/api/slow", http.StatusGatewayTimeout, `{"message":"Gateway Timeout"}`},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := serve(h, "GET", tt.path, nil, nil)
			assert.Equal(t, tt.code, rec.Code)
			assert.JSONEq(t, tt.body, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "10.1.2.3")
		})
	}
}
