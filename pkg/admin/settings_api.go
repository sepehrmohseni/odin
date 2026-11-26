package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"odin/pkg/config"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"
)

// SettingsHandler handles gateway settings management
type SettingsHandler struct {
	configPath string
	config     *config.Config
	cacheStore interface{ Clear() } // the response cache, when enabled
}

// NewSettingsHandler creates a new settings handler
func NewSettingsHandler(configPath string, cfg *config.Config) *SettingsHandler {
	return &SettingsHandler{
		configPath: configPath,
		config:     cfg,
	}
}

// GetAllSettings returns all gateway settings
func (h *SettingsHandler) GetAllSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, redactConfig(h.config))
}

// GetServerSettings returns server configuration
func (h *SettingsHandler) GetServerSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"server": h.config.Server,
	})
}

// UpdateServerSettings updates server configuration
func (h *SettingsHandler) UpdateServerSettings(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	var req struct {
		Port            int    `json:"port"`
		Timeout         string `json:"timeout"`
		ReadTimeout     string `json:"readTimeout"`
		WriteTimeout    string `json:"writeTimeout"`
		GracefulTimeout string `json:"gracefulTimeout"`
		Compression     bool   `json:"compression"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}

	// Validate port
	if req.Port <= 0 || req.Port > 65535 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Port must be between 1 and 65535"})
	}

	// Parse durations
	timeout, err := time.ParseDuration(req.Timeout)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid timeout duration"})
	}

	readTimeout, err := time.ParseDuration(req.ReadTimeout)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid readTimeout duration"})
	}

	writeTimeout, err := time.ParseDuration(req.WriteTimeout)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid writeTimeout duration"})
	}

	gracefulTimeout, err := time.ParseDuration(req.GracefulTimeout)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid gracefulTimeout duration"})
	}

	// Update config
	h.config.Server.Port = req.Port
	h.config.Server.Timeout = timeout
	h.config.Server.ReadTimeout = readTimeout
	h.config.Server.WriteTimeout = writeTimeout
	h.config.Server.GracefulTimeout = gracefulTimeout
	h.config.Server.Compression = req.Compression

	// Save to file
	if err := h.saveConfig(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save configuration"})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Server settings updated successfully. Restart required to apply changes.",
	})
}

// GetLoggingSettings returns logging configuration
func (h *SettingsHandler) GetLoggingSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"logging": h.config.Logging,
	})
}

// UpdateLoggingSettings updates logging configuration
func (h *SettingsHandler) UpdateLoggingSettings(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	var req struct {
		Level string `json:"level"`
		JSON  bool   `json:"json"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}

	// Validate log level
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[req.Level] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid log level. Must be debug, info, warn, or error"})
	}

	// Update config
	h.config.Logging.Level = req.Level
	h.config.Logging.JSON = req.JSON

	// Save to file
	if err := h.saveConfig(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save configuration"})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Logging settings updated successfully",
	})
}

// GetRateLimitSettings returns rate limiting configuration
func (h *SettingsHandler) GetRateLimitSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"rateLimit": redactConfig(h.config).RateLimit,
	})
}

// UpdateRateLimitSettings updates rate limiting configuration
func (h *SettingsHandler) UpdateRateLimitSettings(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	var req struct {
		Enabled  bool   `json:"enabled"`
		Limit    int    `json:"limit"`
		Duration string `json:"duration"`
		Strategy string `json:"strategy"`
		RedisURL string `json:"redisUrl"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}

	// Validate
	if req.Limit <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Limit must be greater than 0"})
	}

	duration, err := time.ParseDuration(req.Duration)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid duration"})
	}

	// Update config
	h.config.RateLimit.Enabled = req.Enabled
	h.config.RateLimit.Limit = req.Limit
	h.config.RateLimit.Duration = duration
	h.config.RateLimit.Strategy = req.Strategy
	h.config.RateLimit.RedisURL = keepSecretURL(req.RedisURL, h.config.RateLimit.RedisURL)

	// Save to file
	if err := h.saveConfig(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save configuration"})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Rate limit settings updated successfully. Restart required to apply changes.",
	})
}

// GetCacheSettings returns cache configuration
func (h *SettingsHandler) GetCacheSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"cache": redactConfig(h.config).Cache,
	})
}

// UpdateCacheSettings updates cache configuration
func (h *SettingsHandler) UpdateCacheSettings(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	var req struct {
		Enabled     bool   `json:"enabled"`
		TTL         string `json:"ttl"`
		RedisURL    string `json:"redisUrl"`
		Strategy    string `json:"strategy"`
		MaxSizeInMB int    `json:"maxSizeInMB"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}

	ttl, err := time.ParseDuration(req.TTL)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid TTL duration"})
	}

	// Validate strategy
	validStrategies := map[string]bool{"local": true, "redis": true}
	if !validStrategies[req.Strategy] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid strategy. Must be local or redis"})
	}

	// Update config
	h.config.Cache.Enabled = req.Enabled
	h.config.Cache.TTL = ttl
	h.config.Cache.RedisURL = keepSecretURL(req.RedisURL, h.config.Cache.RedisURL)
	h.config.Cache.Strategy = req.Strategy
	h.config.Cache.MaxSizeInMB = req.MaxSizeInMB

	// Save to file
	if err := h.saveConfig(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save configuration"})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Cache settings updated successfully. Restart required to apply changes.",
	})
}

