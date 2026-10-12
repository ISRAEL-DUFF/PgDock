package store

import (
	"context"
	"embed"
	"errors"
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

// ErrSchemaNewer means the database was migrated by a newer release than the
// one running: its schema holds changes this server doesn't know, and running
// on it can corrupt data (a column it doesn't set, a constraint it doesn't
// meet). Start the release that migrated it, or restore the metadata backup
// taken before that upgrade.
var ErrSchemaNewer = errors.New("the metadata database was migrated by a newer PGDock than this one")

// Migrate applies all pending migrations, after refusing a database whose
// schema is newer than this release. Rolling back is therefore not just
// checking out the old version. A Postgres advisory lock
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
		if err := refuseNewerSchema(ctx, p); err != nil {
			return err
		}
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

// refuseNewerSchema fails when the database's applied version is beyond the
// last migration this release carries.
func refuseNewerSchema(ctx context.Context, p *goose.Provider) error {
	have, err := p.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	var known int64
	for _, src := range p.ListSources() {
		known = max(known, src.Version)
	}
	if have > known {
		return fmt.Errorf("%w: the database is at schema version %d, this release's last migration is %d (restore the metadata backup taken before the upgrade, or run the newer release; see docs/upgrade.md, Rolling back)", ErrSchemaNewer, have, known)
	}
	return nil
}
