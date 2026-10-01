package config

import (
	"log/slog"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" || cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "json" || cfg.ShutdownTimeout != 15*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"PGDOCK_LISTEN_ADDR":      ":9000",
		"PGDOCK_LOG_LEVEL":        "debug",
		"PGDOCK_LOG_FORMAT":       "TEXT",
		"PGDOCK_SHUTDOWN_TIMEOUT": "3s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9000" || cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "text" || cfg.ShutdownTimeout != 3*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadInvalid(t *testing.T) {
	for _, m := range []map[string]string{
		{"PGDOCK_LOG_LEVEL": "loud"},
		{"PGDOCK_LOG_FORMAT": "xml"},
		{"PGDOCK_SHUTDOWN_TIMEOUT": "soon"},
	} {
		if _, err := load(env(m)); err == nil {
			t.Errorf("expected error for %v", m)
		}
	}
}
