package integration

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// liveWriter commits numbered rows through the pooler until stopped,
// retrying through the move's pause; Acked is the last n the server
// acknowledged.
type liveWriter struct {
	acked  atomic.Int64
	errs   atomic.Int64
	stop   chan struct{}
	done   sync.WaitGroup
	paused atomic.Int64 // longest gap between two acknowledged commits, ms
}

func startWriter(t *testing.T, e *testenv.Env, url string) *liveWriter {
	t.Helper()
	w := &liveWriter{stop: make(chan struct{})}
	ctx := context.Background()
	c := e.MustConnect(url)
	if _, err := c.Exec(ctx, `CREATE TABLE ledger (n bigint PRIMARY KEY, at timestamptz NOT NULL DEFAULT now(), pad text)`); err != nil {
		t.Fatal(err)
	}
	_ = c.Close(ctx)
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		var conn *pgx.Conn
		last := time.Now()
		for n := int64(1); ; {
			select {
			case <-w.stop:
				if conn != nil {
					_ = conn.Close(context.Background())
				}
				return
			default:
			}
			if conn == nil {
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				var err error
				conn, err = pgx.Connect(cctx, url)
				cancel()
				if err != nil {
					w.errs.Add(1)
					time.Sleep(50 * time.Millisecond)
					continue
				}
			}
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_, err := conn.Exec(cctx, `INSERT INTO ledger (n, pad) VALUES ($1, repeat('x', 100))`, n)
			cancel()
			if err != nil {
				// A commit that errored may or may not have happened; the next
				// try uses the same n, so a duplicate shows up as a conflict.
				if strings.Contains(err.Error(), "duplicate key") {
					w.acked.Store(n)
					n++
				} else {
					w.errs.Add(1)
				}
				_ = conn.Close(context.Background())
				conn = nil
				continue
			}
			if gap := time.Since(last).Milliseconds(); gap > w.paused.Load() {
				w.paused.Store(gap)
			}
			last = time.Now()
			w.acked.Store(n)
			n++
		}
	}()
	return w
}

func (w *liveWriter) Stop() { close(w.stop); w.done.Wait() }