// GetMonitoringSettings returns monitoring configuration
func (h *SettingsHandler) GetMonitoringSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"monitoring": h.config.Monitoring,
	})
}

// UpdateMonitoringSettings updates monitoring configuration
func (h *SettingsHandler) UpdateMonitoringSettings(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	var req struct {
		Enabled    bool   `json:"enabled"`
		Path       string `json:"path"`
		WebhookURL string `json:"webhookUrl"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}

	// Update config
	h.config.Monitoring.Enabled = req.Enabled
	h.config.Monitoring.Path = req.Path
	h.config.Monitoring.WebhookURL = req.WebhookURL

	// Save to file
	if err := h.saveConfig(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save configuration"})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Monitoring settings updated successfully",
	})
}

// GetTracingSettings returns tracing configuration
func (h *SettingsHandler) GetTracingSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"tracing": h.config.Tracing,
	})
}

// UpdateTracingSettings updates tracing configuration
func (h *SettingsHandler) UpdateTracingSettings(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	var req struct {
		Enabled        bool    `json:"enabled"`
		ServiceName    string  `json:"serviceName"`
		ServiceVersion string  `json:"serviceVersion"`
		Environment    string  `json:"environment"`
		Endpoint       string  `json:"endpoint"`
		SampleRate     float64 `json:"sampleRate"`
		Insecure       bool    `json:"insecure"`
	}

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}

	// Validate sample rate
	if req.SampleRate < 0 || req.SampleRate > 1 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Sample rate must be between 0 and 1"})
	}

	// Update config
	h.config.Tracing.Enabled = req.Enabled
	h.config.Tracing.ServiceName = req.ServiceName
	h.config.Tracing.ServiceVersion = req.ServiceVersion
	h.config.Tracing.Environment = req.Environment
	h.config.Tracing.Endpoint = req.Endpoint
	h.config.Tracing.SampleRate = req.SampleRate
	h.config.Tracing.Insecure = req.Insecure

	// Save to file
	if err := h.saveConfig(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to save configuration"})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Tracing settings updated successfully. Restart required to apply changes.",
	})
}

// GetGatewayInfo returns gateway runtime information
func (h *SettingsHandler) GetGatewayInfo(c echo.Context) error {
	// Read config file modification time
	fileInfo, err := os.Stat(h.configPath)
	var lastModified string
	if err == nil {
		lastModified = fileInfo.ModTime().Format(time.RFC3339)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"configPath":     h.configPath,
		"configModified": lastModified,
		"servicesCount":  len(h.config.Services),
		"pluginsCount":   len(h.config.Plugins.Plugins),
		"features": map[string]bool{
			"rateLimit":   h.config.RateLimit.Enabled,
			"cache":       h.config.Cache.Enabled,
			"monitoring":  h.config.Monitoring.Enabled,
			"tracing":     h.config.Tracing.Enabled,
			"plugins":     h.config.Plugins.Enabled,
			"wasm":        h.config.WASM.Enabled,
			"serviceMesh": h.config.ServiceMesh.Enabled,
			"openapi":     h.config.OpenAPI.Enabled,
			"mongodb":     h.config.MongoDB.Enabled,
			"ai":          h.config.AI.Enabled,
		},
	})
}

// ExportConfig exports the current configuration
func (h *SettingsHandler) ExportConfig(c echo.Context) error {
	// Secrets are masked in the export; keep them in your secret store, not in downloads.
	data, err := yaml.Marshal(redactConfig(h.config))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to marshal configuration"})
	}

	c.Response().Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=config-%s.yaml", time.Now().Format("20060102-150405")))
	return c.Blob(http.StatusOK, "application/x-yaml", data)
}

// ValidateConfig validates the configuration without saving
func (h *SettingsHandler) ValidateConfig(c echo.Context) error {
	var req config.Config

	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"valid":  false,
			"errors": []string{"Invalid configuration format"},
		})
	}

	// Perform validation
	errors := []string{}

	if req.Server.Port <= 0 || req.Server.Port > 65535 {
		errors = append(errors, "Invalid server port")
	}

	for _, service := range req.Services {
		if service.Name == "" {
			errors = append(errors, "Service name cannot be empty")
		}
		if service.BasePath == "" {
			errors = append(errors, fmt.Sprintf("Service %s: basePath cannot be empty", service.Name))
		}
		if len(service.Targets) == 0 {
			errors = append(errors, fmt.Sprintf("Service %s: at least one target must be specified", service.Name))
		}
	}

	if len(errors) > 0 {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"valid":  false,
			"errors": errors,
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"valid":   true,
		"message": "Configuration is valid",
	})
}

// saveConfig saves the current configuration to file. Callers hold configMu.
func (h *SettingsHandler) saveConfig() error {
	backupPath := h.configPath + ".backup-" + time.Now().Format("20060102-150405")
	if err := h.createBackup(backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to create backup: %w", err)
	}
	return writeConfigFile(h.configPath, h.config)
}

// createBackup creates a backup of the current configuration
func (h *SettingsHandler) createBackup(backupPath string) error {
	data, err := os.ReadFile(h.configPath)
	if err != nil {
		return err
	}

	// Ensure backup directory exists
	backupDir := filepath.Join(filepath.Dir(h.configPath), "backups")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return err
	}

	backupFile := filepath.Join(backupDir, filepath.Base(backupPath))
	return os.WriteFile(backupFile, data, 0o600) //nolint:gosec // name reduced to its base inside the backups dir
}

// GetConfigBackups lists available configuration backups
func (h *SettingsHandler) GetConfigBackups(c echo.Context) error {
	backupDir := filepath.Join(filepath.Dir(h.configPath), "backups")

	files, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return c.JSON(http.StatusOK, []interface{}{})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to read backups directory"})
	}

	backups := []map[string]interface{}{}
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		info, err := file.Info()
		if err != nil {
			continue
		}

		backups = append(backups, map[string]interface{}{
			"name":     file.Name(),
			"size":     info.Size(),
			"modified": info.ModTime().Format(time.RFC3339),
		})
	}

	return c.JSON(http.StatusOK, backups)
}

// RestoreConfigBackup restores a configuration backup
func (h *SettingsHandler) RestoreConfigBackup(c echo.Context) error {
	configMu.Lock()
	defer configMu.Unlock()

	backupName := c.Param("name")
	// Only a plain file name inside the backups directory: no separators, no "..".
	if backupName == "" || backupName != filepath.Base(backupName) || strings.HasPrefix(backupName, ".") {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid backup name"})
	}

	backupPath := filepath.Join(filepath.Dir(h.configPath), "backups", backupName)

	// Read backup file
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "Backup not found"})
	}

	// Validate backup before restoring
	var testConfig config.Config
	if err := yaml.Unmarshal(data, &testConfig); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid backup file"})
	}

	// Create backup of current config before restoring
	if err := h.createBackup(h.configPath + ".before-restore"); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to backup current config"})
	}

	// Restore backup
	if err := os.WriteFile(h.configPath, data, 0o600); err != nil { //nolint:gosec // configPath is the gateway's own config file
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to restore backup"})
	}

	// Update the shared config in place, so the service editor (which holds the same pointer)
	// does not later write the old configuration back over the restored file.
	*h.config = testConfig

	return c.JSON(http.StatusOK, map[string]string{
		"message": "Configuration restored successfully. Restart required to apply changes.",
	})
}

// GetSystemStats returns system statistics
func (h *SettingsHandler) GetSystemStats(c echo.Context) error {
	metrics := GetCollector().GetMetrics()

	return c.JSON(http.StatusOK, map[string]interface{}{
		"requests":          metrics.TotalRequests,
		"avgResponseTime":   metrics.AvgResponseTime,
		"activeConnections": metrics.ActiveConnections,
		"successRate":       metrics.SuccessRate,
		"errorRate":         (1.0 - metrics.SuccessRate) * 100,
		"timestamp":         time.Now().Unix(),
	})
}

// ClearCache clears the gateway's response cache.
func (h *SettingsHandler) ClearCache(c echo.Context) error {
	if h.cacheStore == nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "Response caching is not enabled"})
	}
	h.cacheStore.Clear()
	return c.JSON(http.StatusOK, map[string]string{"message": "Cache cleared"})
}

// ReloadConfig would apply the configuration without a restart. That is not supported yet, so
// say so instead of reporting a reload that does not happen.
func (h *SettingsHandler) ReloadConfig(c echo.Context) error {
	return c.JSON(http.StatusNotImplemented, map[string]string{
		"error": "Live reload is not supported yet; restart the gateway to apply configuration changes.",
	})
}

// GetConfigAsJSON returns the current configuration as JSON
func (h *SettingsHandler) GetConfigAsJSON(c echo.Context) error {
	data, err := json.MarshalIndent(redactConfig(h.config), "", "  ")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to marshal configuration"})
	}

	return c.JSONBlob(http.StatusOK, data)
}
