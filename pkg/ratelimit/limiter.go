package ratelimit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

type Algorithm string

const (
	AlgorithmTokenBucket   Algorithm = "token_bucket"
	AlgorithmSlidingWindow Algorithm = "sliding_window"
	AlgorithmFixedWindow   Algorithm = "fixed_window"
	AlgorithmLeakyBucket   Algorithm = "leaky_bucket"
)

type Config struct {
	Enabled          bool          `yaml:"enabled"`
	Algorithm        Algorithm     `yaml:"algorithm"`
	DefaultLimit     int           `yaml:"defaultLimit"`
	DefaultWindow    time.Duration `yaml:"defaultWindow"`
	BurstSize        int           `yaml:"burstSize"`
	Redis            RedisConfig   `yaml:"redis"`
	Rules            []Rule        `yaml:"rules"`
	SkipPaths        []string      `yaml:"skipPaths"`
	TrustedProxies   []string      `yaml:"trustedProxies"`
	HeadersToInclude []string      `yaml:"headersToInclude"`
	CustomKeyFunc    string        `yaml:"customKeyFunc"`
	ResponseHeaders  bool          `yaml:"responseHeaders"`
	LogViolations    bool          `yaml:"logViolations"`
	// FailClosed rejects requests when Redis cannot be reached. By default they are allowed
	// (and the error logged), so a Redis outage does not take the gateway down.
	FailClosed bool `yaml:"failClosed"`
}

