// Package load is the spec §13 load test: 150 shared projects, pgbench on
// 10 of them, measuring create latency, pooler overhead, and the impact on
// a quiet neighbour. Run with `make test-load`; it writes a report to
// tmp/load-report.md.
package load

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	i := int(float64(len(s)-1) * p)
	return s[i]
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000) }

// probe runs n short queries on conn and returns their latencies.
func probe(ctx context.Context, conn *pgx.Conn, n int) ([]time.Duration, error) {
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		var v int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM probe WHERE id <= $1", 1+i%100).Scan(&v); err != nil {
			return out, err
		}
		out = append(out, time.Since(start))
	}
	return out, nil
}

// probeFor probes until stop is closed.
func probeFor(ctx context.Context, conn *pgx.Conn, stop <-chan struct{}) ([]time.Duration, error) {
	var out []time.Duration
	for {
		select {
		case <-stop:
			return out, nil
		default:
		}
		d, err := probe(ctx, conn, 1)
		out = append(out, d...)
		if err != nil {
			return out, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var tpsRe = regexp.MustCompile(`tps = ([0-9.]+)`)

func TestLoad(t *testing.T) {
	if os.Getenv("PGDOCK_TEST_LOAD") == "" {
		t.Skip("set PGDOCK_TEST_LOAD=1 (make test-load)")
	}
	pgbench, err := exec.LookPath("pgbench")
	if err != nil {
		t.Skip("pgbench not installed")
	}
	total := envInt("PGDOCK_LOAD_PROJECTS", 150)
	busy := envInt("PGDOCK_LOAD_BUSY", 10)
	seconds := envInt("PGDOCK_LOAD_SECONDS", 30)
	parallel := envInt("PGDOCK_LOAD_PARALLEL", 8)
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	report := &strings.Builder{}
	fmt.Fprintf(report, "# Load test\n\n%s, %d shared projects, pgbench on %d for %ds.\n\n", time.Now().UTC().Format(time.RFC3339), total, busy, seconds)

	// 1. Create the projects, `parallel` at a time, through the API.
	creds := make([]gen.ProjectCredentials, total)
	lat := make([]time.Duration, total)
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	errs := make(chan error, total)
	start := time.Now()
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t0 := time.Now()
			var c gen.ProjectCredentials
			if code := e.Do("POST", "/api/v1/projects", map[string]string{"name": fmt.Sprintf("load %03d", i)}, &c); code != http.StatusAccepted {
				errs <- fmt.Errorf("create %d: status %d", i, code)
				return
			}
			op := e.WaitOperation(c.Operation.Id)
			if op.Status != gen.OperationStatusSucceeded {
				errs <- fmt.Errorf("create %d: %s %s", i, op.Status, testenv.FormatLog(op))
				return
			}
			creds[i], lat[i] = c, time.Since(t0)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	admin := e.SharedAdmin("postgres")
	backends := func() (n int) {
		_ = admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'`).Scan(&n)
		return n
	}
	afterCreate := backends()
	time.Sleep(40 * time.Second) // past the poolers' server_idle_timeout
	settled := backends()
	fmt.Fprintf(report, "## Create latency\n\n| | |\n| --- | --- |\n| projects | %d in %s (%d at a time) |\n| p50 | %s |\n| p95 | %s |\n| max | %s |\n\n"+
		"Client backends on the shared cluster: %d right after the creates, %d after 40s idle.\n\n",
		total, elapsed.Round(time.Second), parallel, ms(pct(lat, .5)), ms(pct(lat, .95)), ms(pct(lat, 1)), afterCreate, settled)

	// 2. Pooler overhead on a quiet project: direct vs session vs transaction.
	quiet := creds[total-1]
	for _, u := range []string{quiet.Connection.SessionUrl} {
		c := e.MustConnect(u)
		if _, err := c.Exec(ctx, `CREATE TABLE probe AS SELECT g AS id FROM generate_series(1, 1000) g; CREATE INDEX ON probe (id); ANALYZE probe`); err != nil {
			t.Fatal(err)
		}
	}
	direct := e.MustConnect(e.DirectURL(quiet.Connection.SessionUrl, quiet.Project.DbName))
	sessionC := e.MustConnect(quiet.Connection.SessionUrl)
	txC := e.MustConnect(quiet.Connection.PooledUrl)
	fmt.Fprintf(report, "## Pooler overhead (1,000 indexed point queries, idle cluster)\n\n| path | p50 | p95 |\n| --- | --- | --- |\n")
	base := map[string][]time.Duration{}
	for _, x := range []struct {
		name string
		c    *pgx.Conn
	}{{"direct", direct}, {"session pooler", sessionC}, {"transaction pooler", txC}} {
		if _, err := probe(ctx, x.c, 100); err != nil { // warm up
			t.Fatal(err)
		}
		d, err := probe(ctx, x.c, 1000)
		if err != nil {
			t.Fatalf("%s: %v", x.name, err)
		}
		base[x.name] = d
		fmt.Fprintf(report, "| %s | %s | %s |\n", x.name, ms(pct(d, .5)), ms(pct(d, .95)))
	}
	report.WriteString("\n")

	// 3. pgbench on `busy` projects through the transaction pooler while
	// the quiet project keeps probing.
	dir := t.TempDir()
	type result struct {
		tps  float64
		out  string
		fail error
	}
	results := make([]result, busy)
	bench := func(i int, args ...string) ([]byte, error) {
		u, err := url.Parse(creds[i].Connection.PooledUrl)
		if err != nil {
			return nil, err
		}
		pw, _ := u.User.Password()
		host := u.Hostname()
		if a := os.Getenv("PGDOCK_TEST_POOLER_POOLED_ADDR"); a != "" {
			host = a[:strings.LastIndex(a, ":")]
		}
		cmd := exec.Command(pgbench, append([]string{"-h", host, "-p", u.Port(), "-U", u.User.Username()}, append(args, strings.TrimPrefix(u.Path, "/"))...)...)
		cmd.Env = append(os.Environ(), "PGPASSWORD="+pw, "PGSSLMODE=require")
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}
	for i := 0; i < busy; i++ {
		if out, err := bench(i, "-i", "-s", "2", "-q"); err != nil {
			t.Fatalf("pgbench -i on %d: %v\n%s", i, err, out)
		}
	}
	stop := make(chan struct{})
	var during []time.Duration
	var probeErr error
	var pwg sync.WaitGroup
	pwg.Add(1)
	go func() { defer pwg.Done(); during, probeErr = probeFor(ctx, txC, stop) }()
	var bwg sync.WaitGroup
	for i := 0; i < busy; i++ {
		bwg.Add(1)
		go func(i int) {
			defer bwg.Done()
			out, err := bench(i, "-c", "8", "-j", "2", "-T", strconv.Itoa(seconds), "-M", "simple", "--no-vacuum")
			r := result{out: string(out), fail: err}
			if m := tpsRe.FindStringSubmatch(string(out)); m != nil {
				r.tps, _ = strconv.ParseFloat(m[1], 64)
			}
			results[i] = r
		}(i)
	}
	// Watch the shared cluster's backend count while it runs.
	var maxBackends int
	watchStop := make(chan struct{})
	go func() {
		for {
			select {
			case <-watchStop:
				return
			case <-time.After(500 * time.Millisecond):
			}
			var n int
			if admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'`).Scan(&n) == nil && n > maxBackends {
				maxBackends = n
			}
		}
	}()
	bwg.Wait()
	close(stop)
	close(watchStop)
	pwg.Wait()
	if probeErr != nil {
		t.Fatalf("neighbour probe: %v", probeErr)
	}
	var maxConn int
	if err := admin.QueryRow(ctx, `SELECT current_setting('max_connections')::int`).Scan(&maxConn); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(report, "## pgbench (TPC-B-like, scale 2, 8 clients each, transaction pooler)\n\n| project | tps |\n| --- | --- |\n")
	var sum float64
	for i, r := range results {
		if r.fail != nil {
			t.Fatalf("pgbench on %d: %v\n%s", i, r.fail, r.out)
		}
		sum += r.tps
		fmt.Fprintf(report, "| load %03d | %.0f |\n", i, r.tps)
	}
	fmt.Fprintf(report, "| **total** | **%.0f** |\n\nPeak client backends on the shared cluster: %d of max_connections %d.\n\n", sum, maxBackends, maxConn)
	idle := base["transaction pooler"]
	fmt.Fprintf(report, "## Noisy neighbour (quiet project, transaction pooler)\n\n| | p50 | p95 | p99 |\n| --- | --- | --- | --- |\n")
	fmt.Fprintf(report, "| idle cluster | %s | %s | %s |\n| during pgbench | %s | %s | %s |\n\n",
		ms(pct(idle, .5)), ms(pct(idle, .95)), ms(pct(idle, .99)), ms(pct(during, .5)), ms(pct(during, .95)), ms(pct(during, .99)))

	if err := os.MkdirAll(filepath.Join("..", "..", "tmp"), 0o755); err == nil {
		_ = os.WriteFile(filepath.Join("..", "..", "tmp", "load-report.md"), []byte(report.String()), 0o644)
	}
	t.Log("\n" + report.String())

	// Budgets: every create succeeded (above); creates stay quick, the
	// neighbour stays responsive, and backends stay within the cluster.
	if p95 := pct(lat, .95); p95 > 30*time.Second {
		t.Errorf("create p95 %s over 30s", p95)
	}
	if p95 := pct(during, .95); p95 > 250*time.Millisecond {
		t.Errorf("neighbour p95 %s during load, over 250ms", p95)
	}
	if settled > total/2 {
		t.Errorf("%d idle backends linger after the creates", settled)
	}
	if maxBackends >= maxConn {
		t.Errorf("backends reached max_connections (%d)", maxConn)
	}
}
