package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestMoveUnderLoad is M18's done-when at scale: a dedicated project of
// PGDOCK_TEST_MOVE_GB gigabytes (CI: 1; the milestone: 20, `make test-move`)
// moves to another node by logical replication while a client keeps
// committing, with writes paused under 5 seconds and every acknowledged
// commit on the new node.
func TestMoveUnderLoad(t *testing.T) {
	gb, _ := strconv.Atoi(os.Getenv("PGDOCK_TEST_MOVE_GB"))
	if gb <= 0 {
		t.Skip("PGDOCK_TEST_MOVE_GB is not set")
	}
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "dedicated")
	ctx := context.Background()

	tier, profile, vol := gen.ProjectTierDedicated, "small", gb*3+5
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Large mover", Tier: &tier, Profile: &profile, VolumeGb: &vol}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}

	// About 1 KB a row, a million rows a gigabyte, over eight tables so
	// the initial copy runs in parallel.
	const tables, batch = 8, 250_000
	rows := int64(gb) * 1_000_000 / tables
	start := time.Now()
	conn := e.MustConnect(c.Connection.PooledUrl)
	for i := range tables {
		if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE TABLE bulk_%d (id bigint PRIMARY KEY, body text NOT NULL)`, i)); err != nil {
			t.Fatal(err)
		}
		for lo := int64(1); lo <= rows; lo += batch {
			hi := min(lo+batch-1, rows)
			if _, err := conn.Exec(ctx, fmt.Sprintf(`BEGIN; SET LOCAL statement_timeout = 0;
				INSERT INTO bulk_%d SELECT g, repeat(md5(g::text || '%d'), 30) FROM generate_series(%d, %d) g; COMMIT`, i, i, lo, hi)); err != nil {
				t.Fatalf("fill bulk_%d: %v", i, err)
			}
		}
	}
	var size int64
	if err := conn.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&size); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	t.Logf("filled %.1f GB in %s", float64(size)/1e9, time.Since(start).Round(time.Second))
	if size < int64(gb)*900_000_000 {
		t.Fatalf("database is %d bytes, want about %d GB", size, gb)
	}

	var created gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-b", PrivateAddr: "agent-test-2", Role: gen.CreateNodeRequestRoleDedicated}, &created); code != http.StatusCreated {
		t.Fatalf("create node: %d", code)
	}
	e.StartSecondAgent("node-b", created.Token)

	w := startWriter(t, e, c.Connection.PooledUrl)
	time.Sleep(time.Second)
	start = time.Now()
	op, mv := moveAndWait(t, e, c.Project.Id.String(), created.Node.Id.String())
	took := time.Since(start)
	time.Sleep(500 * time.Millisecond)
	w.Stop()
	t.Logf("move log:\n%s", testenv.FormatLog(op))
	if mv.Mode != gen.MoveModeLogical || mv.FreezeMs == nil {
		t.Fatalf("move: %+v", mv)
	}
	if *mv.FreezeMs >= 5000 {
		t.Errorf("writes paused for %d ms, want under 5 s", *mv.FreezeMs)
	}

	conn = e.MustConnect(c.Connection.PooledUrl)
	defer conn.Close(ctx)
	var count, maxN int64
	if err := conn.QueryRow(ctx, `SELECT count(*), coalesce(max(n), 0) FROM ledger`).Scan(&count, &maxN); err != nil {
		t.Fatal(err)
	}
	if maxN < w.acked.Load() || count != maxN {
		t.Fatalf("ledger after the move: %d rows, max %d, %d acknowledged", count, maxN, w.acked.Load())
	}
	for i := range tables {
		var n, sum int64
		if err := conn.QueryRow(ctx, fmt.Sprintf(`SELECT count(*), sum(id) FROM bulk_%d`, i)).Scan(&n, &sum); err != nil {
			t.Fatal(err)
		}
		if n != rows || sum != rows*(rows+1)/2 {
			t.Fatalf("bulk_%d after the move: %d rows (sum %d), want %d", i, n, sum, rows)
		}
	}
	t.Logf("%d GB moved in %s; writes paused %d ms (longest client gap %d ms); %d commits acknowledged, none lost",
		gb, took.Round(time.Second), *mv.FreezeMs, w.paused.Load(), w.acked.Load())
}
