package load

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

type backendProject struct {
	pid      uuid.UUID
	ref      string
	pub, sec string
}

// TestBackendLoad is V4-M37's load test of backend services (V4 §13):
// PGDOCK_LOAD_BACKEND_PROJECTS (1,000) projects with backend services on
// the shared tier behind one edge, PGDOCK_LOAD_RPS (2,000) data API
// requests a second spread over all of them for PGDOCK_LOAD_SECONDS (60),
// four reads to each write, then a burst of image transforms
// (PGDOCK_LOAD_TRANSFORMS, 200 at once over 10 projects) with data
// requests still running. It writes tmp/load-report-backend.md.
//
// It fails if more than 0.1% of requests fail, fewer than 95% of the
// target rate are served, the data API's p95 exceeds 250 ms (500 ms
// during the transform burst), or a transform fails or the burst takes
// over a minute.
func TestBackendLoad(t *testing.T) {
	if os.Getenv("PGDOCK_TEST_LOAD") == "" {
		t.Skip("set PGDOCK_TEST_LOAD=1 (make test-load)")
	}
	total := envInt("PGDOCK_LOAD_BACKEND_PROJECTS", 1000)
	rps := envInt("PGDOCK_LOAD_RPS", 2000)
	seconds := envInt("PGDOCK_LOAD_SECONDS", 60)
	transforms := envInt("PGDOCK_LOAD_TRANSFORMS", 200)
	parallel := envInt("PGDOCK_LOAD_PARALLEL", 8)
	perOrg := 100
	e := testenv.Start(t, testenv.Options{})
	e.ConfigureBackups() // file storage for the transforms
	ctx := context.Background()
	t.Cleanup(func() {
		rows, _ := e.DB.Query(context.Background(), `SELECT db_name FROM projects`)
		names, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		c := e.SharedAdmin("postgres")
		defer c.Close(context.Background())
		for _, n := range names {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{n}.Sanitize()+" WITH (FORCE)")
		}
	})
	report := &strings.Builder{}
	defer func() {
		_, file, _, _ := runtime.Caller(0)
		out := filepath.Join(filepath.Dir(file), "..", "..", "tmp", "load-report-backend.md")
		_ = os.MkdirAll(filepath.Dir(out), 0o755)
		_ = os.WriteFile(out, []byte(report.String()), 0o644)
		t.Log("\n" + report.String())
	}()
	fmt.Fprintf(report, "# Backend services load test (V4-M37)\n\n%s: %d projects with backend services behind one edge; %d data API requests/s for %d s; %d image transforms at once.\n\n",
		time.Now().UTC().Format(time.RFC3339), total, rps, seconds, transforms)

	// ---- The projects ------------------------------------------------------------------
	orgs := make([]uuid.UUID, (total+perOrg-1)/perOrg)
	for i := range orgs {
		orgs[i] = e.CreateOrg(fmt.Sprintf("Backend load %02d", i))
		if code := e.Do("PATCH", "/api/v1/admin/orgs/"+orgs[i].String(), map[string]any{"limit_overrides": map[string]int64{
			"projects": 1000, "operations_in_flight": 1000, "shared_storage_mb": 1000000, "project_storage_mb": 100000,
		}}, nil); code != http.StatusOK {
			t.Fatalf("overrides: %d", code)
		}
	}
	projects := make([]backendProject, total)
	lat := make([]time.Duration, total)
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	errs := make(chan error, total)
	start := time.Now()
	for i := range total {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t0 := time.Now()
			var c gen.ProjectCredentials
			if code := e.Do("POST", "/api/v1/projects", map[string]any{"name": fmt.Sprintf("backend %04d", i), "org_id": orgs[i/perOrg]}, &c); code != http.StatusAccepted {
				errs <- fmt.Errorf("create %d: %d", i, code)
				return
			}
			if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
				errs <- fmt.Errorf("create %d: %s", i, op.Status)
				return
			}
			var en gen.BackendServicesEnabled
			if code := e.Do("POST", "/api/v1/projects/"+c.Project.Id.String()+"/services", nil, &en); code != http.StatusAccepted {
				errs <- fmt.Errorf("enable %d: %d", i, code)
				return
			}
			if op := e.WaitOperation(en.Operation.Id); op.Status != gen.OperationStatusSucceeded {
				errs <- fmt.Errorf("enable %d: %s %s", i, op.Status, testenv.FormatLog(op))
				return
			}
			bp := backendProject{pid: c.Project.Id, ref: *en.Services.Ref}
			for _, k := range en.Keys {
				if k.Key.Kind == gen.ApiKeyKindPublishable {
					bp.pub = k.Value
				} else {
					bp.sec = k.Value
				}
			}
			conn, err := pgx.Connect(ctx, c.Connection.PooledUrl)
			if err != nil {
				errs <- fmt.Errorf("connect %d: %w", i, err)
				return
			}
			_, err = conn.Exec(ctx, `CREATE TABLE items (id bigserial PRIMARY KEY, body text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
				INSERT INTO items (body) SELECT 'item ' || g FROM generate_series(1, 100) g`)
			conn.Close(ctx)
			if err != nil {
				errs <- fmt.Errorf("table %d: %w", i, err)
				return
			}
			projects[i], lat[i] = bp, time.Since(t0)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	fmt.Fprintf(report, "## Projects\n\n%d created with backend services in %s (%d at a time); each p50 %s, p95 %s.\n\n",
		total, time.Since(start).Round(time.Second), parallel, ms(pct(lat, .5)), ms(pct(lat, .95)))
	// Every request comes from one address and one key per project: lift
	// the per-IP and per-key limits so the test measures capacity.
	if _, err := e.DB.Exec(ctx, `UPDATE project_services SET settings = settings || '{"rate_per_ip": 1000000, "rate_per_key": 10000000}'`); err != nil {
		t.Fatal(err)
	}

	ed := e.StartEdge()
	tr := &http.Transport{MaxIdleConns: 2000, MaxIdleConnsPerHost: 2000, IdleConnTimeout: time.Minute}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	do := func(p backendProject, method, path string, body []byte, ctype string) (int, []byte, error) {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, ed.URL+path, r)
		if err != nil {
			return 0, nil, err
		}
		req.Host = p.ref + "." + testenv.EdgeDomain
		req.Header.Set("apikey", p.sec)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return res.StatusCode, b, err
	}
	waitAll := time.Now().Add(2 * time.Minute)
	for _, p := range projects {
		for {
			if code, _, _ := do(p, "GET", "/data/v1/items?limit=1", nil, ""); code == 200 {
				break
			}
			if time.Now().After(waitAll) {
				t.Fatalf("the edge never served %s", p.ref)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// ---- Data API at the target rate ------------------------------------------------------
	type result struct {
		ok   bool
		lat  time.Duration
		code int
	}
	run := func(rate int, d time.Duration, stop <-chan struct{}) (results []result, dropped int64) {
		jobs := make(chan struct{}, rate)
		out := make(chan result, rate*4)
		var workers sync.WaitGroup
		for range min(rate/2, 1000) + 50 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for range jobs {
					p := projects[rand.IntN(len(projects))]
					t0 := time.Now()
					var code int
					var err error
					if rand.IntN(5) == 0 {
						code, _, err = do(p, "POST", "/data/v1/items", []byte(`{"body":"load write"}`), "application/json")
					} else {
						code, _, err = do(p, "GET", "/data/v1/items?select=id,body&order=id:desc&limit=10", nil, "")
					}
					out <- result{ok: err == nil && code < 300, lat: time.Since(t0), code: code}
				}
			}()
		}
		var collected sync.WaitGroup
		collected.Add(1)
		go func() {
			defer collected.Done()
			for r := range out {
				results = append(results, r)
			}
		}()
		tick := time.NewTicker(10 * time.Millisecond)
		end := time.Now().Add(d)
		var owed float64
	loop:
		for now := range tick.C {
			if now.After(end) {
				break
			}
			select {
			case <-stop:
				break loop
			default:
			}
			owed += float64(rate) / 100
			for ; owed >= 1; owed-- {
				select {
				case jobs <- struct{}{}:
				default:
					dropped++
				}
			}
		}
		tick.Stop()
		close(jobs)
		workers.Wait()
		close(out)
		collected.Wait()
		return results, dropped
	}
	summarise := func(name string, rs []result, dropped int64, d time.Duration) (failRate float64, served float64, p95 time.Duration) {
		var lats []time.Duration
		failed := 0
		codes := map[int]int{}
		for _, r := range rs {
			lats = append(lats, r.lat)
			if !r.ok {
				failed++
				codes[r.code]++
			}
		}
		failRate = float64(failed) / float64(max(len(rs), 1))
		served = float64(len(rs)-failed) / d.Seconds()
		p95 = pct(lats, .95)
		fmt.Fprintf(report, "| %s | %d | %.0f/s | %d (%.3f%%) %v | %d | %s / %s / %s / %s |\n", name, len(rs), served, failed, failRate*100, codes,
			dropped, ms(pct(lats, .5)), ms(p95), ms(pct(lats, .99)), ms(pct(lats, 1)))
		return
	}
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	fmt.Fprintf(report, "## Data API\n\n| Phase | Requests | Served | Failed | Not sent (generator full) | p50 / p95 / p99 / max |\n| --- | --- | --- | --- | --- | --- |\n")
	var maxBackends atomic.Int64
	watchStop := make(chan struct{})
	go func() {
		c := e.SharedAdmin("postgres")
		defer c.Close(context.Background())
		for {
			select {
			case <-watchStop:
				return
			case <-time.After(time.Second):
			}
			var n int64
			if c.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'`).Scan(&n) == nil && n > maxBackends.Load() {
				maxBackends.Store(n)
			}
		}
	}()
	d := time.Duration(seconds) * time.Second
	// PGDOCK_LOAD_STEPS (e.g. "500,1000,1500") runs lower rates first,
	// for where latency turns; the target rate is judged.
	for _, f := range strings.Split(os.Getenv("PGDOCK_LOAD_STEPS"), ",") {
		var step int
		if _, err := fmt.Sscan(strings.TrimSpace(f), &step); err != nil || step <= 0 || step >= rps {
			continue
		}
		maxBackends.Store(0)
		rs, dropped := run(step, d/2, nil)
		summarise(fmt.Sprintf("%d/s for %ds (step; load %s, backends %d)", step, seconds/2, loadAvg(), maxBackends.Load()), rs, dropped, d/2)
	}
	maxBackends.Store(0)
	rs, dropped := run(rps, d, nil)
	failRate, served, p95 := summarise(fmt.Sprintf("%d/s for %ds (load %s, backends %d)", rps, seconds, loadAvg(), maxBackends.Load()), rs, dropped, d)
	fmt.Fprintf(report, "\nPeak client backends on the shared node so far: %d.\n", maxBackends.Load())

	// ---- Transform burst with data requests running ----------------------------------------
	// The pooler's idle server connections close after its
	// server_idle_timeout; wait for the backends to drain first.
	drainBy := time.Now().Add(90 * time.Second)
	for time.Now().Before(drainBy) {
		var n int64
		c := e.SharedAdmin("postgres")
		_ = c.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'`).Scan(&n)
		c.Close(ctx)
		if n < 300 {
			break
		}
		time.Sleep(2 * time.Second)
	}
	burstProjects := projects[:min(10, len(projects))]
	img := testPNG(1200, 900)
	for _, p := range burstProjects {
		if code, b, err := do(p, "POST", "/storage/v1/bucket", []byte(`{"id":"img","public":true}`), "application/json"); err != nil || code != 201 {
			t.Fatalf("bucket: %d %s %v", code, b, err)
		}
		if code, b, err := do(p, "POST", "/storage/v1/object/img/photo.png", img, "image/png"); err != nil || code != 200 {
			t.Fatalf("upload: %d %s %v", code, b, err)
		}
	}
	bgStop := make(chan struct{})
	var bg []result
	var bgDropped int64
	bgDone := make(chan struct{})
	bgStart := time.Now()
	go func() { bg, bgDropped = run(rps/4, 5*time.Minute, bgStop); close(bgDone) }()
	time.Sleep(2 * time.Second)
	var tLat []time.Duration
	var tMu sync.Mutex
	var tFail atomic.Int64
	tStart := time.Now()
	var tw sync.WaitGroup
	for i := range transforms {
		tw.Add(1)
		go func(i int) {
			defer tw.Done()
			p := burstProjects[i%len(burstProjects)]
			t0 := time.Now()
			code, _, err := do(p, "GET", fmt.Sprintf("/storage/v1/render/img/photo.png?width=%d&format=webp", 100+i), nil, "")
			if err != nil || code != 200 {
				tFail.Add(1)
			}
			tMu.Lock()
			tLat = append(tLat, time.Since(t0))
			tMu.Unlock()
		}(i)
	}
	tw.Wait()
	burst := time.Since(tStart)
	// The same renders again: from the cache.
	cStart := time.Now()
	var cFail atomic.Int64
	for i := range transforms {
		tw.Add(1)
		go func(i int) {
			defer tw.Done()
			p := burstProjects[i%len(burstProjects)]
			code, _, err := do(p, "GET", fmt.Sprintf("/storage/v1/render/img/photo.png?width=%d&format=webp", 100+i), nil, "")
			if err != nil || code != 200 {
				cFail.Add(1)
			}
		}(i)
	}
	tw.Wait()
	cached := time.Since(cStart)
	close(bgStop)
	<-bgDone
	bgFail, _, bgP95 := summarise(fmt.Sprintf("%d/s during the transform burst", rps/4), bg, bgDropped, time.Since(bgStart))
	close(watchStop)
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	fmt.Fprintf(report, "\nPeak client backends on the shared node: %d. Test process heap: %d MB before, %d MB after.\n\n",
		maxBackends.Load(), memBefore.HeapAlloc>>20, memAfter.HeapAlloc>>20)
	fmt.Fprintf(report, "## Image transforms\n\n| | |\n| --- | --- |\n| %d new renders at once (1200×900 PNG to WebP, %d projects) | %s in all, %d failed; each p50 %s, p95 %s, max %s |\n| the same again (cached) | %s in all, %d failed |\n",
		transforms, len(burstProjects), burst.Round(time.Millisecond), tFail.Load(), ms(pct(tLat, .5)), ms(pct(tLat, .95)), ms(pct(tLat, 1)),
		cached.Round(time.Millisecond), cFail.Load())

	switch {
	case failRate > 0.001:
		t.Errorf("%.3f%% of requests failed", failRate*100)
	case served < 0.95*float64(rps):
		t.Errorf("served %.0f/s of %d/s", served, rps)
	case p95 > 250*time.Millisecond:
		t.Errorf("data API p95 %s", p95)
	}
	if bgFail > 0.001 || bgP95 > 500*time.Millisecond {
		t.Errorf("during the transforms: %.3f%% failed, p95 %s", bgFail*100, bgP95)
	}
	if tFail.Load() > 0 || cFail.Load() > 0 || burst > time.Minute {
		t.Errorf("transforms: %d failed, %d cached failed, burst took %s", tFail.Load(), cFail.Load(), burst)
	}
}

// loadAvg is the host's one-minute load average.
func loadAvg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "?"
	}
	return strings.Fields(string(b))[0]
}

// testPNG is a w×h image with some detail to encode.
func testPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x ^ y) & 0xff), 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}
