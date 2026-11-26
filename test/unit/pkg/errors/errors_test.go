package errors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"odin/pkg/errors"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func handle(t *testing.T, method string, err error) *httptest.ResponseRecorder {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(method, "/test", nil), rec)
	errors.ErrorHandler(logger)(err, c)
	return rec
}

func TestNewHTTPError(t *testing.T) {
	err := errors.NewHTTPError(http.StatusBadRequest, "Bad request", "Invalid input")

	assert.Equal(t, http.StatusBadRequest, err.Code)
	assert.Equal(t, "Bad request", err.Message)
	assert.Equal(t, "Invalid input", err.Details)
	assert.Equal(t, "Bad request: Invalid input", err.Error())
}

func TestErrorHandlerBodies(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"HTTPError with details", errors.NewHTTPError(http.StatusBadRequest, "custom error", "details"),
			http.StatusBadRequest, `{"details":"details","message":"custom error"}`},
		{"echo error keeps Echo's shape", echo.NewHTTPError(http.StatusNotFound, "not found"),
			http.StatusNotFound, `{"message":"not found"}`},
		{"echo error without message", echo.NewHTTPError(http.StatusTooManyRequests),
			http.StatusTooManyRequests, `{"message":"Too Many Requests"}`},
		{"echo error with a map message", echo.NewHTTPError(http.StatusConflict, map[string]string{"error": "taken"}),
			http.StatusConflict, `{"error":"taken"}`},
		{"inner echo error wins", echo.NewHTTPError(http.StatusBadRequest).SetInternal(echo.NewHTTPError(http.StatusForbidden, "no")),
			http.StatusForbidden, `{"message":"no"}`},
		{"raw error text is not sent", fmt.Errorf("dial tcp 10.0.0.5:8080: connection refused"),
			http.StatusInternalServerError, `{"message":"Internal Server Error"}`},
		{"wrapped sentinel", fmt.Errorf("user 42: %w", errors.ErrNotFound),
			http.StatusNotFound, `{"message":"Not Found"}`},
		{"deadline", fmt.Errorf("upstream: %w", context.DeadlineExceeded),
			http.StatusGatewayTimeout, `{"message":"Gateway Timeout"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := handle(t, http.MethodGet, tt.err)
			assert.Equal(t, tt.code, rec.Code)
			assert.JSONEq(t, tt.body, rec.Body.String())
		})
	}
}

func TestErrorHandlerHeadHasNoBody(t *testing.T) {
	rec := handle(t, http.MethodHead, echo.NewHTTPError(http.StatusNotFound, "not found"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestErrorHandlerLeavesACommittedResponseAlone(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/test", nil), rec)
	require.NoError(t, c.String(http.StatusOK, "partial"))

	errors.ErrorHandler(logger)(fmt.Errorf("stream broke"), c)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "partial", rec.Body.String())
}

func TestStatusCodeFromError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{"HTTPError", errors.NewHTTPError(http.StatusBadRequest, "bad request", ""), http.StatusBadRequest},
		{"echo.HTTPError", echo.NewHTTPError(http.StatusTeapot), http.StatusTeapot},
		{"ErrNotFound", errors.ErrNotFound, http.StatusNotFound},
		{"ErrUnauthorized", errors.ErrUnauthorized, http.StatusUnauthorized},
		{"ErrForbidden", errors.ErrForbidden, http.StatusForbidden},
		{"ErrBadRequest", errors.ErrBadRequest, http.StatusBadRequest},
		{"ErrTimeout", errors.ErrTimeout, http.StatusGatewayTimeout},
		{"DeadlineExceeded", context.DeadlineExceeded, http.StatusGatewayTimeout},
		{"ErrServiceUnavailable", errors.ErrServiceUnavailable, http.StatusServiceUnavailable},
		{"wrapped", fmt.Errorf("x: %w", errors.ErrForbidden), http.StatusForbidden},
		{"Generic error", assert.AnError, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, errors.StatusCodeFromError(tt.err))
		})
	}
}
