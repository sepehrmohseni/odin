package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName = "odin_admin_session"
	sessionTTL        = 12 * time.Hour

	// Failed logins allowed per client IP within loginWindow before it is locked out for
	// loginLockout.
	maxLoginFailures = 5
	loginWindow      = 15 * time.Minute
	loginLockout     = 15 * time.Minute
)

// sessionStore keeps admin sessions in memory. A session is an opaque random token; the
// password never leaves the login request. Sessions do not survive a restart.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

func (s *sessionStore) create() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for t, exp := range s.sessions {
		if now.After(exp) {
			delete(s.sessions, t)
		}
	}
	s.sessions[token] = now.Add(sessionTTL)
	return token, nil
}

func (s *sessionStore) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *sessionStore) revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// loginLimiter counts failed logins per client IP.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	locked   map[string]time.Time // ip -> locked until
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		failures: make(map[string][]time.Time),
		locked:   make(map[string]time.Time),
	}
}

// blocked reports whether ip is locked out, and until when.
func (l *loginLimiter) blocked(ip string) (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.locked[ip]
	if !ok {
		return false, time.Time{}
	}
	if time.Now().After(until) {
		delete(l.locked, ip)
		return false, time.Time{}
	}
	return true, until
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	recent := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if now.Sub(t) < loginWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	if len(recent) >= maxLoginFailures {
		l.locked[ip] = now.Add(loginLockout)
		delete(l.failures, ip)
		return
	}
	l.failures[ip] = recent
}

func (l *loginLimiter) succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

// checkCredentials compares in constant time. With a bcrypt hash configured, the password is
// checked against the hash instead of the plain-text password.
func (h *AdminHandler) checkCredentials(username, password string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(h.username)) == 1
	var passOK bool
	if h.passwordHash != "" {
		passOK = bcrypt.CompareHashAndPassword([]byte(h.passwordHash), []byte(password)) == nil
	} else {
		passOK = subtle.ConstantTimeCompare([]byte(password), []byte(h.password)) == 1
	}
	return userOK && passOK
}

// sameOrigin rejects cross-site state-changing requests (CSRF). Browsers always send Origin on
// cross-origin POST/PUT/PATCH/DELETE; when it is absent the Referer is checked instead, and a
// request with neither (curl, scripts) is allowed, since it cannot come from another site's page.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	source := r.Header.Get("Origin")
	if source == "" || source == "null" {
		source = r.Header.Get("Referer")
	}
	if source == "" {
		return r.Header.Get("Origin") != "null"
	}
	u, err := url.Parse(source)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// isSecureRequest reports whether the client reached us over HTTPS, directly or through a
// TLS-terminating proxy.
func isSecureRequest(c echo.Context) bool {
	return c.Scheme() == "https"
}

func clientIP(c echo.Context) string {
	ip := c.RealIP()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		return host
	}
	return ip
}
