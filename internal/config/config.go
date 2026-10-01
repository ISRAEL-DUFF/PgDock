// Package config loads pgdock-server configuration from the environment.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config is the pgdock-server configuration.
type Config struct {
	// ListenAddr is the address the HTTP server binds to.
	ListenAddr string
	// LogLevel is one of debug, info, warn, error.
	LogLevel slog.Level
	// LogFormat is "json" or "text".
	LogFormat string
	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration
}

// Load reads configuration from PGDOCK_* environment variables, applying
// defaults for anything unset.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	cfg := Config{
		ListenAddr:      "127.0.0.1:8080",
		LogLevel:        slog.LevelInfo,
		LogFormat:       "json",
		ShutdownTimeout: 15 * time.Second,
	}

	if v := getenv("PGDOCK_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := getenv("PGDOCK_LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("PGDOCK_LOG_LEVEL: %w", err)
		}
	}
	if v := getenv("PGDOCK_LOG_FORMAT"); v != "" {
		v = strings.ToLower(v)
		if v != "json" && v != "text" {
			return Config{}, fmt.Errorf("PGDOCK_LOG_FORMAT: must be json or text, got %q", v)
		}
		cfg.LogFormat = v
	}
	if v := getenv("PGDOCK_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("PGDOCK_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}
	return cfg, nil
}
