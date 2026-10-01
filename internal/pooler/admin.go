package pooler

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Admin talks to one PgBouncer's admin console (the virtual "pgbouncer"
// database), which only speaks the simple query protocol.
type Admin struct {
	Name string // for logs, e.g. "session" or "transaction"
	cfg  *pgx.ConnConfig
}

// NewAdmin returns a client for the admin console of the PgBouncer at addr
// (host:port), logging in as an admin_users entry.
func NewAdmin(name, addr, user, password, sslmode string) (*Admin, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("pooler %s address %q: %w", name, addr, err)
	}
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%s sslmode=%s", host, port, sslmode))
	if err != nil {
		return nil, fmt.Errorf("pooler %s: %w", name, err)
	}
	cfg.User, cfg.Password, cfg.Database = user, password, "pgbouncer"
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	// The admin console rejects unknown startup parameters.
	delete(cfg.RuntimeParams, "application_name")
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	return &Admin{Name: name, cfg: cfg}, nil
}

// Addr returns host:port of the pooler.
func (a *Admin) Addr() string { return fmt.Sprintf("%s:%d", a.cfg.Host, a.cfg.Port) }

// Reload makes PgBouncer re-read its configuration and auth files.
func (a *Admin) Reload(ctx context.Context) error { return a.exec(ctx, "RELOAD") }

// Pause waits for server connections to db to be released and holds new
// queries until Resume, so a backend can be switched without client errors.
func (a *Admin) Pause(ctx context.Context, db string) error { return a.dbCommand(ctx, "PAUSE", db) }

// Resume releases a database paused with Pause (or Kill).
func (a *Admin) Resume(ctx context.Context, db string) error { return a.dbCommand(ctx, "RESUME", db) }

// Reconnect closes db's server connections as they are released, so new
// ones pick up changed role settings (ALTER ROLE ... SET applies only to new
// backends).
func (a *Admin) Reconnect(ctx context.Context, db string) error {
	return a.dbCommand(ctx, "RECONNECT", db)
}

// Kill immediately drops all client and server connections to db. New
// clients wait until Resume or until the route is removed.
func (a *Admin) Kill(ctx context.Context, db string) error { return a.dbCommand(ctx, "KILL", db) }

var dbNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func (a *Admin) dbCommand(ctx context.Context, cmd, db string) error {
	if !dbNameRe.MatchString(db) {
		return fmt.Errorf("pooler: invalid database name %q", db)
	}
	return a.exec(ctx, cmd+" "+db)
}

func (a *Admin) exec(ctx context.Context, cmd string) error {
	conn, err := pgx.ConnectConfig(ctx, a.cfg)
	if err != nil {
		return fmt.Errorf("pooler %s (%s): connect admin console: %w", a.Name, a.Addr(), err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, cmd); err != nil {
		return fmt.Errorf("pooler %s (%s): %s: %w", a.Name, a.Addr(), cmd, err)
	}
	return nil
}

// Pool is one row of SHOW POOLS.
type Pool struct {
	Database  string
	User      string
	ClActive  int64
	ClWaiting int64
	SvActive  int64
	SvIdle    int64
}

// Pools runs SHOW POOLS.
func (a *Admin) Pools(ctx context.Context) ([]Pool, error) {
	conn, err := pgx.ConnectConfig(ctx, a.cfg)
	if err != nil {
		return nil, fmt.Errorf("pooler %s (%s): connect admin console: %w", a.Name, a.Addr(), err)
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, "SHOW POOLS")
	if err != nil {
		return nil, fmt.Errorf("pooler %s: SHOW POOLS: %w", a.Name, err)
	}
	defer rows.Close()
	// Columns vary between PgBouncer versions; read them by name.
	col := map[string]int{}
	for i, fd := range rows.FieldDescriptions() {
		col[fd.Name] = i
	}
	get := func(raw [][]byte, name string) string {
		if i, ok := col[name]; ok && i < len(raw) {
			return string(raw[i])
		}
		return ""
	}
	num := func(raw [][]byte, name string) int64 {
		n, _ := strconv.ParseInt(get(raw, name), 10, 64)
		return n
	}
	var out []Pool
	for rows.Next() {
		raw := rows.RawValues()
		out = append(out, Pool{
			Database: get(raw, "database"), User: get(raw, "user"),
			ClActive: num(raw, "cl_active"), ClWaiting: num(raw, "cl_waiting"),
			SvActive: num(raw, "sv_active"), SvIdle: num(raw, "sv_idle"),
		})
	}
	return out, rows.Err()
}
