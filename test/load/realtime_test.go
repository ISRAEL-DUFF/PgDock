package load

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestRealtimeLoad is V4-M34's load done-when (V4 §6.5, §13): 10,000
// realtime connections held on one region's edge, half subscribed to a
// table's changes and half to a broadcast channel, each receiving a change
// and a broadcast while held. The clients run in a child process (the
// same test binary), so each side has its own file descriptors. It writes
// tmp/realtime-load-report.md.
//
//	PGDOCK_TEST_RT_CONNS (10000), PGDOCK_TEST_RT_HOLD (60s)
func TestRealtimeLoad(t *testing.T) {
	if os.Getenv("PGDOCK_TEST_LOAD") == "" {
		t.Skip("set PGDOCK_TEST_LOAD=1 (make test-load)")
	}
	n := envInt("PGDOCK_TEST_RT_CONNS", 10000)
	hold, err := time.ParseDuration(os.Getenv("PGDOCK_TEST_RT_HOLD"))
	if err != nil || hold <= 0 {
		hold = time.Minute
	}
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	creds := e.CreateProject("rt-load")
	pid := creds.Project.Id
	p, err := store.New(e.DB).GetProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	var en gen.BackendServicesEnabled
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/services", nil, &en); code != http.StatusAccepted {
		t.Fatalf("enable: %d", code)
	}
	if op := e.WaitOperation(en.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable: %s", op.Status)
	}
	var pub, sec string
	for _, k := range en.Keys {
		if k.Key.Kind == gen.ApiKeyKindPublishable {
			pub = k.Value
		} else {
			sec = k.Value
		}
	}
	ref := *en.Services.Ref
	// Every client comes from one address: lift the per-IP and per-key
	// limits (the most the settings allow).
	if _, err := e.DB.Exec(ctx, `UPDATE project_services SET settings = settings || '{"rate_per_ip": 1000000, "rate_per_key": 10000000}'
		WHERE project_id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	ed := e.StartEdge()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if code, _, _ := ed.Do(ref, "GET", "/data/v1/health", nil, "apikey", pub); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the edge never reached the project")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// One user, a table they see their own rows of, with realtime on.
	code, _, body := ed.Do(ref, "POST", "/auth/v1/admin/users", strings.NewReader(`{"email":"load@example.com","password":"password-123456","email_confirm":true}`),
		"apikey", sec, "Content-Type", "application/json")
	if code != 201 {
		t.Fatalf("user: %d %s", code, body)
	}
	var user struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &user)
	code, _, body = ed.Do(ref, "POST", "/auth/v1/signin/password", strings.NewReader(`{"email":"load@example.com","password":"password-123456"}`),
		"apikey", pub, "Content-Type", "application/json")
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal([]byte(body), &session) != nil || session.AccessToken == "" {
		t.Fatalf("sign in: %d %s", code, body)
	}
	app := e.MustConnect(creds.Connection.PooledUrl)
	for _, st := range []string{
		`CREATE TABLE feed (id serial PRIMARY KEY, owner uuid NOT NULL, body text NOT NULL)`,
		`ALTER TABLE feed ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY own ON feed FOR SELECT TO %q USING (owner = pgd_auth.uid())`, store.UserRole(p.DbName)),
		`SELECT pgd_realtime.enable('feed')`,
	} {
		if _, err := app.Exec(ctx, st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}

	// The clients.
	cmd := exec.Command(os.Args[0], "-test.run=^TestRealtimeLoadClient$", "-test.v", "-test.timeout=0")
	cmd.Env = append(os.Environ(), "PGDOCK_RT_CHILD=1",
		"PGDOCK_RT_URL="+strings.Replace(ed.URL, "http://", "ws://", 1)+"/realtime/v1/websocket?vsn=1.0.0&apikey="+url.QueryEscape(pub),
		"PGDOCK_RT_HOST="+ref+"."+testenv.EdgeDomain, "PGDOCK_RT_TOKEN="+session.AccessToken,
		fmt.Sprintf("PGDOCK_RT_N=%d", n), "PGDOCK_RT_HOLD="+hold.String())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	wait := func(prefix string, d time.Duration) string {
		t.Helper()
		timeout := time.After(d)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("the clients exited before %s:\n%s", prefix, stderr.String())
				}
				if strings.HasPrefix(l, prefix) {
					return l
				}
			case <-timeout:
				t.Fatalf("no %s within %s:\n%s", prefix, d, stderr.String())
			}
		}
	}
	ready := wait("READY", 15*time.Minute)
	connected := time.Since(start)
	var ms0 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	goroutines := runtime.NumGoroutine()

	// While held: one change for the subscribers, one broadcast for the room.
	sent := time.Now()
	if _, err := app.Exec(ctx, `INSERT INTO feed (owner, body) VALUES ($1, 'load-ping')`, user.ID); err != nil {
		t.Fatal(err)
	}
	code, _, body = ed.Do(ref, "POST", "/realtime/v1/api/broadcast", strings.NewReader(`{"messages":[{"topic":"room","event":"load-bc","payload":{}}]}`),
		"apikey", sec, "Content-Type", "application/json")
	if code != 202 {
		t.Fatalf("broadcast: %d %s", code, body)
	}
	delivered := wait("DELIVERED", 5*time.Minute)
	deliveredIn := time.Since(sent)
	result := wait("RESULT", hold+5*time.Minute)
	app.Close(ctx)

	var r struct {
		Conns, Changes, Broadcasts, Alive, Errors int
	}
	if _, err := fmt.Sscanf(strings.TrimPrefix(result, "RESULT "), "conns=%d changes=%d broadcasts=%d alive=%d errors=%d",
		&r.Conns, &r.Changes, &r.Broadcasts, &r.Alive, &r.Errors); err != nil {
		t.Fatalf("result %q: %v", result, err)
	}
	report := fmt.Sprintf(`# Realtime load (V4-M34)

| | |
| --- | --- |
| Connections | %d |
| Connected and joined in | %s (%s) |
| Edge heap while held | %d MB |
| Edge process goroutines | %d |
| Change delivered to subscribers | %d of %d |
| Broadcast delivered | %d of %d |
| Delivery (%s) | %s |
| Alive after %s held | %d |
| Errors | %d |
`, n, connected.Round(time.Second), strings.TrimSpace(ready), ms0.HeapAlloc>>20, goroutines, r.Changes, n/2, r.Broadcasts, n-n/2,
		strings.TrimSpace(delivered), deliveredIn.Round(time.Millisecond), hold, r.Alive, r.Errors)
	_ = os.MkdirAll("../../tmp", 0o755)
	_ = os.WriteFile("../../tmp/realtime-load-report.md", []byte(report), 0o644)
	t.Log("\n" + report)
	if r.Conns != n || r.Alive != n || r.Changes != n/2 || r.Broadcasts != n-n/2 {
		t.Fatalf("not every connection held and received: %+v\n%s", r, stderr.String())
	}
}

// TestRealtimeLoadClient is TestRealtimeLoad's clients (run as its child).
func TestRealtimeLoadClient(t *testing.T) {
	if os.Getenv("PGDOCK_RT_CHILD") == "" {
		t.Skip("run by TestRealtimeLoad")
	}
	u, host, token := os.Getenv("PGDOCK_RT_URL"), os.Getenv("PGDOCK_RT_HOST"), os.Getenv("PGDOCK_RT_TOKEN")
	n := envInt("PGDOCK_RT_N", 10000)
	hold, _ := time.ParseDuration(os.Getenv("PGDOCK_RT_HOLD"))
	var changes, broadcasts, alive, errs atomic.Int64
	var got sync.WaitGroup
	got.Add(n)
	conns := make([]*websocket.Conn, n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 100)
	var dialErr atomic.Value
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			var ws *websocket.Conn
			var err error
			for attempt := 0; attempt < 5; attempt++ {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				var res *http.Response
				ws, res, err = websocket.Dial(ctx, u, &websocket.DialOptions{Host: host})
				cancel()
				if res != nil && res.Body != nil {
					_, _ = io.Copy(io.Discard, res.Body)
					_ = res.Body.Close()
				}
				if err == nil {
					break
				}
				time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
			}
			if err != nil {
				dialErr.Store(err.Error())
				errs.Add(1)
				got.Done()
				return
			}
			conns[i] = ws
			topic, cfg := "realtime:room", map[string]any{}
			want := "load-bc"
			if i%2 == 0 {
				topic = "realtime:feed"
				cfg = map[string]any{"postgres_changes": []map[string]any{{"event": "INSERT", "schema": "public", "table": "feed"}}}
				want = "load-ping"
			}
			join, _ := json.Marshal(map[string]any{"topic": topic, "event": "phx_join", "ref": "1", "join_ref": "1",
				"payload": map[string]any{"config": cfg, "access_token": token}})
			if err := ws.Write(context.Background(), websocket.MessageText, join); err != nil {
				errs.Add(1)
				got.Done()
				return
			}
			joined := make(chan bool, 1)
			go func() {
				done := false
				for {
					_, b, err := ws.Read(context.Background())
					if err != nil {
						if !done {
							got.Done()
						}
						return
					}
					var m struct {
						Event   string          `json:"event"`
						Payload json.RawMessage `json:"payload"`
						Ref     *string         `json:"ref"`
					}
					if json.Unmarshal(b, &m) != nil {
						continue
					}
					switch {
					case m.Event == "phx_reply" && m.Ref != nil && *m.Ref == "1":
						joined <- strings.Contains(string(m.Payload), `"status":"ok"`)
					case !done && strings.Contains(string(m.Payload), want):
						done = true
						if want == "load-ping" {
							changes.Add(1)
						} else {
							broadcasts.Add(1)
						}
						got.Done()
					}
				}
			}()
			select {
			case ok := <-joined:
				if !ok {
					errs.Add(1)
				}
			case <-time.After(60 * time.Second):
				errs.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if v := dialErr.Load(); v != nil {
		fmt.Fprintln(os.Stderr, "dial:", v)
	}
	fmt.Printf("READY %d connected\n", n-int(errs.Load()))
	// Heartbeats every 25 seconds, as the clients send them.
	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(25 * time.Second)
		defer tk.Stop()
		hb, _ := json.Marshal(map[string]any{"topic": "phoenix", "event": "heartbeat", "payload": map[string]any{}, "ref": "hb"})
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				for _, ws := range conns {
					if ws != nil {
						_ = ws.Write(context.Background(), websocket.MessageText, hb)
					}
				}
			}
		}
	}()
	waitAll := make(chan struct{})
	go func() { got.Wait(); close(waitAll) }()
	select {
	case <-waitAll:
	case <-time.After(3 * time.Minute):
	}
	fmt.Printf("DELIVERED %d changes, %d broadcasts\n", changes.Load(), broadcasts.Load())
	time.Sleep(hold)
	close(stop)
	// Alive: answers a heartbeat.
	var wg2 sync.WaitGroup
	for _, ws := range conns {
		if ws == nil {
			continue
		}
		wg2.Add(1)
		go func(ws *websocket.Conn) {
			defer wg2.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if ws.Ping(ctx) == nil {
				alive.Add(1)
			}
		}(ws)
	}
	wg2.Wait()
	fmt.Printf("RESULT conns=%d changes=%d broadcasts=%d alive=%d errors=%d\n", n-int(errs.Load()), changes.Load(), broadcasts.Load(), alive.Load(), errs.Load())
	for _, ws := range conns {
		if ws != nil {
			_ = ws.Close(websocket.StatusNormalClosure, "")
		}
	}
}
