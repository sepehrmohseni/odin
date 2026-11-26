package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"odin/pkg/cache"

	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// ResponseCacheOptions configures NewResponseCache.
type ResponseCacheOptions struct {
	// TTL of an entry; 0 uses the store's default. A shorter Cache-Control max-age or
	// s-maxage from the upstream wins.
	TTL time.Duration
	// MaxEntryBytes is the largest response body that is stored (default 1 MiB).
	MaxEntryBytes int
	// KeyPrefix namespaces the keys, e.g. by service name.
	KeyPrefix string
}

// CacheMiddleware is NewResponseCache with default options.
func CacheMiddleware(store cache.Store, logger *logrus.Logger) echo.MiddlewareFunc {
	return NewResponseCache(store, ResponseCacheOptions{}, logger)
}

// NewResponseCache caches upstream responses for GET and HEAD requests.
//
// It must be mounted on a service's routes after its authentication middleware. (The old
// version ran globally before any route middleware, so a cached response to an authenticated
// request was served to anyone, without a token, and it also cached admin pages and errors.)
// Only responses that are safe to share are stored:
//   - the request carries no Authorization or Cookie header and no Cache-Control: no-store /
//     no-cache;
//   - the response is 200, sets no cookie, and is not marked no-store, no-cache or private and
//     has no Vary: *;
//   - the body fits in MaxEntryBytes.
//
// Responses carry X-Cache: HIT or MISS (counted by the Prometheus metrics).
func NewResponseCache(store cache.Store, opts ResponseCacheOptions, logger *logrus.Logger) echo.MiddlewareFunc {
	if opts.MaxEntryBytes <= 0 {
		opts.MaxEntryBytes = 1 << 20
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if !requestIsCacheable(req) {
				return next(c)
			}

			key := responseCacheKey(opts.KeyPrefix, req)
			if cached, ok := store.Get(key); ok {
				if entry, ok := cached.(*cache.CachedResponse); ok {
					h := c.Response().Header()
					for k, vals := range entry.Headers {
						h[k] = append([]string(nil), vals...)
					}
					h.Set("X-Cache", "HIT")
					c.Response().WriteHeader(entry.StatusCode)
					if req.Method == http.MethodHead {
						return nil
					}
					_, err := c.Response().Write(entry.Body)
					return err
				}
			}

			c.Response().Header().Set("X-Cache", "MISS")
			rec := &cacheRecorder{ResponseWriter: c.Response().Writer, limit: opts.MaxEntryBytes}
			c.Response().Writer = rec
			err := next(c)
			c.Response().Writer = rec.ResponseWriter

			if err != nil || rec.overflow || rec.status != http.StatusOK || req.Method != http.MethodGet {
				return err
			}
			header := c.Response().Header()
			ttl, ok := responseTTL(header, opts.TTL)
			if !ok {
				return nil
			}

			stored := make(http.Header, len(header))
			for k, vals := range header {
				switch http.CanonicalHeaderKey(k) {
				case "Set-Cookie", "X-Cache", "Connection", "Keep-Alive", "Transfer-Encoding":
					continue
				}
				stored[k] = append([]string(nil), vals...)
			}
			store.Set(key, &cache.CachedResponse{
				Headers:    stored,
				StatusCode: rec.status,
				Body:       rec.body.Bytes(),
			}, ttl)
			if logger != nil {
				logger.WithField("uri", req.RequestURI).Debug("Cached response")
			}
			return nil
		}
	}
}

func requestIsCacheable(req *http.Request) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	if req.Header.Get("Upgrade") != "" { // WebSocket handshakes need the raw connection
		return false
	}
	if req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
		return false
	}
	cc := strings.ToLower(req.Header.Get("Cache-Control"))
	return !strings.Contains(cc, "no-store") && !strings.Contains(cc, "no-cache")
}

// responseTTL decides whether a response may be stored, and for how long.
func responseTTL(h http.Header, configured time.Duration) (time.Duration, bool) {
	if h.Get("Set-Cookie") != "" || strings.TrimSpace(h.Get("Vary")) == "*" {
		return 0, false
	}
	ttl := configured
	for _, directive := range strings.Split(strings.ToLower(h.Get("Cache-Control")), ",") {
		directive = strings.TrimSpace(directive)
		switch {
		case directive == "no-store" || directive == "no-cache" || directive == "private":
			return 0, false
		case strings.HasPrefix(directive, "s-maxage=") || strings.HasPrefix(directive, "max-age="):
			_, value, _ := strings.Cut(directive, "=")
			secs, err := strconv.Atoi(value)
			if err != nil || secs <= 0 {
				return 0, false
			}
			if upstream := time.Duration(secs) * time.Second; ttl <= 0 || upstream < ttl {
				ttl = upstream
			}
		}
	}
	return ttl, true
}

// responseCacheKey identifies a response by method, host, path and query. Keys share the
// "cache:" prefix so the Redis store's Clear can find them.
func responseCacheKey(prefix string, req *http.Request) string {
	sum := sha256.Sum256([]byte(req.Method + "\x00" + req.Host + "\x00" + req.URL.Path + "\x00" + req.URL.RawQuery))
	return "cache:" + prefix + ":" + hex.EncodeToString(sum[:])
}

// cacheRecorder passes the response through while keeping a copy of the status and body.
type cacheRecorder struct {
	http.ResponseWriter
	status   int
	body     bytes.Buffer
	limit    int
	overflow bool
}

func (r *cacheRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *cacheRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if !r.overflow {
		if r.body.Len()+len(b) > r.limit {
			r.overflow = true
			r.body.Reset()
		} else {
			r.body.Write(b)
		}
	}
	return r.ResponseWriter.Write(b)
}

// Flush keeps streaming responses working through the recorder.
func (r *cacheRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *cacheRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
