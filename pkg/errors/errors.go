// Package errors turns handler errors into HTTP responses for the whole gateway.
package errors

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// Sentinel errors that handlers can wrap; StatusCodeFromError maps them to a status.
var (
	ErrNotFound           = errors.New("resource not found")
	ErrUnauthorized       = errors.New("unauthorized access")
	ErrForbidden          = errors.New("forbidden access")
	ErrBadRequest         = errors.New("bad request")
	ErrInternalServer     = errors.New("internal server error")
	ErrServiceUnavailable = errors.New("service unavailable")
	ErrTimeout            = errors.New("request timed out")
)

// HTTPError is an error with a status, a message for the client and optional details.
type HTTPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Error returns the message and, if set, the details.
func (e *HTTPError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("%s: %s", e.Message, e.Details)
	}
	return e.Message
}

// NewHTTPError creates an HTTPError.
func NewHTTPError(code int, message string, details string) *HTTPError {
	return &HTTPError{Code: code, Message: message, Details: details}
}

// StatusCodeFromError returns the HTTP status for an error: the code of an HTTPError or
// *echo.HTTPError, the status for a wrapped sentinel, 504 for a deadline, else 500.
func StatusCodeFromError(err error) int {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Code
	}
	var echoErr *echo.HTTPError
	if errors.As(err, &echoErr) {
		return echoErr.Code
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.Is(err, ErrServiceUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// ErrorHandler is the gateway's echo.HTTPErrorHandler. The body keeps the shape of Echo's
// default handler, {"message": "..."}, so clients see no change; HTTPError adds "details".
// A raw error's text is never sent (it may name internal hosts). Beyond the default, wrapped
// sentinels and deadlines get their own status, and server errors are logged with the request
// (client errors only at debug level, so a scan of unknown paths does not flood the log).
func ErrorHandler(logger *logrus.Logger) echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			// Headers are out (a streamed or proxied response failed midway): nothing to send.
			logger.WithError(err).WithField("path", c.Request().URL.Path).Debug("Error after the response was sent")
			return
		}

		code, body := responseFor(err)

		fields := logrus.Fields{
			"method": c.Request().Method,
			"path":   c.Request().URL.Path,
			"status": code,
		}
		if code >= http.StatusInternalServerError {
			logger.WithFields(fields).WithError(err).Error("Request failed")
		} else {
			logger.WithFields(fields).WithError(err).Debug("Request rejected")
		}

		var werr error
		if c.Request().Method == http.MethodHead {
			werr = c.NoContent(code)
		} else {
			werr = c.JSON(code, body)
		}
		if werr != nil {
			logger.WithError(werr).Warn("Failed to write error response")
		}
	}
}

func responseFor(err error) (int, interface{}) {
	var he *HTTPError
	if errors.As(err, &he) {
		body := map[string]string{"message": he.Message}
		if he.Details != "" {
			body["details"] = he.Details
		}
		return he.Code, body
	}

	var ee *echo.HTTPError
	if errors.As(err, &ee) {
		// echo.NewHTTPError(code).SetInternal(another *echo.HTTPError) reports the inner one,
		// as Echo's default handler does.
		var inner *echo.HTTPError
		if ee.Internal != nil && errors.As(ee.Internal, &inner) {
			ee = inner
		}
		if msg, ok := ee.Message.(string); ok {
			return ee.Code, map[string]string{"message": msg}
		}
		if ee.Message == nil {
			return ee.Code, map[string]string{"message": http.StatusText(ee.Code)}
		}
		return ee.Code, ee.Message
	}

	code := StatusCodeFromError(err)
	return code, map[string]string{"message": http.StatusText(code)}
}
