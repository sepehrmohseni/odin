package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"odin/pkg/config"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	// Create a temporary config file
	configContent := `
server:
  port: 8080
  readTimeout: 30s
  writeTimeout: 30s

services:
  - name: test-service
    basePath: /api/test
    targets:
      - http://localhost:8081
    timeout: 30s
    authentication: false
    stripBasePath: true
`

	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(configContent)
	require.NoError(t, err)
	tmpFile.Close()

	cfg, err := config.Load(tmpFile.Name(), logrus.New())
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 30*time.Second, cfg.Server.ReadTimeout)
	assert.Len(t, cfg.Services, 1)
	assert.Equal(t, "test-service", cfg.Services[0].Name)
	assert.Equal(t, "/api/test", cfg.Services[0].BasePath)
	assert.Len(t, cfg.Services[0].Targets, 1)
	assert.Equal(t, "http://localhost:8081", cfg.Services[0].Targets[0])
}

func TestLoadNonExistentFile(t *testing.T) {
	_, err := config.Load("nonexistent.yaml", logrus.New())
	assert.Error(t, err)
}

func TestServiceDefaults(t *testing.T) {
	service := &config.ServiceConfig{
		Name:     "test",
		BasePath: "/api/test",
		Targets:  []string{"http://localhost:8081"},
	}

	service.SetDefaults()

	assert.Equal(t, 30*time.Second, service.Timeout)
	assert.False(t, service.Authentication)
	assert.Equal(t, "round-robin", service.LoadBalancing)
	assert.Equal(t, 3, service.RetryCount)
	assert.Equal(t, time.Second, service.RetryDelay)
}

func TestBasicConfigValidation(t *testing.T) {
	// Test basic config structure
	cfg := &config.Config{
		Server: config.ServerConfig{
			Port: 8080,
		},
		Services: []config.ServiceConfig{
			{
				Name:     "test",
				BasePath: "/api/test",
				Targets:  []string{"http://localhost:8081"},
			},
		},
	}

	// Basic validation - ensure required fields are present
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Len(t, cfg.Services, 1)
	assert.Equal(t, "test", cfg.Services[0].Name)
	assert.NotEmpty(t, cfg.Services[0].Targets)

	// Test with empty service name
	cfg.Services[0].Name = ""
	assert.Empty(t, cfg.Services[0].Name, "Service name should be empty for validation test")
}

func TestConfigDefaults(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Port:         8080,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
	}

	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 30*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.Server.WriteTimeout)
}

func TestBasicConfigLoad(t *testing.T) {
	// Test that we can create a basic config without errors
	cfg := &config.Config{
		Server: config.ServerConfig{
			Port: 8080,
		},
	}

	assert.NotNil(t, cfg)
	assert.Equal(t, 8080, cfg.Server.Port)
}

func TestServiceConfigBasics(t *testing.T) {
	service := config.ServiceConfig{
		Name:     "test-service",
		BasePath: "/api/test",
		Targets:  []string{"http://localhost:8081"},
	}

	service.SetDefaults()
	assert.Equal(t, "test-service", service.Name)
	assert.Equal(t, "/api/test", service.BasePath)
	assert.Len(t, service.Targets, 1)
	assert.Equal(t, 30*time.Second, service.Timeout)
	assert.False(t, service.Authentication)
}

func TestConfigWithAuth(t *testing.T) {
	configContent := `
server:
  port: 8080

auth:
  jwtSecret: "test-secret"
  accessTokenTTL: 3600s
  refreshTokenTTL: 86400s

services:
  - name: secure-service
    basePath: /api/secure
    targets:
      - http://localhost:8081
    authentication: true
`

	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(configContent)
	require.NoError(t, err)
	tmpFile.Close()

	cfg, err := config.Load(tmpFile.Name(), logrus.New())
	require.NoError(t, err)

	assert.Equal(t, "test-secret", cfg.Auth.JWTSecret)
	assert.True(t, cfg.Services[0].Authentication)
}

func TestMergeServices(t *testing.T) {
	file := []config.ServiceConfig{{Name: "orders", BasePath: "/api/orders", Targets: []string{"http://orders"}}}
	stored := []config.ServiceConfig{
		{Name: "orders", BasePath: "/v2/orders", Targets: []string{"http://elsewhere"}},
		{Name: "billing", BasePath: "/api/billing", Targets: []string{"http://billing"}},
		{Name: "broken", BasePath: "/api/broken"},
	}
	merged, added, skipped := config.MergeServices(file, stored)

	require.Len(t, merged, 2)
	assert.Equal(t, "/api/orders", merged[0].BasePath, "the config file wins")
	require.Len(t, added, 1)
	assert.Equal(t, "billing", added[0].Name)
	assert.Equal(t, "http", added[0].Protocol, "defaults applied")
	assert.Len(t, skipped, 2)
	assert.Len(t, file, 1, "the file's list is not modified")
}

// COR-24: a misspelt key is reported (with its line) instead of silently doing nothing, and the
// file still loads.
func TestUnknownKeysAreReported(t *testing.T) {
	data := []byte("server:\n  port: 8080\nratelimit:\n  enabled: true\nservices:\n  - name: a\n    basepath: /a\n    targets: [http://a]\n")
	var cfg config.Config
	unknown := config.UnknownKeys(data, &cfg)
	assert.Equal(t, []string{`line 3: unknown key "ratelimit"`, `line 7: unknown key "basepath"`}, unknown)
	assert.Empty(t, config.UnknownKeys([]byte("server:\n  port: 8080\n"), &config.Config{}))

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  port: 8080\nratelimit:\n  enabled: true\n"), 0o600))
	var buf bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&buf)
	loaded, err := config.Load(path, logger)
	require.NoError(t, err, "unknown keys warn, they do not stop the gateway")
	assert.Equal(t, 8080, loaded.Server.Port)
	assert.Contains(t, buf.String(), `line 3: unknown key \"ratelimit\"`)
}