// TestLogicalNodeMove is M18's node move (V3 §2.3): a shared project under
// continuous writes moves to the shared cluster on another node by logical
// replication. Writes pause for under 5 seconds, every acknowledged commit
// is on the new node, and the old copy is kept read-only.
func TestLogicalNodeMove(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	ctx := context.Background()

	c := e.CreateProject("Moving")
	conn := e.MustConnect(c.Connection.PooledUrl)
	if _, err := conn.Exec(ctx, `CREATE TABLE items (id serial PRIMARY KEY, v text);
		INSERT INTO items (v) SELECT md5(g::text) FROM generate_series(1, 20000) g;
		CREATE TABLE tags (item int, tag text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO tags SELECT g, 'seed' FROM generate_series(1, 500) g`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)

	// A second node with its own shared cluster.
	var created gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-b", PrivateAddr: "agent-test-2", Role: gen.CreateNodeRequestRoleShared}, &created); code != http.StatusCreated {
		t.Fatalf("create node: %d", code)
	}
	e.StartSecondAgent("node-b", created.Token)
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/nodes/"+created.Node.Id.String()+"/shared-cluster", gen.SharedClusterRequest{MemoryMb: 512}, &op); code != http.StatusAccepted {
		t.Fatalf("shared cluster: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("shared cluster: %s\n%s", op.Status, testenv.FormatLog(op))
	}

	w := startWriter(t, e, c.Connection.PooledUrl)
	time.Sleep(time.Second)
	if code := e.Do("POST", "/api/v1/admin/projects/"+c.Project.Id.String()+"/move", gen.MoveProjectRequest{NodeId: created.Node.Id}, &op); code != http.StatusAccepted {
		w.Stop()
		t.Fatalf("move: %d", code)
	}
	var p gen.Project
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
	if p.Status != gen.ProjectStatusMoving {
		t.Errorf("status during the move: %s", p.Status)
	}
	op = e.WaitOperation(op.Id)
	time.Sleep(time.Second) // a few commits on the new node
	w.Stop()
	t.Logf("move log:\n%s", testenv.FormatLog(op))
	if op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("move: %s %s", op.Status, deref(op.Error))
	}

	var moves gen.MoveList
	if code := e.Do("GET", "/api/v1/projects/"+c.Project.Id.String()+"/moves", nil, &moves); code != http.StatusOK || len(moves.Items) != 1 {
		t.Fatalf("moves: %d %+v", code, moves)
	}
	mv := moves.Items[0]
	if mv.Mode != gen.MoveModeLogical || mv.Phase != gen.MovePhaseDone || mv.FreezeMs == nil {
		t.Fatalf("move record: %+v", mv)
	}
	t.Logf("writes paused %d ms (longest gap seen by the client %d ms); %d commits acknowledged, %d client errors",
		*mv.FreezeMs, w.paused.Load(), w.acked.Load(), w.errs.Load())
	if *mv.FreezeMs >= 5000 {
		t.Errorf("writes paused for %d ms, want under 5 s", *mv.FreezeMs)
	}

	// On node-b now, with every acknowledged commit.
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
	if p.Status != gen.ProjectStatusActive || p.Instance == nil || p.Instance.NodeName != "node-b" {
		t.Fatalf("after the move: %s on %+v", p.Status, p.Instance)
	}
	conn = e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var count, maxN int64
	if err := conn.QueryRow(ctx, `SELECT count(*), coalesce(max(n), 0) FROM ledger`).Scan(&count, &maxN); err != nil {
		t.Fatal(err)
	}
	if maxN < w.acked.Load() || count != maxN {
		t.Fatalf("ledger on node-b: %d rows, max %d; acknowledged %d", count, maxN, w.acked.Load())
	}
	var items, tags int
	if err := conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM items), (SELECT count(*) FROM tags)`).Scan(&items, &tags); err != nil || items != 20000 || tags != 500 {
		t.Fatalf("copied tables: items %d tags %d %v", items, tags, err)
	}
	// Sequences continue past the copied values.
	if _, err := conn.Exec(ctx, `INSERT INTO items (v) VALUES ('after')`); err != nil {
		t.Fatalf("insert after the move: %v", err)
	}
	// Schema changes work again.
	if _, err := conn.Exec(ctx, `CREATE TABLE after_move (x int)`); err != nil {
		t.Fatalf("DDL after the move: %v", err)
	}

	// The old copy: read-only, no replication left behind.
	old := e.SharedAdmin(c.Project.DbName)
	defer old.Close(ctx)
	var ro string
	if err := old.QueryRow(ctx, `SELECT setting FROM pg_settings WHERE name = 'default_transaction_read_only'`).Scan(&ro); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := old.QueryRow(ctx, `SELECT (SELECT count(*) FROM pg_replication_slots WHERE database = current_database())
		+ (SELECT count(*) FROM pg_publication) + (SELECT count(*) FROM pg_event_trigger)`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("replication objects left on the old cluster: %d %v", left, err)
	}
	_ = ro
}

// moveAndWait queues a node move and waits for it.
func moveAndWait(t *testing.T, e *testenv.Env, project, node string) (gen.Operation, gen.Move) {
	t.Helper()
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/admin/projects/"+project+"/move", gen.MoveProjectRequest{NodeId: uuidOf(t, node)}, &op); code != http.StatusAccepted {
		t.Fatalf("move: %d", code)
	}
	op = e.WaitOperation(op.Id)
	if op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("move: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	var moves gen.MoveList
	e.Do("GET", "/api/v1/projects/"+project+"/moves", nil, &moves)
	if len(moves.Items) == 0 {
		t.Fatal("no move recorded")
	}
	return op, moves.Items[0]
}

func uuidOf(t *testing.T, s string) openapi_types.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestDedicatedNodeMove: a dedicated project moves to a new instance on
// another node by logical replication; the old instance is stopped and
// kept for 48 hours.
func TestDedicatedNodeMove(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "dedicated")
	ctx := context.Background()

	tier := gen.ProjectTierDedicated
	profile, vol := "small", 5
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Dedicated mover", Tier: &tier, Profile: &profile, VolumeGb: &vol}, &c); code != http.StatusAccepted {
		t.Fatalf("create dedicated: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var before gen.Project
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &before)

	var created gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-b", PrivateAddr: "agent-test-2", Role: gen.CreateNodeRequestRoleDedicated}, &created); code != http.StatusCreated {
		t.Fatalf("create node: %d", code)
	}
	e.StartSecondAgent("node-b", created.Token)

	w := startWriter(t, e, c.Connection.PooledUrl)
	time.Sleep(time.Second)
	op, mv := moveAndWait(t, e, c.Project.Id.String(), created.Node.Id.String())
	time.Sleep(500 * time.Millisecond)
	w.Stop()
	t.Logf("move log:\n%s", testenv.FormatLog(op))
	if mv.Mode != gen.MoveModeLogical || mv.FreezeMs == nil || *mv.FreezeMs >= 5000 {
		t.Fatalf("move: %+v", mv)
	}

	var after gen.Project
	e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &after)
	if after.Instance == nil || after.Instance.NodeName != "node-b" || after.Instance.Id == before.Instance.Id || after.Tier != gen.ProjectTierDedicated {
		t.Fatalf("after the move: %+v", after.Instance)
	}
	conn := e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var count, maxN int64
	if err := conn.QueryRow(ctx, `SELECT count(*), coalesce(max(n), 0) FROM ledger`).Scan(&count, &maxN); err != nil {
		t.Fatal(err)
	}
	if maxN < w.acked.Load() || count != maxN {
		t.Fatalf("ledger: %d rows, max %d; acknowledged %d", count, maxN, w.acked.Load())
	}
	if state, err := dockerInspect(t, "pgdock-"+before.Instance.Id.String(), "{{.State.Running}}"); err != nil || state != "false" {
		t.Fatalf("old instance should be stopped and kept: %q %v", state, err)
	}
	t.Logf("writes paused %d ms; %d commits acknowledged", *mv.FreezeMs, w.acked.Load())
}

// TestNodeMoveFallsBackToDump: a project with a large object can't move by
// logical replication; the move says why and copies with dump/restore.
func TestNodeMoveFallsBackToDump(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	ctx := context.Background()
	c := e.CreateProject("Large objects")
	conn := e.MustConnect(c.Connection.SessionUrl)
	if _, err := conn.Exec(ctx, `CREATE TABLE docs (id int PRIMARY KEY, body oid); INSERT INTO docs VALUES (1, lo_from_bytea(0, 'hello'))`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)

	var created gen.NodeCreated
	e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-b", PrivateAddr: "agent-test-2", Role: gen.CreateNodeRequestRoleShared}, &created)
	e.StartSecondAgent("node-b", created.Token)
	var op gen.Operation
	e.Do("POST", "/api/v1/nodes/"+created.Node.Id.String()+"/shared-cluster", gen.SharedClusterRequest{MemoryMb: 512}, &op)
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("shared cluster: %s", op.Status)
	}
	op, mv := moveAndWait(t, e, c.Project.Id.String(), created.Node.Id.String())
	if mv.Mode != gen.MoveModeDump || mv.FallbackReason == nil || !strings.Contains(*mv.FallbackReason, "large objects") {
		t.Fatalf("move: %+v\n%s", mv, testenv.FormatLog(op))
	}
	if !strings.Contains(testenv.FormatLog(op), "dump/restore") {
		t.Fatalf("log doesn't explain the fallback:\n%s", testenv.FormatLog(op))
	}
	conn = e.MustConnect(c.Connection.SessionUrl)
	defer conn.Close(ctx)
	var body []byte
	if err := conn.QueryRow(ctx, `SELECT lo_get(body) FROM docs WHERE id = 1`).Scan(&body); err != nil || string(body) != "hello" {
		t.Fatalf("large object after the move: %q %v", body, err)
	}
}
