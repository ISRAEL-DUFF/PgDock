// Package storetest provides throwaway, fully migrated metadata databases
// for tests.
//
// Tests that need Postgres are skipped unless PGDOCK_TEST_DATABASE_URL points
// at a server where that user may create databases, e.g.
//
//	PGDOCK_TEST_DATABASE_URL=postgres://postgres@127.0.0.1:5432/postgres
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/store"
)

// EnvVar names the admin connection string used to create test databases.
const EnvVar = "PGDOCK_TEST_DATABASE_URL"

// New creates a fresh database, migrates it, and returns a pool connected to
// it. The database is dropped when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := NewEmpty(t)
	if err := store.Migrate(context.Background(), pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// NewEmpty is New without migrations.
func NewEmpty(t testing.TB) *pgxpool.Pool {
	t.Helper()
	adminURL := os.Getenv(EnvVar)
	if adminURL == "" {
		t.Skipf("%s not set; skipping test that needs Postgres", EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect %s: %v", EnvVar, err)
	}
	defer admin.Close(context.Background())

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "pgdock_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	// As pgdock-server sizes its pool (cmd/server connect): the test
	// environment runs the same workers, two of which keep a connection.
	cfg.MaxConns = max(cfg.MaxConns, 16)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admin, err := pgx.Connect(ctx, adminURL)
		if err != nil {
			t.Errorf("drop test database: %v", err)
			return
		}
		defer admin.Close(context.Background())
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	return pool
}
