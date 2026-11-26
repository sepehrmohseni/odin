package admin

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"odin/pkg/config"

	"gopkg.in/yaml.v3"
)

// configMu serialises every read-modify-write of the configuration by the admin handlers, so two
// concurrent edits cannot interleave or truncate the file.
var configMu sync.Mutex

// writeConfigFile saves cfg to path atomically (temporary file in the same directory, then
// rename) with owner-only permissions, since the file holds secrets. Services that were loaded
// from a separate services.yaml are left out: they belong to that file, and writing them into
// config.yaml duplicated them on every save.
func writeConfigFile(path string, cfg *config.Config) error {
	out := *cfg
	out.Services = make([]config.ServiceConfig, 0, len(cfg.Services))
	for i := range cfg.Services {
		if cfg.Services[i].Source == "" {
			out.Services = append(out.Services, cfg.Services[i])
		}
	}

	data, err := yaml.Marshal(&out)
	if err != nil {
		return fmt.Errorf("marshal configuration: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary config file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write configuration: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set configuration permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write configuration: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace configuration: %w", err)
	}
	return nil
}

// redacted replaces secret values in everything the admin API returns.
const redacted = "********"

func redactString(s string) string {
	if s == "" {
		return ""
	}
	return redacted
}

// redactURL hides the password in a URL's user info (redis://:pw@host, mongodb://u:pw@host).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); !has {
		return raw
	}
	u.User = url.UserPassword(u.User.Username(), redacted)
	return u.String()
}

// keepSecretURL returns the stored URL when the submitted one is the redacted form of it, so a
// settings form that round-trips a redacted value does not overwrite the real password.
func keepSecretURL(submitted, stored string) string {
	if submitted == redactURL(stored) {
		return stored
	}
	return submitted
}

// redactConfig returns a copy of cfg that is safe to show: passwords, tokens, the JWT secret and
// credentials inside URLs are masked. The original is not modified.
func redactConfig(cfg *config.Config) *config.Config {
	out := *cfg

	out.Auth.JWTSecret = redactString(cfg.Auth.JWTSecret)
	out.Admin.Password = redactString(cfg.Admin.Password)
	out.Admin.PasswordHash = redactString(cfg.Admin.PasswordHash)
	out.RateLimit.RedisURL = redactURL(cfg.RateLimit.RedisURL)
	out.Cache.RedisURL = redactURL(cfg.Cache.RedisURL)
	out.MongoDB.URI = redactURL(cfg.MongoDB.URI)
	out.MongoDB.Auth.Password = redactString(cfg.MongoDB.Auth.Password)

	if cfg.ServiceMesh.Consul != nil {
		consul := *cfg.ServiceMesh.Consul
		consul.Token = redactString(consul.Token)
		out.ServiceMesh.Consul = &consul
	}

	out.MultiCluster.Clusters = make([]config.ClusterConfig, len(cfg.MultiCluster.Clusters))
	for i, cl := range cfg.MultiCluster.Clusters {
		cl.Auth.Token = redactString(cl.Auth.Token)
		cl.Auth.Password = redactString(cl.Auth.Password)
		out.MultiCluster.Clusters[i] = cl
	}

	return &out
}
