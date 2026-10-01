package pooler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/store"
)

// syncLockKey serializes pooler syncs across pgdock-server processes.
const syncLockKey int64 = 0x7067646f636b01 // "pgdock\x01"

// Manager keeps the poolers' generated files in step with the metadata DB.
type Manager struct {
	dir    string
	mode   os.FileMode
	db     *pgxpool.Pool
	admins []*Admin
	static []User // e.g. the admin console user
	log    *slog.Logger

	mu sync.Mutex
}

// NewManager returns a Manager writing files with mode into dir and
// reloading admins. staticUsers are always present in the auth file (at
// least the admin console user, or pgdock-server could not reach the
// poolers).
func NewManager(dir string, mode os.FileMode, db *pgxpool.Pool, admins []*Admin, staticUsers []User, log *slog.Logger) (*Manager, error) {
	if dir == "" {
		return nil, errors.New("pooler: config directory is required")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("pooler: %w", err)
	}
	return &Manager{dir: dir, mode: mode, db: db, admins: admins, static: staticUsers, log: log}, nil
}

// Admins returns the pooler admin consoles.
func (m *Manager) Admins() []*Admin { return m.admins }

// Sync renders routes and the auth file from the metadata DB, writes them
// atomically, and RELOADs every pooler. It always reloads, so a pooler that
// missed an earlier reload catches up.
func (m *Manager) Sync(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	conn, err := m.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("pooler sync: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", syncLockKey); err != nil {
		return fmt.Errorf("pooler sync lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", syncLockKey)
	}()

	rows, err := store.New(conn).PoolerRoutes(ctx)
	if err != nil {
		return fmt.Errorf("pooler sync: load routes: %w", err)
	}
	cfg := Config{Users: append([]User(nil), m.static...)}
	for _, r := range rows {
		s, err := store.DecodeProjectSettings(r.Settings)
		if err != nil {
			return fmt.Errorf("pooler sync: %s: %w", r.DbName, err)
		}
		cfg.Routes = append(cfg.Routes, Route{
			Database:         r.DbName,
			Host:             r.Host,
			Port:             int(r.Port),
			PoolSize:         s.PoolSize,
			MaxDBConnections: s.ConnectionLimit,
		})
		cfg.Users = append(cfg.Users, User{Name: r.OwnerRole, Secret: r.ScramVerifier})
	}

	databases, userlist, err := Render(cfg)
	if err != nil {
		return err
	}
	// Users first: a new route is only useful once its user can log in.
	usersChanged, err := WriteFileAtomic(filepath.Join(m.dir, UserlistFile), userlist, m.mode)
	if err != nil {
		return fmt.Errorf("pooler sync: %w", err)
	}
	routesChanged, err := WriteFileAtomic(filepath.Join(m.dir, DatabasesFile), databases, m.mode)
	if err != nil {
		return fmt.Errorf("pooler sync: %w", err)
	}
	m.log.Debug("pooler config rendered", "routes", len(cfg.Routes), "users_changed", usersChanged, "routes_changed", routesChanged)

	return m.each(ctx, func(a *Admin) error { return a.Reload(ctx) })
}

// Dir is the pooler config directory.
func (m *Manager) Dir() string { return m.dir }

// FileMode is the mode generated files are written with.
func (m *Manager) FileMode() os.FileMode { return m.mode }

// Reload makes every pooler re-read its configuration, auth file, and TLS
// certificate without re-rendering anything.
func (m *Manager) Reload(ctx context.Context) error {
	return m.each(ctx, func(a *Admin) error { return a.Reload(ctx) })
}

// Kill drops all connections to db on every pooler.
func (m *Manager) Kill(ctx context.Context, db string) error {
	return m.each(ctx, func(a *Admin) error { return a.Kill(ctx, db) })
}

// Reconnect recycles db's server connections on every pooler.
func (m *Manager) Reconnect(ctx context.Context, db string) error {
	return m.each(ctx, func(a *Admin) error { return a.Reconnect(ctx, db) })
}

// Pause pauses db on every pooler.
func (m *Manager) Pause(ctx context.Context, db string) error {
	return m.each(ctx, func(a *Admin) error { return a.Pause(ctx, db) })
}

// Resume resumes db on every pooler.
func (m *Manager) Resume(ctx context.Context, db string) error {
	return m.each(ctx, func(a *Admin) error { return a.Resume(ctx, db) })
}

func (m *Manager) each(ctx context.Context, f func(*Admin) error) error {
	var errs []error
	for _, a := range m.admins {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f(a); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
