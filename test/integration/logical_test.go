package integration

import (
	"context"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/logical"
)

// logicalPair is a database on the dev shared cluster (the source) and the
// same database on the small test cluster (the target), which reaches the
// source at its Compose name.
type logicalPair struct {
	name               string
	srcAdmin, dstAdmin *pgx.Conn // postgres databases
	src, dst           *pgx.Conn // the moved database
	srcURL             string
	endpoint           logical.Endpoint
}

// dbURL is u with its database replaced (keeping sslmode and the rest).
func dbURL(t *testing.T, u, db string) string {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + db
	return parsed.String()
}

func newLogicalPair(t *testing.T, ddl string) *logicalPair {
	t.Helper()
	srcURL, dstURL := os.Getenv("PGDOCK_TEST_SHARED_ADMIN_URL"), os.Getenv("PGDOCK_TEST_SMALL_ADMIN_URL")
	if srcURL == "" || dstURL == "" {
		t.Skip("PGDOCK_TEST_SHARED_ADMIN_URL and PGDOCK_TEST_SMALL_ADMIN_URL are not set")
	}
	ctx := context.Background()
	p := &logicalPair{name: "lm_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""), srcURL: srcURL}
	port, _ := strconv.Atoi(os.Getenv("PGDOCK_TEST_SHARED_POOLER_PORT"))
	p.endpoint = logical.Endpoint{Host: os.Getenv("PGDOCK_TEST_SHARED_POOLER_HOST"), Port: port, SSLMode: "disable"}
	connect := func(u, db string) *pgx.Conn {
		c, err := pgx.Connect(ctx, dbURL(t, u, db))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close(context.Background()) })
		return c
	}
	p.srcAdmin, p.dstAdmin = connect(srcURL, "postgres"), connect(dstURL, "postgres")
	for _, c := range []*pgx.Conn{p.srcAdmin, p.dstAdmin} {
		if _, err := c.Exec(ctx, "CREATE DATABASE "+p.name); err != nil {
			t.Fatal(err)
		}
	}
	p.src, p.dst = connect(srcURL, p.name), connect(dstURL, p.name)
	// Cleanups run last first: replication objects, then the databases,
	// then the connections.
	t.Cleanup(func() {
		for _, c := range []*pgx.Conn{p.srcAdmin, p.dstAdmin} {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+p.name+" WITH (FORCE)")
		}
	})
	t.Cleanup(func() {
		m := logical.Move{Name: logical.Name(p.name), Database: p.name}
		_ = logical.Cleanup(context.Background(), m, p.src, p.srcAdmin, p.dst)
	})
	// The schema on both sides, as the move's pg_dump --schema-only would.
	for _, c := range []*pgx.Conn{p.src, p.dst} {
		if _, err := c.Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

const logicalSchema = `
CREATE TABLE orders (id bigserial PRIMARY KEY, n bigint NOT NULL UNIQUE, note text);
CREATE TABLE events (kind text, payload text);  -- no primary key
CREATE TABLE secrets (id int PRIMARY KEY, v text);
ALTER TABLE secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE secrets FORCE ROW LEVEL SECURITY;    -- no policy: nobody but a BYPASSRLS role sees rows
CREATE SEQUENCE tickets START 100;
`

// TestLogicalEngine covers internal/logical against two real servers: the
// preflight, the initial copy, streaming under continuous writes, the DDL
// block, the cutover sync, sequences, sampled counts, and cleanup.
func TestLogicalEngine(t *testing.T) {
	p := newLogicalPair(t, logicalSchema)
	ctx := context.Background()
	if _, err := p.src.Exec(ctx, `
		INSERT INTO orders (n, note) SELECT g, 'seed' FROM generate_series(1, 5000) g;
		INSERT INTO events SELECT 'seed', md5(g::text) FROM generate_series(1, 300) g;
		INSERT INTO secrets SELECT g, 'hidden' FROM generate_series(1, 50) g;
		SELECT nextval('tickets') FROM generate_series(1, 7);
		SELECT setval('orders_id_seq', (SELECT max(id) FROM orders));`); err != nil {
		t.Fatal(err)
	}

	rep, err := logical.Preflight(ctx, p.src, p.dstAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || !slices.Equal(rep.NoIdentity, []string{"public.events"}) {
		t.Fatalf("preflight: %+v", rep)
	}

	m := logical.Move{Name: logical.Name(p.name), Database: p.name, SameMajor: true}
	if err := logical.Setup(ctx, m, p.src, p.srcAdmin, p.dst, p.endpoint, rep.NoIdentity); err != nil {
		t.Fatal(err)
	}

	// A tenant's DDL is refused while the move runs; data changes aren't.
	if _, err := p.srcAdmin.Exec(ctx, "CREATE ROLE "+p.name+"_tenant LOGIN PASSWORD 'tenant-pw'; GRANT ALL ON SCHEMA public TO "+p.name+"_tenant"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = p.srcAdmin.Exec(context.Background(), "DROP OWNED BY "+p.name+"_tenant; DROP ROLE IF EXISTS "+p.name+"_tenant")
	})
	tenantURL := strings.Replace(dbURL(t, p.srcURL, p.name), "pgdock_admin:pgdock_admin", p.name+"_tenant:tenant-pw", 1)
	tenant, err := pgx.Connect(ctx, tenantURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.Exec(ctx, "CREATE TABLE sneaky (x int)"); err == nil || !strings.Contains(err.Error(), "being moved") {
		t.Fatalf("DDL during a move: %v", err)
	}
	_ = tenant.Close(ctx)

	// A writer commits numbered rows the whole time; every acknowledged n
	// must be on the target.
	var acked atomic.Int64
	var writerErr atomic.Value
	stop := make(chan struct{})
	var wg sync.WaitGroup
	writer, err := pgx.Connect(ctx, dbURL(t, p.srcURL, p.name))
	if err != nil {
		t.Fatal(err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer writer.Close(context.Background())
		for n := int64(100001); ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			err := pgx.BeginFunc(ctx, writer, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO orders (n, note) VALUES ($1, 'live')`, n); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE events SET payload = $1 WHERE ctid = (SELECT ctid FROM events LIMIT 1)`, strconv.FormatInt(n, 10)); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `SELECT nextval('tickets')`)
				return err
			})
			if err != nil {
				writerErr.Store(err)
				return
			}
			acked.Store(n)
		}
	}()

	var reads int
	if err := logical.WaitCaughtUp(ctx, m, p.src, p.dst, logical.WaitOptions{StableFor: 2 * time.Second, Poll: 200 * time.Millisecond,
		Report: func(logical.Status) error { reads++; return nil }}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // keep streaming for a moment after the copy
	close(stop)             // the freeze
	wg.Wait()

	res, err := logical.Cutover(ctx, m, p.src, p.dst, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("caught up after %d readings; %d acknowledged live rows; cutover sync %s, %d sequences, %d tables sampled",
		reads, acked.Load()-100000, res.SyncWait, res.Sequences, len(res.Verified))
	if e := writerErr.Load(); e != nil {
		t.Fatalf("writer: %v", e)
	}
	if acked.Load() <= 100001 {
		t.Fatal("the writer didn't commit anything while the move streamed")
	}

	// Every row, every table, identical (RLS didn't hide any from the copy).
	for _, q := range []string{
		`SELECT count(*), coalesce(sum(n), 0), coalesce(max(n), 0) FROM orders`,
		`SELECT count(*), coalesce(sum(length(payload)), 0), 0 FROM events`,
		`SELECT count(*), coalesce(sum(id), 0), 0 FROM secrets`,
	} {
		var a, b [3]int64
		if err := p.src.QueryRow(ctx, q).Scan(&a[0], &a[1], &a[2]); err != nil {
			t.Fatal(err)
		}
		if err := p.dst.QueryRow(ctx, q).Scan(&b[0], &b[1], &b[2]); err != nil {
			t.Fatal(err)
		}
		if a != b {
			t.Fatalf("%s: source %v, target %v", q, a, b)
		}
	}
	var maxN int64
	if err := p.dst.QueryRow(ctx, `SELECT max(n) FROM orders`).Scan(&maxN); err != nil || maxN < acked.Load() {
		t.Fatalf("acknowledged commit %d missing on the target (max %d, %v)", acked.Load(), maxN, err)
	}
	// Sequences: ahead of the source by the margin; new ids don't collide.
	var srcTicket, dstTicket int64
	_ = p.src.QueryRow(ctx, `SELECT last_value FROM tickets`).Scan(&srcTicket)
	_ = p.dst.QueryRow(ctx, `SELECT last_value FROM tickets`).Scan(&dstTicket)
	if dstTicket != srcTicket+logical.Margin {
		t.Fatalf("tickets: source %d, target %d", srcTicket, dstTicket)
	}
	if _, err := p.dst.Exec(ctx, `INSERT INTO orders (n, note) VALUES (-1, 'after cutover')`); err != nil {
		t.Fatalf("insert on the target after cutover: %v", err)
	}

	if err := logical.Cleanup(ctx, m, p.src, p.srcAdmin, p.dst); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := p.src.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1)
		+ (SELECT count(*) FROM pg_publication WHERE pubname = $1) + (SELECT count(*) FROM pg_roles WHERE rolname = $1)
		+ (SELECT count(*) FROM pg_event_trigger WHERE evtname LIKE 'pgdock_move%') + (SELECT count(*) FROM pg_namespace WHERE nspname = 'pgdock_move')`,
		m.Name).Scan(&left); err != nil || left != 0 {
		t.Fatalf("left on the source after cleanup: %d %v", left, err)
	}
	if err := p.dst.QueryRow(ctx, `SELECT count(*) FROM pg_subscription WHERE subname = $1`, m.Name).Scan(&left); err != nil || left != 0 {
		t.Fatalf("subscription left: %d %v", left, err)
	}
	if err := logical.Cleanup(ctx, m, p.src, p.srcAdmin, p.dst); err != nil {
		t.Fatalf("cleanup twice: %v", err)
	}
}

// TestLogicalPreflightBlocks: large objects, unlogged tables and missing
// extensions each stop a logical move, with a reason.
func TestLogicalPreflightBlocks(t *testing.T) {
	p := newLogicalPair(t, `CREATE UNLOGGED TABLE scratch (x int); CREATE EXTENSION IF NOT EXISTS pg_stat_statements;`)
	ctx := context.Background()
	if _, err := p.src.Exec(ctx, `SELECT lo_create(0)`); err != nil {
		t.Fatal(err)
	}
	rep, err := logical.Preflight(ctx, p.src, p.dstAdmin)
	if err != nil {
		t.Fatal(err)
	}
	var checks []string
	for _, b := range rep.Blockers {
		checks = append(checks, b.Check)
	}
	for _, want := range []string{"large_objects", "unlogged_tables"} {
		if !slices.Contains(checks, want) {
			t.Errorf("no %s blocker: %+v", want, rep.Blockers)
		}
	}
	if rep.OK() || !strings.Contains(rep.Reason(), "large objects") {
		t.Fatalf("reason: %q", rep.Reason())
	}
}
