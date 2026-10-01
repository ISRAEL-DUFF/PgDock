// Package config loads pgdock-server configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/crypto"
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

	// DatabaseURL is the metadata DB connection string.
	DatabaseURL string
	// MasterKey encrypts secrets at rest (spec §7.3). PreviousMasterKeys
	// stay readable during a rotation.
	MasterKey          []byte
	PreviousMasterKeys [][]byte

	// Workers is the number of operations run concurrently.
	Workers int
	// DevEndpoints enables /api/v1/dev/*. Never enable in production.
	DevEndpoints bool

	// Shared is the shared cluster registered at startup (optional).
	Shared SharedCluster
	// Pooler configures the edge poolers; provisioning needs it.
	Pooler Pooler
	// Public is the connection info handed to clients.
	Public Public
}

// Load reads configuration from PGDOCK_* environment variables, applying
// defaults for anything unset.
func Load() (Config, error) {
	return load(os.Getenv, os.ReadFile)
}

func load(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	cfg := Config{
		ListenAddr:      "127.0.0.1:8080",
		LogLevel:        slog.LevelInfo,
		LogFormat:       "json",
		ShutdownTimeout: 15 * time.Second,
		Workers:         4,
	}
	var errs []error

	if v := getenv("PGDOCK_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := getenv("PGDOCK_LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_LOG_LEVEL: %w", err))
		}
	}
	if v := getenv("PGDOCK_LOG_FORMAT"); v != "" {
		v = strings.ToLower(v)
		if v != "json" && v != "text" {
			errs = append(errs, fmt.Errorf("PGDOCK_LOG_FORMAT: must be json or text, got %q", v))
		}
		cfg.LogFormat = v
	}
	if v := getenv("PGDOCK_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_SHUTDOWN_TIMEOUT: %w", err))
		}
		cfg.ShutdownTimeout = d
	}

	cfg.DatabaseURL = getenv("PGDOCK_DATABASE_URL")
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("PGDOCK_DATABASE_URL is required"))
	}

	key, err := loadMasterKey(getenv, readFile)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.MasterKey = key
	if v := getenv("PGDOCK_MASTER_KEY_PREVIOUS"); v != "" {
		for i, s := range strings.Split(v, ",") {
			k, err := crypto.ParseKey(s)
			if err != nil {
				errs = append(errs, fmt.Errorf("PGDOCK_MASTER_KEY_PREVIOUS[%d]: %w", i, err))
				continue
			}
			cfg.PreviousMasterKeys = append(cfg.PreviousMasterKeys, k)
		}
	}

	if v := getenv("PGDOCK_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 256 {
			errs = append(errs, fmt.Errorf("PGDOCK_WORKERS: must be an integer from 1 to 256, got %q", v))
		}
		cfg.Workers = n
	}
	if v := getenv("PGDOCK_DEV_ENDPOINTS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_DEV_ENDPOINTS: %w", err))
		}
		cfg.DevEndpoints = b
	}

	errs = append(errs, loadProvisioning(getenv, readFile, &cfg)...)

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadMasterKey reads PGDOCK_MASTER_KEY, or the file named by
// PGDOCK_MASTER_KEY_FILE (for Docker/systemd secrets). Exactly one is required.
func loadMasterKey(getenv func(string) string, readFile func(string) ([]byte, error)) ([]byte, error) {
	inline, file := getenv("PGDOCK_MASTER_KEY"), getenv("PGDOCK_MASTER_KEY_FILE")
	switch {
	case inline != "" && file != "":
		return nil, errors.New("set only one of PGDOCK_MASTER_KEY and PGDOCK_MASTER_KEY_FILE")
	case inline != "":
		k, err := crypto.ParseKey(inline)
		if err != nil {
			return nil, fmt.Errorf("PGDOCK_MASTER_KEY: %w", err)
		}
		return k, nil
	case file != "":
		b, err := readFile(file)
		if err != nil {
			return nil, fmt.Errorf("PGDOCK_MASTER_KEY_FILE: %w", err)
		}
		k, err := crypto.ParseKey(string(b))
		if err != nil {
			return nil, fmt.Errorf("PGDOCK_MASTER_KEY_FILE: %w", err)
		}
		return k, nil
	default:
		return nil, errors.New("PGDOCK_MASTER_KEY or PGDOCK_MASTER_KEY_FILE is required (generate one with `pgdock-server -gen-master-key`)")
	}
}
