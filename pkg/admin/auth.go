package admin

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// basicAuthMiddleware protects the admin routes. A request is let through with a valid session
// cookie (set by the login form) or with HTTP Basic credentials (for scripts). State-changing
// requests must also come from the admin's own origin.
//
// The admin routes are only mounted when admin.enabled is true (see gateway.New), so there is no
// "disabled" bypass here: an earlier version skipped authentication entirely when admin was
// disabled while still serving the routes.
func (h *AdminHandler) basicAuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if !h.sameOrigin(c.Request()) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
		}

		if cookie, err := c.Cookie(sessionCookieName); err == nil && h.sessions.valid(cookie.Value) {
			return next(c)
		}

		if user, pass, ok := parseBasicAuth(c.Request().Header.Get("Authorization")); ok {
			ip := clientIP(c)
			if blocked, until := h.logins.blocked(ip); blocked {
				return tooManyAttempts(c, until)
			}
			if h.checkCredentials(user, pass) {
				h.logins.succeed(ip)
				return next(c)
			}
			h.logins.fail(ip)
		}

		return h.unauthorized(c)
	}
}

func parseBasicAuth(header string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	payload, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(payload), ":")
	return user, pass, ok
}

func tooManyAttempts(c echo.Context, until time.Time) error {
	wait := time.Until(until).Round(time.Second)
	c.Response().Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())))
	return c.HTML(http.StatusTooManyRequests,
		`<div class="alert alert-danger">Too many failed logins. Try again in `+wait.String()+`.</div>`)
}

func (h *AdminHandler) unauthorized(c echo.Context) error {
	// API clients get a plain 401; browsers are sent to the login page.
	if strings.HasPrefix(c.Request().URL.Path, "/admin/api/") ||
		strings.Contains(c.Request().Header.Get("Accept"), "application/json") {
		c.Response().Header().Set("WWW-Authenticate", `Basic realm="Odin Admin"`)
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	}
	return c.Redirect(http.StatusSeeOther, "/admin/login")
}

func (h *AdminHandler) handleLogin(c echo.Context) error {
	return h.renderTemplate(c, "login.html", nil)
}

func (h *AdminHandler) handleLoginPost(c echo.Context) error {
	if !h.sameOrigin(c.Request()) {
		return c.HTML(http.StatusForbidden, `<div class="alert alert-danger">Cross-origin login rejected</div>`)
	}

	ip := clientIP(c)
	if blocked, until := h.logins.blocked(ip); blocked {
		return tooManyAttempts(c, until)
	}

	if !h.checkCredentials(c.FormValue("username"), c.FormValue("password")) {
		h.logins.fail(ip)
		h.logger.WithField("ip", ip).Warn("Failed admin login")
		return c.HTML(http.StatusUnauthorized, `<div class="alert alert-danger">Invalid username or password</div>`)
	}
	h.logins.succeed(ip)

	token, err := h.sessions.create()
	if err != nil {
		return c.HTML(http.StatusInternalServerError, `<div class="alert alert-danger">Could not create a session</div>`)
	}
	c.SetCookie(&http.Cookie{ //nolint:gosec // Secure follows the request scheme so plain-HTTP setups still work
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   isSecureRequest(c),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	c.Response().Header().Set("HX-Redirect", "/admin/dashboard")
	return c.String(http.StatusOK, "Login successful. Redirecting...")
}

func (h *AdminHandler) handleLogout(c echo.Context) error {
	if cookie, err := c.Cookie(sessionCookieName); err == nil {
		h.sessions.revoke(cookie.Value)
	}
	c.SetCookie(&http.Cookie{ //nolint:gosec // Secure follows the request scheme so plain-HTTP setups still work
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   isSecureRequest(c),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	return c.Redirect(http.StatusSeeOther, "/admin/login")
}