type RedisConfig struct {
	// URL (redis://[:password@]host:port/db) takes precedence over Address/Password/DB.
	URL      string `yaml:"url"`
	Address  string `yaml:"address"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type Rule struct {
	Name        string        `yaml:"name"`
	Path        string        `yaml:"path"`
	Method      string        `yaml:"method"`
	Limit       int           `yaml:"limit"`
	Window      time.Duration `yaml:"window"`
	BurstSize   int           `yaml:"burstSize"`
	UserTypes   []string      `yaml:"userTypes"`
	APIKeys     []string      `yaml:"apiKeys"`
	IPWhitelist []string      `yaml:"ipWhitelist"`
	IPBlacklist []string      `yaml:"ipBlacklist"`
	Headers     []HeaderRule  `yaml:"headers"`
	SkipAuth    bool          `yaml:"skipAuth"`
}

type HeaderRule struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type KeyType string

const (
	KeyTypeIP     KeyType = "ip"
	KeyTypeUser   KeyType = "user"
	KeyTypeAPIKey KeyType = "api_key"
	KeyTypeCustom KeyType = "custom"
)

type Limiter struct {
	config      Config
	redisClient *redis.Client
	local       *localStore
	logger      *logrus.Logger
	rules       map[string]Rule
	now         func() time.Time
}

type LimitInfo struct {
	Key       string        `json:"key"`
	Limit     int           `json:"limit"`
	Remaining int           `json:"remaining"`
	ResetTime time.Time     `json:"reset_time"`
	Window    time.Duration `json:"window"`
}

type RateLimiter interface {
	Allow(key string) bool
	CheckLimit(ctx context.Context, key string, rule *Rule) (*LimitInfo, bool)
	Middleware() echo.MiddlewareFunc
}

func NewLimiter(config Config, logger *logrus.Logger) (*Limiter, error) {
	var redisClient *redis.Client

	if config.Redis.URL != "" {
		opts, err := redis.ParseURL(config.Redis.URL)
		if err != nil {
			return nil, fmt.Errorf("invalid Redis URL: %w", err)
		}
		redisClient = redis.NewClient(opts)
	} else if config.Redis.Address != "" {
		redisClient = redis.NewClient(&redis.Options{
			Addr:     config.Redis.Address,
			Password: config.Redis.Password,
			DB:       config.Redis.DB,
		})

	}
	if redisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := redisClient.Ping(ctx).Err(); err != nil {
			_ = redisClient.Close()
			return nil, fmt.Errorf("failed to connect to Redis: %w", err)
		}
	}

	rules := make(map[string]Rule)
	for _, rule := range config.Rules {
		key := fmt.Sprintf("%s:%s", rule.Method, rule.Path)
		rules[key] = rule
	}

	return &Limiter{
		config:      config,
		redisClient: redisClient,
		local:       newLocalStore(),
		logger:      logger,
		rules:       rules,
		now:         time.Now,
	}, nil
}

func (l *Limiter) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !l.config.Enabled {
				return next(c)
			}

			if l.shouldSkip(c) {
				return next(c)
			}

			rule := l.findRule(c)
			if rule == nil {
				rule = &Rule{
					Limit:  l.config.DefaultLimit,
					Window: l.config.DefaultWindow,
				}
			}

			if l.isWhitelisted(c, rule) {
				return next(c)
			}

			if l.isBlacklisted(c, rule) {
				return echo.NewHTTPError(http.StatusForbidden, "IP address is blacklisted")
			}

			key := l.generateKey(c, rule)
			limitInfo, allowed := l.checkLimit(c.Request().Context(), key, rule)

			if l.config.ResponseHeaders {
				l.setHeaders(c, limitInfo)
			}

			if !allowed {
				if l.config.LogViolations {
					l.logger.WithFields(logrus.Fields{
						"key":       key,
						"limit":     limitInfo.Limit,
						"remaining": limitInfo.Remaining,
						"ip":        l.getClientIP(c),
						"path":      c.Request().URL.Path,
						"method":    c.Request().Method,
					}).Warn("Rate limit exceeded")
				}

				return echo.NewHTTPError(http.StatusTooManyRequests, "Rate limit exceeded")
			}

			return next(c)
		}
	}
}

func (l *Limiter) shouldSkip(c echo.Context) bool {
	path := c.Request().URL.Path

	for _, skipPath := range l.config.SkipPaths {
		if strings.HasPrefix(path, skipPath) {
			return true
		}
	}

	return false
}

func (l *Limiter) findRule(c echo.Context) *Rule {
	method := c.Request().Method
	path := c.Request().URL.Path

	key := fmt.Sprintf("%s:%s", method, path)
	if rule, exists := l.rules[key]; exists {
		return &rule
	}

	for _, rule := range l.config.Rules {
		if l.MatchesRule(c, &rule) {
			return &rule
		}
	}

	return nil
}

func (l *Limiter) MatchesRule(c echo.Context, rule *Rule) bool {
	if rule.Method != "" && rule.Method != c.Request().Method {
		return false
	}

	if rule.Path != "" && !strings.HasPrefix(c.Request().URL.Path, rule.Path) {
		return false
	}

	for _, headerRule := range rule.Headers {
		headerValue := c.Request().Header.Get(headerRule.Name)
		if headerValue != headerRule.Value {
			return false
		}
	}

	return true
}

func (l *Limiter) isWhitelisted(c echo.Context, rule *Rule) bool {
	clientIP := l.getClientIP(c)

	for _, whiteIP := range rule.IPWhitelist {
		if l.MatchesIP(clientIP, whiteIP) {
			return true
		}
	}

	return false
}

func (l *Limiter) isBlacklisted(c echo.Context, rule *Rule) bool {
	clientIP := l.getClientIP(c)

	for _, blackIP := range rule.IPBlacklist {
		if l.MatchesIP(clientIP, blackIP) {
			return true
		}
	}

	return false
}

func (l *Limiter) MatchesIP(clientIP, ruleIP string) bool {
	if strings.Contains(ruleIP, "/") {
		_, network, err := net.ParseCIDR(ruleIP)
		if err != nil {
			return false
		}
		ip := net.ParseIP(clientIP)
		return network.Contains(ip)
	}

	return clientIP == ruleIP
}

func (l *Limiter) generateKey(c echo.Context, rule *Rule) string {
	var keyParts []string

	if apiKey := c.Request().Header.Get("X-API-Key"); apiKey != "" {
		keyParts = append(keyParts, "api_key:"+apiKey)
	} else if userID := l.getUserID(c); userID != "" {
		keyParts = append(keyParts, "user:"+userID)
	} else {
		keyParts = append(keyParts, "ip:"+l.getClientIP(c))
	}

	keyParts = append(keyParts, rule.Path)
	keyParts = append(keyParts, rule.Method)

	for _, header := range l.config.HeadersToInclude {
		value := c.Request().Header.Get(header)
		if value != "" {
			keyParts = append(keyParts, fmt.Sprintf("%s:%s", header, value))
		}
	}

	key := strings.Join(keyParts, ":")
	hash := sha256.Sum256([]byte(key))
	return fmt.Sprintf("ratelimit:%x", hash)
}

// getClientIP returns the client IP. Proxy trust is decided by the gateway's IP extractor
// (server.trustedProxies); with trustedProxies set here too, the first X-Forwarded-For entry of
// a request from such a proxy is used, as before.
func (l *Limiter) getClientIP(c echo.Context) string {
	if l.isTrustedProxy(c.RealIP()) {
		if xff := c.Request().Header.Get("X-Forwarded-For"); xff != "" {
			ips := strings.Split(xff, ",")
			return strings.TrimSpace(ips[0])
		}
		if xri := c.Request().Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}

	return c.RealIP()
}

func (l *Limiter) isTrustedProxy(ip string) bool {
	for _, trustedIP := range l.config.TrustedProxies {
		if l.MatchesIP(ip, trustedIP) {
			return true
		}
	}
	return false
}

func (l *Limiter) getUserID(c echo.Context) string {
	if user := c.Get("user"); user != nil {
		if userMap, ok := user.(map[string]interface{}); ok {
			if userID, exists := userMap["user_id"]; exists {
				return fmt.Sprintf("%v", userID)
			}
		}
	}

	return ""
}

func (l *Limiter) CheckLimit(ctx context.Context, key string, rule *Rule) (*LimitInfo, bool) {
	limit := rule.Limit
	window := rule.Window

	if limit <= 0 {
		limit = l.config.DefaultLimit
	}
	if window <= 0 {
		window = l.config.DefaultWindow
	}
	if window <= 0 {
		window = time.Minute
	}
	if limit <= 0 {
		// No limit configured: allow.
		return &LimitInfo{Key: key, Limit: 0, Remaining: 0, ResetTime: l.now().Add(window), Window: window}, true
	}

	switch l.config.Algorithm {
	case AlgorithmSlidingWindow:
		return l.checkSlidingWindow(ctx, key, limit, window)
	case AlgorithmFixedWindow:
		return l.checkFixedWindow(ctx, key, limit, window)
	case AlgorithmTokenBucket:
		return l.checkTokenBucket(ctx, key, limit, window, rule.BurstSize)
	default:
		return l.checkFixedWindow(ctx, key, limit, window)
	}
}

func (l *Limiter) checkFixedWindow(ctx context.Context, key string, limit int, window time.Duration) (*LimitInfo, bool) {
	now := l.now()
	if l.redisClient == nil {
		count, reset := l.local.fixedWindow(key, window, now)
		return &LimitInfo{
			Key:       key,
			Limit:     limit,
			Remaining: max(limit-count, 0),
			ResetTime: reset,
			Window:    window,
		}, count <= limit
	}

	// Window index in nanoseconds: the old Unix()/window.Seconds() divided by zero for windows
	// under a second.
	start := now.Truncate(window)
	windowKey := fmt.Sprintf("%s:%d", key, start.UnixNano()/int64(window))

	count, err := l.redisClient.Incr(ctx, windowKey).Result()
	if err == nil && count == 1 {
		// Set the expiry once, when the window starts; refreshing it on every request kept
		// busy keys alive forever.
		err = l.redisClient.Expire(ctx, windowKey, window+time.Second).Err()
	}
	if err != nil {
		return l.redisFailure(key, limit, window, err)
	}

	remaining := limit - int(count)
	if remaining < 0 {
		remaining = 0
	}

	resetTime := start.Add(window)

	return &LimitInfo{
		Key:       key,
		Limit:     limit,
		Remaining: remaining,
		ResetTime: resetTime,
		Window:    window,
	}, int(count) <= limit
}

// redisFailure applies the configured failure mode when Redis cannot be used.
func (l *Limiter) redisFailure(key string, limit int, window time.Duration, err error) (*LimitInfo, bool) {
	l.logger.WithError(err).Error("Rate limit store unavailable")
	info := &LimitInfo{Key: key, Limit: limit, Remaining: 0, ResetTime: l.now().Add(window), Window: window}
	return info, !l.config.FailClosed
}

func (l *Limiter) checkSlidingWindow(ctx context.Context, key string, limit int, window time.Duration) (*LimitInfo, bool) {
	now := l.now()
	if l.redisClient == nil {
		count, allowed, reset := l.local.slidingWindow(key, limit, window, now)
		return &LimitInfo{
			Key:       key,
			Limit:     limit,
			Remaining: max(limit-count, 0),
			ResetTime: reset,
			Window:    window,
		}, allowed
	}

	windowStart := now.Add(-window)

	pipe := l.redisClient.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStart.UnixNano(), 10))
	countCmd := pipe.ZCard(ctx, key)
	// A unique member per request: two requests in the same nanosecond must count twice.
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixNano()), Member: fmt.Sprintf("%d-%d", now.UnixNano(), rand.Int64())}) //nolint:gosec // uniqueness, not security
	pipe.Expire(ctx, key, window+time.Minute)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return l.redisFailure(key, limit, window, err)
	}

	count := int(countCmd.Val())
	remaining := limit - count - 1
	if remaining < 0 {
		remaining = 0
	}

	return &LimitInfo{
		Key:       key,
		Limit:     limit,
		Remaining: remaining,
		ResetTime: now.Add(window),
		Window:    window,
	}, count < limit
}

func (l *Limiter) checkTokenBucket(ctx context.Context, key string, limit int, window time.Duration, burstSize int) (*LimitInfo, bool) {
	if burstSize <= 0 {
		burstSize = l.config.BurstSize
	}
	if burstSize <= 0 {
		burstSize = limit
	}

	if l.redisClient == nil {
		remaining, allowed, reset := l.local.tokenBucket(key, limit, burstSize, window, l.now())
		return &LimitInfo{
			Key:       key,
			Limit:     limit,
			Remaining: remaining,
			ResetTime: reset,
			Window:    window,
		}, allowed
	}
	// Distributed token buckets need a script; with Redis this approximates one with a fixed
	// window of the burst size.
	return l.checkFixedWindow(ctx, key, burstSize, window)
}

func (l *Limiter) setHeaders(c echo.Context, limitInfo *LimitInfo) {
	c.Response().Header().Set("X-RateLimit-Limit", strconv.Itoa(limitInfo.Limit))
	c.Response().Header().Set("X-RateLimit-Remaining", strconv.Itoa(limitInfo.Remaining))
	c.Response().Header().Set("X-RateLimit-Reset", strconv.FormatInt(limitInfo.ResetTime.Unix(), 10))
	c.Response().Header().Set("X-RateLimit-Window", limitInfo.Window.String())
}

func (l *Limiter) Close() error {
	if l.redisClient != nil {
		return l.redisClient.Close()
	}
	return nil
}

func (l *Limiter) checkLimit(ctx context.Context, key string, rule *Rule) (*LimitInfo, bool) {
	return l.CheckLimit(ctx, key, rule)
}

func (l *Limiter) GenerateKey(c echo.Context, rule *Rule) string {
	return l.generateKey(c, rule)
}

func (l *Limiter) Allow(key string) bool {
	rule := &Rule{
		Limit:  l.config.DefaultLimit,
		Window: l.config.DefaultWindow,
	}

	_, allowed := l.checkLimit(context.Background(), key, rule)
	return allowed
}

// ServiceMiddleware limits requests to one service: limit requests per window for each client,
// where the client is the authenticated user (when the service's JWT middleware ran first) or
// else the client IP. Responses carry X-RateLimit-* headers, and rejections a Retry-After.
func (l *Limiter) ServiceMiddleware(serviceName string, limit int, window time.Duration) echo.MiddlewareFunc {
	rule := &Rule{Name: serviceName, Limit: limit, Window: window}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			identity := "ip:" + c.RealIP()
			if user, ok := c.Get("user_id").(string); ok && user != "" {
				identity = "user:" + user
			}
			sum := sha256.Sum256([]byte(serviceName + "\x00" + identity))
			key := fmt.Sprintf("ratelimit:%x", sum)

			info, allowed := l.CheckLimit(c.Request().Context(), key, rule)
			h := c.Response().Header()
			h.Set("X-RateLimit-Limit", strconv.Itoa(info.Limit))
			h.Set("X-RateLimit-Remaining", strconv.Itoa(info.Remaining))
			h.Set("X-RateLimit-Reset", strconv.FormatInt(info.ResetTime.Unix(), 10))
			if !allowed {
				wait := int(math.Ceil(time.Until(info.ResetTime).Seconds()))
				if wait < 1 {
					wait = 1
				}
				h.Set("Retry-After", strconv.Itoa(wait))
				if l.config.LogViolations {
					l.logger.WithFields(logrus.Fields{"service": serviceName, "client": identity}).Warn("Rate limit exceeded")
				}
				return echo.NewHTTPError(http.StatusTooManyRequests, "Rate limit exceeded")
			}
			return next(c)
		}
	}
}
