package admin

import (
	"crypto/rand"
	"encoding/base64"
	"odin/pkg/config"
	"odin/pkg/plugins"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

type AdminCredentials struct {
	Username     string `yaml:"username"`
	Password     string `yaml:"password"`
	PasswordHash string `yaml:"passwordHash"`
}

type AdminHandler struct {
	config               *config.Config
	configPath           string
	logger               *logrus.Logger
	username             string
	password             string
	passwordHash         string // bcrypt; takes precedence over password
	enabled              bool
	sessions             *sessionStore
	logins               *loginLimiter
	allowedOrigins       map[string]bool // scheme://host, lower case
	cacheStore           interface{ Clear() }
	pluginHandler        *PluginHandler
	middlewareAPIHandler *MiddlewareAPIHandler
	integrationHandler   *IntegrationHandler
	pluginUploadHandler  *PluginUploadHandler
}

func loadAdminCredentials(logger *logrus.Logger) (*AdminCredentials, error) {
	credsPaths := []string{
		"config/admin_creds.yaml",
		"/etc/odin/admin_creds.yaml",
		filepath.Join(os.Getenv("HOME"), ".odin", "admin_creds.yaml"),
	}

	var credentials AdminCredentials
	var loaded bool

	for _, path := range credsPaths {
		if _, err := os.Stat(path); err == nil { //nolint:gosec // fixed list of well-known locations
			data, err := os.ReadFile(path) //nolint:gosec // fixed list of well-known locations
			if err != nil {
				logger.WithError(err).Warnf("Failed to read admin credentials file: %s", path)
				continue
			}

			err = yaml.Unmarshal(data, &credentials)
			if err != nil {
				logger.WithError(err).Warnf("Failed to parse admin credentials file: %s", path)
				continue
			}

			loaded = true
			break
		}
	}

	if !loaded {
		return nil, nil
	}

	return &credentials, nil
}

// New creates the admin handler. Credentials come from, in order: an admin_creds.yaml file,
// the ODIN_ADMIN_USERNAME / ODIN_ADMIN_PASSWORD / ODIN_ADMIN_PASSWORD_HASH environment variables,
// then admin.username / admin.password / admin.passwordHash in the config. There is no built-in
// password: when admin is enabled without one, a random password is generated for this run and
// logged once.
func New(cfg *config.Config, configPath string, logger *logrus.Logger) *AdminHandler {
	creds, _ := loadAdminCredentials(logger)
	if creds == nil {
		creds = &AdminCredentials{}
	}

	username := firstNonEmpty(creds.Username, os.Getenv("ODIN_ADMIN_USERNAME"), cfg.Admin.Username, "admin")
	password := firstNonEmpty(creds.Password, os.Getenv("ODIN_ADMIN_PASSWORD"), cfg.Admin.Password)
	passwordHash := firstNonEmpty(creds.PasswordHash, os.Getenv("ODIN_ADMIN_PASSWORD_HASH"), cfg.Admin.PasswordHash)

	if cfg.Admin.Enabled && password == "" && passwordHash == "" {
		generated, err := randomPassword()
		if err != nil {
			logger.WithError(err).Error("Failed to generate an admin password; the admin login will reject everyone")
		} else {
			password = generated
			logger.WithFields(logrus.Fields{
				"username": username,
				"password": generated,
			}).Warn("No admin password configured: generated one for this run. " +
				"Set admin.passwordHash (bcrypt), admin.password or ODIN_ADMIN_PASSWORD to choose your own.")
		}
	}

	return &AdminHandler{
		config:               cfg,
		configPath:           configPath,
		logger:               logger,
		username:             username,
		password:             password,
		passwordHash:         passwordHash,
		enabled:              cfg.Admin.Enabled,
		sessions:             newSessionStore(),
		logins:               newLoginLimiter(),
		allowedOrigins:       parseOrigins(cfg.Admin.AllowedOrigins, os.Getenv("ODIN_ADMIN_ALLOWED_ORIGINS"), logger),
		pluginHandler:        nil, // Will be set later via SetPluginHandler
		middlewareAPIHandler: nil, // Will be set later via SetMiddlewareAPIHandler
		integrationHandler:   nil, // Will be set later via SetIntegrationHandler
		pluginUploadHandler:  nil, // Will be set later via SetPluginUploadHandler
	}
}

// SetCacheStore gives the settings page access to the response cache (for "clear cache").
func (h *AdminHandler) SetCacheStore(store interface{ Clear() }) {
	h.cacheStore = store
}

// SetPluginHandler sets the plugin handler for admin
func (h *AdminHandler) SetPluginHandler(pluginManager *plugins.PluginManager, pluginRepo *plugins.PluginRepository) {
	h.pluginHandler = NewPluginHandler(pluginManager, pluginRepo)
	if h.config.Plugins.Directory != "" {
		h.pluginHandler.pluginsDir = h.config.Plugins.Directory
	}
}

// SetMiddlewareAPIHandler sets the middleware API handler for admin
func (h *AdminHandler) SetMiddlewareAPIHandler(pluginManager *plugins.PluginManager, pluginRepo *plugins.PluginRepository) {
	h.middlewareAPIHandler = NewMiddlewareAPIHandler(pluginManager, pluginRepo)
}

// SetIntegrationHandler sets the integration handler for admin
func (h *AdminHandler) SetIntegrationHandler(handler *IntegrationHandler) {
	h.integrationHandler = handler
	handler.SetServiceLookup(func(name string) (*config.ServiceConfig, bool) {
		configMu.Lock()
		defer configMu.Unlock()
		for i := range h.config.Services {
			if h.config.Services[i].Name == name {
				svc := h.config.Services[i]
				return &svc, true
			}
		}
		return nil, false
	})
}

// SetPluginUploadHandler sets the plugin upload handler for admin
func (h *AdminHandler) SetPluginUploadHandler(handler *PluginUploadHandler) {
	h.pluginUploadHandler = handler
}

// GetIntegrationHandler returns the integration handler
func (h *AdminHandler) GetIntegrationHandler() *IntegrationHandler {
	return h.integrationHandler
}

func (h *AdminHandler) saveConfig() error {
	if err := writeConfigFile(h.configPath, h.config); err != nil {
		h.logger.WithError(err).Error("Failed to save configuration")
		return err
	}
	h.logger.Info("Configuration saved successfully")
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func randomPassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
