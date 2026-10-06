package insights

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Bloat is a table's estimated wasted space (V3 §8, with the reclaim-space
// action of V2 §10.4).
type Bloat struct {
	Schema, Table  string
	Bytes          int64 // the table's main fork
	ExpectedBytes  int64
	BloatBytes     int64
	BloatRatio     float64
	LiveRows       int64
	DeadRows       int64
	LastVacuum     *time.Time
	LastAutovacuum *time.Time
}

const pageSize = 8192

// minBloatTable leaves out tables too small for bloat to matter.
const minBloatTable = 1 << 20

// Bloat estimates each table's bloat from its row count and average row
// width (pg_stats): the pages the rows need at the table's fillfactor,
// against the pages it has. Tables never analysed are left out.
func (s *Service) Bloat(ctx context.Context, p store.Project) ([]Bloat, error) {
	if ok, err := s.Available(ctx, p); err != nil || !ok {
		return nil, errors.Join(err, notAvailable(ok))
	}
	out := []Bloat{}
	err := s.console.ReadOnly(ctx, p.ID, func(conn *pgx.Conn) error {
		rows, err := conn.Query(ctx, `
			SELECT n.nspname, c.relname, pg_relation_size(c.oid), c.reltuples,
			  COALESCE((SELECT option_value::int FROM pg_options_to_table(c.reloptions) WHERE option_name = 'fillfactor'), 100),
			  (SELECT sum(st.avg_width) FROM pg_stats st WHERE st.schemaname = n.nspname AND st.tablename = c.relname),
			  (SELECT count(*) FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped),
			  COALESCE(s.n_live_tup, 0), COALESCE(s.n_dead_tup, 0), s.last_vacuum, s.last_autovacuum
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
			WHERE c.relkind IN ('r', 'm') AND `+userSchemas+` AND pg_relation_size(c.oid) >= $1`, minBloatTable)
		if err != nil {
			return err
		}
		return forEach(rows, func(r pgx.Rows) error {
			var b Bloat
			var tuples float64
			var fill int
			var width *int64
			var ncols int64
			if err := r.Scan(&b.Schema, &b.Table, &b.Bytes, &tuples, &fill, &width, &ncols, &b.LiveRows, &b.DeadRows, &b.LastVacuum, &b.LastAutovacuum); err != nil {
				return err
			}
			if width == nil || tuples < 0 {
				return nil // not analysed yet
			}
			b.ExpectedBytes = expectedBytes(tuples, *width, ncols, fill)
			if b.Bytes > b.ExpectedBytes {
				b.BloatBytes = b.Bytes - b.ExpectedBytes
				b.BloatRatio = float64(b.BloatBytes) / float64(b.Bytes)
			}
			out = append(out, b)
			return nil
		})
	})
	sort.Slice(out, func(i, j int) bool { return out[i].BloatBytes > out[j].BloatBytes })
	return out, err
}

// expectedBytes is the size rows need: a 24-byte page header, a 4-byte
// line pointer and a 24-byte tuple header (with a null bitmap for wide
// rows) per row, data aligned to 8 bytes, pages filled to fillfactor.
func expectedBytes(tuples float64, width, ncols int64, fill int) int64 {
	if tuples <= 0 {
		return pageSize
	}
	header := 23 + (ncols+7)/8
	tuple := align(header) + align(width)
	usable := float64(pageSize-24) * float64(fill) / 100
	perPage := math.Floor(usable / float64(tuple+4))
	if perPage < 1 {
		perPage = 1
	}
	return int64(math.Ceil(tuples/perPage)) * pageSize
}

func align(n int64) int64 { return (n + 7) &^ 7 }

// Session is a backend in a blocking chain.
type Session struct {
	PID       int32
	Role      string
	State     string
	WaitEvent string
	Query     string
	Running   time.Duration
	InXact    time.Duration
	// Platform: PGDock's own session (backups, the console, moves).
	Platform bool
}

// Block is a blocked session, the lock it waits for, and who holds it.
type Block struct {
	Blocked  Session
	Lock     string // the mode and object, e.g. "AccessExclusiveLock on public.orders"
	Blockers []Session
}

// Locks lists p's current blocking chains.
func (s *Service) Locks(ctx context.Context, p store.Project) ([]Block, error) {
	if ok, err := s.Available(ctx, p); err != nil || !ok {
		return nil, errors.Join(err, notAvailable(ok))
	}
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, `
		SELECT a.pid, COALESCE(a.usename, ''), COALESCE(a.state, ''), COALESCE(a.wait_event_type || ': ' || a.wait_event, ''),
		  left(COALESCE(a.query, ''), 2000), COALESCE(EXTRACT(EPOCH FROM now() - a.query_start), 0), COALESCE(EXTRACT(EPOCH FROM now() - a.xact_start), 0),
		  a.usename = current_user OR a.application_name LIKE 'pgdock%', pg_blocking_pids(a.pid),
		  COALESCE((SELECT l.mode || ' on ' || COALESCE(l.relation::regclass::text, l.locktype) FROM pg_locks l WHERE l.pid = a.pid AND NOT l.granted LIMIT 1), '')
		FROM pg_stat_activity a
		WHERE a.datname = current_database() AND a.backend_type = 'client backend' AND a.pid <> pg_backend_pid()`)
	if err != nil {
		return nil, err
	}
	type row struct {
		s        Session
		blockers []int32
		lock     string
	}
	byPID := map[int32]row{}
	var order []int32
	if err := forEach(rows, func(r pgx.Rows) error {
		var x row
		var run, xact float64
		if err := r.Scan(&x.s.PID, &x.s.Role, &x.s.State, &x.s.WaitEvent, &x.s.Query, &run, &xact, &x.s.Platform, &x.blockers, &x.lock); err != nil {
			return err
		}
		x.s.Running, x.s.InXact = time.Duration(run*float64(time.Second)), time.Duration(xact*float64(time.Second))
		byPID[x.s.PID] = x
		order = append(order, x.s.PID)
		return nil
	}); err != nil {
		return nil, err
	}
	out := []Block{}
	for _, pid := range order {
		x := byPID[pid]
		if len(x.blockers) == 0 {
			continue
		}
		b := Block{Blocked: x.s, Lock: x.lock, Blockers: []Session{}}
		for _, bp := range x.blockers {
			if y, ok := byPID[bp]; ok {
				b.Blockers = append(b.Blockers, y.s)
			} else {
				b.Blockers = append(b.Blockers, Session{PID: bp, Role: "(another database)"})
			}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Blocked.Running > out[j].Blocked.Running })
	return out, nil
}
