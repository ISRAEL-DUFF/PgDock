package pooler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

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
		// A V1 project renamed to an opaque database keeps its old name as
		// an alias, so existing connection strings still work (V2 §10.2).
		if r.AliasDbName != nil {
			cfg.Routes = append(cfg.Routes, Route{
				Database: *r.AliasDbName, BackendDB: r.DbName, Host: r.Host, Port: int(r.Port),
				PoolSize: s.PoolSize, MaxDBConnections: s.ConnectionLimit,
			})
		}
		// The V1 owner role during a switch to opaque credentials.
		if r.LegacyOwnerRole != nil && r.LegacyScramVerifier != nil {
			cfg.Users = append(cfg.Users, User{Name: *r.LegacyOwnerRole, Secret: *r.LegacyScramVerifier})
		}
	}
	members, err := store.New(conn).PoolerDBUsers(ctx)
	if err != nil {
		return fmt.Errorf("pooler sync: load personal logins: %w", err)
	}
	for _, u := range members {
		cfg.Users = append(cfg.Users, User{Name: u.RoleName, Secret: u.ScramVerifier})
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

	if err := m.each(ctx, func(a *Admin) error { return a.Reload(ctx) }); err != nil {
		return &ReloadError{Err: err}
	}
	return nil
}

// ReloadError means the configuration files were written but at least one
// pooler did not reload them (it reads them when it next starts).
type ReloadError struct{ Err error }

func (e *ReloadError) Error() string { return e.Err.Error() }
func (e *ReloadError) Unwrap() error { return e.Err }

// Dir is the pooler config directory.
func (m *Manager) Dir() string { return m.dir }

// FileMode is the mode generated files are written with.
func (m *Manager) FileMode() os.FileMode { return m.mode }

// Reload makes every pooler re-read its configuration, auth file, and TLS
// certificate without re-rendering anything.
func (m *Manager) Reload(ctx context.Context) error {
	return m.each(ctx, func(a *Admin) error { return a.Reload(ctx) })
}

// Kill drops all connections to dbs on every pooler. dbs are a project's
// pooler names: its database and any V1 alias (store.PoolerNames).
func (m *Manager) Kill(ctx context.Context, dbs ...string) error {
	return m.eachDB(ctx, dbs, func(a *Admin, db string) error { return a.Kill(ctx, db) })
}

// Reconnect recycles dbs' server connections on every pooler.
func (m *Manager) Reconnect(ctx context.Context, dbs ...string) error {
	return m.eachDB(ctx, dbs, func(a *Admin, db string) error { return a.Reconnect(ctx, db) })
}

// Pause pauses dbs on every pooler.
func (m *Manager) Pause(ctx context.Context, dbs ...string) error {
	return m.eachDB(ctx, dbs, func(a *Admin, db string) error { return a.Pause(ctx, db) })
}

func (m *Manager) eachDB(ctx context.Context, dbs []string, f func(*Admin, string) error) error {
	var errs []error
	for _, db := range dbs {
		if err := m.each(ctx, func(a *Admin) error { return f(a, db) }); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SessionPooler names the session-mode pooler's admin.
const SessionPooler = "session"

// Freeze holds db's clients on every pooler before a cutover (spec §6.6
// step 3). On the transaction pooler PAUSE lets in-flight transactions
// finish and queues new work, falling back to KILL if that takes longer
// than wait. Session-mode clients hold their server connection until they
// disconnect, so PAUSE would wait for them: that pooler gets KILL, which
// drops them; their reconnects wait. Resume releases both. It returns the
// poolers that used KILL.
func (m *Manager) Freeze(ctx context.Context, wait time.Duration, dbs ...string) ([]string, error) {
	var killed []string
	var errs []error
	for _, db := range dbs {
		k, err := m.freeze(ctx, db, wait)
		killed = append(killed, k...)
		if err != nil {
			errs = append(errs, err)
		}
		if ctx.Err() != nil {
			return killed, ctx.Err()
		}
	}
	return killed, errors.Join(errs...)
}

func (m *Manager) freeze(ctx context.Context, db string, wait time.Duration) ([]string, error) {
	var killed []string
	var errs []error
	for _, a := range m.admins {
		if a.Name == SessionPooler {
			if err := a.Kill(ctx, db); err != nil {
				errs = append(errs, err)
				continue
			}
			killed = append(killed, a.Name)
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, wait)
		err := a.Pause(pctx, db)
		cancel()
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return killed, ctx.Err()
		}
		if kerr := a.Kill(ctx, db); kerr != nil {
			errs = append(errs, fmt.Errorf("pause: %w; kill: %w", err, kerr))
			continue
		}
		killed = append(killed, a.Name)
	}
	return killed, errors.Join(errs...)
}

// Resume resumes dbs on every pooler.
func (m *Manager) Resume(ctx context.Context, dbs ...string) error {
	return m.eachDB(ctx, dbs, func(a *Admin, db string) error { return a.Resume(ctx, db) })
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

// KillUser drops user's client connections on every pooler.
func (m *Manager) KillUser(ctx context.Context, user string) error {
	return m.each(ctx, func(a *Admin) error { _, err := a.KillUser(ctx, user); return err })
}
