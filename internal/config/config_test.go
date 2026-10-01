package config

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/crypto"
)

var (
	key1 = bytes.Repeat([]byte{1}, crypto.KeySize)
	key2 = bytes.Repeat([]byte{2}, crypto.KeySize)
)

func env(m map[string]string) func(string) string {
	base := map[string]string{
		"PGDOCK_DATABASE_URL": "postgres://localhost/pgdock",
		"PGDOCK_MASTER_KEY":   crypto.EncodeKey(key1),
	}
	for k, v := range m {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func noFiles(string) ([]byte, error) { return nil, errors.New("no such file") }

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(nil), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" || cfg.LogLevel != slog.LevelInfo || cfg.LogFormat != "json" ||
		cfg.ShutdownTimeout != 15*time.Second || cfg.Workers != 4 || cfg.DevEndpoints {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !bytes.Equal(cfg.MasterKey, key1) {
		t.Fatal("master key not loaded")
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"PGDOCK_LISTEN_ADDR":         ":9000",
		"PGDOCK_LOG_LEVEL":           "debug",
		"PGDOCK_LOG_FORMAT":          "TEXT",
		"PGDOCK_SHUTDOWN_TIMEOUT":    "3s",
		"PGDOCK_WORKERS":             "8",
		"PGDOCK_DEV_ENDPOINTS":       "true",
		"PGDOCK_MASTER_KEY_PREVIOUS": crypto.EncodeKey(key2),
	}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":9000" || cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "text" ||
		cfg.ShutdownTimeout != 3*time.Second || cfg.Workers != 8 || !cfg.DevEndpoints {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if len(cfg.PreviousMasterKeys) != 1 || !bytes.Equal(cfg.PreviousMasterKeys[0], key2) {
		t.Fatal("previous master key not loaded")
	}
}

func TestMasterKeyFile(t *testing.T) {
	files := func(name string) ([]byte, error) {
		if name == "/run/secrets/key" {
			return []byte(crypto.EncodeKey(key2) + "\n"), nil
		}
		return nil, errors.New("no such file")
	}
	cfg, err := load(env(map[string]string{"PGDOCK_MASTER_KEY": "", "PGDOCK_MASTER_KEY_FILE": "/run/secrets/key"}), files)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cfg.MasterKey, key2) {
		t.Fatal("master key file not loaded")
	}
	if _, err := load(env(map[string]string{"PGDOCK_MASTER_KEY_FILE": "/run/secrets/key"}), files); err == nil {
		t.Fatal("both key sources accepted")
	}
}

func TestLoadInvalid(t *testing.T) {
	for _, m := range []map[string]string{
		{"PGDOCK_LOG_LEVEL": "loud"},
		{"PGDOCK_LOG_FORMAT": "xml"},
		{"PGDOCK_SHUTDOWN_TIMEOUT": "soon"},
		{"PGDOCK_WORKERS": "0"},
		{"PGDOCK_DEV_ENDPOINTS": "maybe"},
		{"PGDOCK_DATABASE_URL": ""},
		{"PGDOCK_MASTER_KEY": ""},
		{"PGDOCK_MASTER_KEY": "c2hvcnQ="},
		{"PGDOCK_MASTER_KEY_PREVIOUS": "nope"},
	} {
		if _, err := load(env(m), noFiles); err == nil {
			t.Errorf("expected error for %v", m)
		}
	}
}

func TestErrorsAreCombined(t *testing.T) {
	_, err := load(func(string) string { return "" }, noFiles)
	if err == nil || !strings.Contains(err.Error(), "PGDOCK_DATABASE_URL") || !strings.Contains(err.Error(), "PGDOCK_MASTER_KEY") {
		t.Fatalf("got %v", err)
	}
}

func TestProvisioningDefaults(t *testing.T) {
	cfg, err := load(env(nil), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pooler.ConfigDir != "" || cfg.Shared.AdminURL != "" {
		t.Fatalf("provisioning should be off by default: %+v %+v", cfg.Pooler, cfg.Shared)
	}
	if cfg.Public != (Public{Host: "localhost", SessionPort: 5432, PooledPort: 6543, SSLMode: "require"}) {
		t.Fatalf("public: %+v", cfg.Public)
	}
}

func TestProvisioningConfig(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"PGDOCK_DB_HOST":               "db.example.com",
		"PGDOCK_DB_SESSION_PORT":       "6432",
		"PGDOCK_DB_SSLMODE":            "disable",
		"PGDOCK_POOLER_CONFIG_DIR":     "/var/lib/pgdock/pooler",
		"PGDOCK_POOLER_FILE_MODE":      "644",
		"PGDOCK_POOLER_ADMIN_PASSWORD": "pw",
		"PGDOCK_POOLER_POOLED_ADDR":    "10.0.0.2:6543",
		"PGDOCK_SHARED_ADMIN_URL":      "postgres://pgdock_admin:x@10.0.0.3/postgres",
		"PGDOCK_SHARED_POOLER_PORT":    "5433",
	}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Pooler
	if p.FileMode != 0o644 || p.AdminUser != "pgdock" || p.AdminPassword != "pw" ||
		p.SessionAddr != "db.example.com:6432" || p.PooledAddr != "10.0.0.2:6543" || p.SSLMode != "prefer" {
		t.Fatalf("pooler: %+v", p)
	}
	if cfg.Shared.NodeName != "local" || cfg.Shared.PoolerPort != 5433 {
		t.Fatalf("shared: %+v", cfg.Shared)
	}
}

func TestProvisioningInvalid(t *testing.T) {
	for _, m := range []map[string]string{
		{"PGDOCK_POOLER_CONFIG_DIR": "/x"}, // no admin password
		{"PGDOCK_POOLER_CONFIG_DIR": "/x", "PGDOCK_POOLER_ADMIN_PASSWORD": "p", "PGDOCK_POOLER_FILE_MODE": "9"},
		{"PGDOCK_POOLER_CONFIG_DIR": "/x", "PGDOCK_POOLER_ADMIN_PASSWORD": "p", "PGDOCK_POOLER_SESSION_ADDR": "nohost"},
		{"PGDOCK_DB_SSLMODE": "sometimes"},
		{"PGDOCK_DB_POOLED_PORT": "99999"},
	} {
		if _, err := load(env(m), noFiles); err == nil {
			t.Errorf("expected error for %v", m)
		}
	}
}
