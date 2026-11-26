package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"odin/pkg/config"
	"odin/pkg/gateway"
	"odin/pkg/logging"
)

// version and buildTime are set at build time (see the Makefile and Dockerfile).
var (
	version   = "dev"
	buildTime = ""
)

func main() {
	var configPath string
	var showVersion bool
	flag.StringVar(&configPath, "config", "config/config.yaml", "Path to configuration file")
	flag.BoolVar(&showVersion, "version", false, "Print the version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("odin %s %s\n", version, buildTime)
		return
	}

	logger := logging.NewLogger()

	if envLogLevel := os.Getenv("LOG_LEVEL"); envLogLevel != "" {
		loggingConfig := logging.Config{
			Level: envLogLevel,
			JSON:  false,
		}
		logging.ConfigureLogger(logger, loggingConfig)
	}

	logger.Infof("Starting Odin API Gateway %s", version)
	logger.Infof("Loading configuration from %s", configPath)

	cfg, err := config.Load(configPath, logger)
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	// Configure logging
	loggingConfig := logging.Config{
		Level: cfg.Logging.Level,
		JSON:  cfg.Logging.JSON,
	}
	logging.ConfigureLogger(logger, loggingConfig)

	// Apply environment variable overrides
	if envPort := os.Getenv("GATEWAY_PORT"); envPort != "" {
		var port int
		if _, err := fmt.Sscanf(envPort, "%d", &port); err == nil {
			cfg.Server.Port = port
		}
	}

	// Log enabled features
	features := []string{"HTTP routing"}

	if cfg.Plugins.Enabled {
		features = append(features, "plugin system")
	}

	httpServices := 0
	graphqlServices := 0
	grpcServices := 0

	for _, svc := range cfg.Services {
		switch svc.Protocol {
		case "graphql":
			graphqlServices++
		case "grpc":
			grpcServices++
		default:
			httpServices++
		}
	}

	if graphqlServices > 0 {
		features = append(features, fmt.Sprintf("GraphQL proxy (%d services)", graphqlServices))
	}

	if grpcServices > 0 {
		features = append(features, fmt.Sprintf("gRPC proxy (%d services)", grpcServices))
	}

	if cfg.Auth.JWTSecret != "" || len(cfg.Auth.IgnorePathRegexes) > 0 {
		features = append(features, "JWT authentication")
	}

	if cfg.Cache.Enabled {
		features = append(features, "response caching")
	}

	if cfg.RateLimit.Enabled {
		features = append(features, "rate limiting")
	}

	if cfg.Monitoring.Enabled {
		features = append(features, "Prometheus metrics")
	}

	logger.Infof("Enabled features: %v", features)
	logger.Infof("Services configured: %d HTTP, %d GraphQL, %d gRPC", httpServices, graphqlServices, grpcServices)

	gw, err := gateway.New(cfg, configPath, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize gateway: %v", err)
	}

	// Start the server in a goroutine
	go func() {
		if err := gw.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Setup signal handling
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Handle SIGHUP for config reload
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)

	// Live reload is not implemented: SIGHUP only checks the file, so a restart can be planned
	// with confidence. (It used to log "Configuration reloaded successfully" without applying
	// anything.)
	go func() {
		for range reload {
			logger.Info("Received SIGHUP: validating the configuration file")
			if _, err := config.Load(configPath, logger); err != nil {
				logger.Errorf("Configuration is invalid: %v", err)
			} else {
				logger.Warn("Configuration is valid. Live reload is not supported yet; restart the gateway to apply changes")
			}
		}
	}()

	// Block until signal is received
	sig := <-quit
	logger.Infof("Received signal: %v, shutting down...", sig)

	// Create a deadline for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.GracefulTimeout)
	defer cancel()

	// Attempt graceful shutdown
	if err := gw.Shutdown(ctx); err != nil {
		logger.Fatalf("Server forced to shutdown: %v", err)
	}

	logger.Info("Server gracefully shut down")
}
