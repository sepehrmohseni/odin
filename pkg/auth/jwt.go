package auth

import (
	"fmt"
	"net/http"
	"odin/pkg/config"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"
)

type AuthSecrets struct {
	JWTSecret string `yaml:"jwtSecret"`
}

type JWTConfig struct {
	Secret          string        `yaml:"secret"`
	AccessTokenTTL  time.Duration `yaml:"accessTokenTTL"`
	RefreshTokenTTL time.Duration `yaml:"refreshTokenTTL"`
}

type Claims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

type JWTManager struct {
	secret          string
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

func NewJWTManager(config JWTConfig) *JWTManager {
	return &JWTManager{
		secret:          config.Secret,
		accessTokenTTL:  config.AccessTokenTTL,
		refreshTokenTTL: config.RefreshTokenTTL,
	}
}

func (jm *JWTManager) GenerateToken(claims *Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(jm.secret))
}

func (jm *JWTManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jm.secret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, fmt.Errorf("invalid token")
}

func loadJWTSecret() (string, error) {
	secretsPaths := []string{
		"config/auth_secrets.yaml",
		"/etc/odin/auth_secrets.yaml",
		filepath.Join(os.Getenv("HOME"), ".odin", "auth_secrets.yaml"),
	}

	for _, path := range secretsPaths {
		if _, err := os.Stat(path); err == nil { //nolint:gosec // fixed list of well-known locations
			data, err := os.ReadFile(path) //nolint:gosec // fixed list of well-known locations
			if err != nil {
				continue
			}

			var secrets AuthSecrets
			if err := yaml.Unmarshal(data, &secrets); err != nil {
				continue
			}

			if secrets.JWTSecret != "" {
				return secrets.JWTSecret, nil
			}
		}
	}

	if envSecret := os.Getenv("ODIN_JWT_SECRET"); envSecret != "" {
		return envSecret, nil
	}

	return "", fmt.Errorf("couldn't load JWT secret from any source")
}

type JWTClaims struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// JWTValidator checks bearer tokens for services with authentication enabled.
type JWTValidator struct {
	secret      []byte
	ignorePaths []*regexp.Regexp
	parser      *jwt.Parser
}

// NewJWTValidator builds the validator from the auth config. The signing secret comes from, in
// order: an auth_secrets.yaml file, the ODIN_JWT_SECRET environment variable, auth.jwtSecret.
// An empty secret is allowed here (gateways without authenticated services need none), but then
// every token is rejected: see Configured.
func NewJWTValidator(cfg config.AuthConfig) (*JWTValidator, error) {
	secret, err := loadJWTSecret()
	if err != nil || secret == "" {
		secret = cfg.JWTSecret
	}

	ignore := make([]*regexp.Regexp, 0, len(cfg.IgnorePathRegexes))
	for _, expr := range cfg.IgnorePathRegexes {
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("auth.ignorePathRegexes: invalid pattern %q: %w", expr, err)
		}
		ignore = append(ignore, re)
	}

	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"HS256", "HS384", "HS512"}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(cfg.Leeway),
	}
	if cfg.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(cfg.Issuer))
	}
	if cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(cfg.Audience))
	}

	return &JWTValidator{
		secret:      []byte(secret),
		ignorePaths: ignore,
		parser:      jwt.NewParser(opts...),
	}, nil
}

// Configured reports whether a signing secret is set.
func (v *JWTValidator) Configured() bool { return len(v.secret) > 0 }

// Validate checks the request's bearer token and returns its claims. Tokens must be HMAC-signed
// with the configured secret and carry an expiry; issuer and audience are checked when set.
func (v *JWTValidator) Validate(r *http.Request) (*JWTClaims, error) {
	if !v.Configured() {
		// Fail closed: with an empty key every HMAC "verifies" against a forged token.
		return nil, errAuthNotConfigured
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, errMissingToken
	}
	scheme, token, ok := strings.Cut(authHeader, " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return nil, errBadAuthFormat
	}

	claims := &JWTClaims{}
	parsed, err := v.parser.ParseWithClaims(token, claims, func(*jwt.Token) (interface{}, error) {
		return v.secret, nil
	})
	if err != nil || !parsed.Valid {
		return nil, errInvalidToken
	}
	return claims, nil
}

// Ignored reports whether path is exempt from authentication (auth.ignorePathRegexes).
func (v *JWTValidator) Ignored(path string) bool {
	for _, re := range v.ignorePaths {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// Middleware rejects requests without a valid token. On success the claims are stored in the
// context as "user" (*JWTClaims) and the subject as "user_id".
func (v *JWTValidator) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if v.Ignored(c.Request().URL.Path) {
				return next(c)
			}
			claims, err := v.Validate(c.Request())
			if err != nil {
				return authError(err)
			}
			c.Set("user", claims)
			c.Set("user_id", claims.Subject())
			return next(c)
		}
	}
}

var (
	errAuthNotConfigured = fmt.Errorf("authentication is not configured")
	errMissingToken      = fmt.Errorf("missing authorization header")
	errBadAuthFormat     = fmt.Errorf("invalid authorization format")
	errInvalidToken      = fmt.Errorf("invalid or expired token")
)

func authError(err error) error {
	if err == errAuthNotConfigured {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "Authentication is not configured on this gateway")
	}
	he := echo.NewHTTPError(http.StatusUnauthorized, strings.ToUpper(err.Error()[:1])+err.Error()[1:])
	return he
}

// NewJWTMiddleware returns the JWT middleware for cfg. Invalid ignore patterns make it reject
// every request (use NewJWTValidator to get the error instead).
func NewJWTMiddleware(cfg config.AuthConfig) echo.MiddlewareFunc {
	v, err := NewJWTValidator(cfg)
	if err != nil {
		return func(echo.HandlerFunc) echo.HandlerFunc {
			return func(echo.Context) error {
				return echo.NewHTTPError(http.StatusInternalServerError, "Authentication is misconfigured")
			}
		}
	}
	return v.Middleware()
}

// Subject returns the user identifier of the token: user_id, or the standard sub claim.
func (c *JWTClaims) Subject() string {
	if c.UserID != "" {
		return c.UserID
	}
	return c.RegisteredClaims.Subject
}

func GenerateToken(userID, username, role string, secret string, expiry time.Duration) (string, error) {
	if secret == "" {
		var err error
		secret, err = loadJWTSecret()
		if err != nil {
			return "", fmt.Errorf("no JWT secret available: %w", err)
		}
	}

	claims := &JWTClaims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}
