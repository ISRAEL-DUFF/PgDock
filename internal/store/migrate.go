package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies all pending migrations. A Postgres advisory lock
// serializes concurrent callers, so several servers can start at once.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	return MigrateTo(ctx, pool, -1, log)
}

// MigrateTo migrates up or down to version (-1: the latest). Tests use it
// to load data in an older schema and upgrade it.
func MigrateTo(ctx context.Context, pool *pgxpool.Pool, version int64, log *slog.Logger) error {
	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	p, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	var results []*goose.MigrationResult
	if version < 0 {
		results, err = p.Up(ctx)
	} else {
		cur, verr := p.GetDBVersion(ctx)
		if verr != nil {
			return fmt.Errorf("migrations: %w", verr)
		}
		if version >= cur {
			results, err = p.UpTo(ctx, version)
		} else {
			results, err = p.DownTo(ctx, version)
		}
	}
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	for _, r := range results {
		log.Info("applied migration", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}
	return nil
}
