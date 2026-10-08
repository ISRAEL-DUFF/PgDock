package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// realtimeProject is a project with backend services, two users, and a
// todos table each user sees only their own rows of, with realtime on.
type realtimeProject struct {
	e          *testenv.Env
	ed         *testenv.Edge
	ref        string
	pid        uuid.UUID
	db         string
	pub, sec   string
	owner      string // the project owner's pooled URL
	alice, bob string
	aliceID    uuid.UUID
	bobID      uuid.UUID
}

func newRealtimeProject(t *testing.T, name string) *realtimeProject {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	creds := e.CreateProject(name)
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
		t.Fatalf("enable: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	rp := &realtimeProject{e: e, ref: *en.Services.Ref, pid: pid, db: p.DbName, owner: creds.Connection.PooledUrl}
	for _, k := range en.Keys {
		if k.Key.Kind == gen.ApiKeyKindPublishable {
			rp.pub = k.Value
		} else {
			rp.sec = k.Value
		}
	}
	rp.ed = e.StartEdge()
	waitFor(t, 30*time.Second, "the edge reaches the project's database", func() bool {
		code, _, _ := rp.ed.Do(rp.ref, "GET", "/data/v1/health", nil, "apikey", rp.pub)
		return code == 200
	})
	api := authAPI{t: t, ed: rp.ed, ref: rp.ref, key: rp.sec}
	user := func(email string) (string, uuid.UUID) {
		if r := api.post("/auth/v1/admin/users", fmt.Sprintf(`{"email":%q,"password":"password-123456","email_confirm":true}`, email)); r.Code != 201 {
			t.Fatalf("create %s: %d %s", email, r.Code, r.Body)
		}
		r := authAPI{t: t, ed: rp.ed, ref: rp.ref, key: rp.pub}.post("/auth/v1/signin/password", fmt.Sprintf(`{"email":%q,"password":"password-123456"}`, email))
		if r.Code != 200 {
			t.Fatalf("sign in %s: %d %s", email, r.Code, r.Body)
		}
		return r.Session.AccessToken, r.Session.User.ID
	}
	rp.alice, rp.aliceID = user("alice@example.com")
	rp.bob, rp.bobID = user("bob@example.com")
	app := rp.ownerConn(t)
	defer app.Close(ctx)
	for _, st := range []string{
		`CREATE TABLE todos (id serial PRIMARY KEY, owner uuid NOT NULL, body text NOT NULL, done boolean NOT NULL DEFAULT false)`,
		`ALTER TABLE todos ENABLE ROW LEVEL SECURITY`,
		fmt.Sprintf(`CREATE POLICY own_todos ON todos FOR ALL TO %q USING (owner = pgd_auth.uid()) WITH CHECK (owner = pgd_auth.uid())`,
			store.UserRole(p.DbName)),
		`SELECT pgd_realtime.enable('todos')`,
	} {
		if _, err := app.Exec(ctx, st); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	return rp
}

func (rp *realtimeProject) ownerConn(t *testing.T) *pgx.Conn {
	t.Helper()
	return rp.e.MustConnect(rp.owner)
}

// TestRealtimeCapture: enabling a table captures its committed changes in
// the outbox, and a rolled-back change leaves nothing.
func TestRealtimeCapture(t *testing.T) {
	rp := newRealtimeProject(t, "rt-capture")
	ctx := context.Background()
	app := rp.ownerConn(t)
	defer app.Close(ctx)

	if _, err := app.Exec(ctx, `SELECT pgd_realtime.enable('pgd_realtime.outbox')`); err == nil {
		t.Fatal("enabled realtime on a platform table")
	}
	if _, err := app.Exec(ctx, `CREATE TABLE nokey (x int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `SELECT pgd_realtime.enable('nokey')`); err == nil {
		t.Fatal("enabled realtime on a table without a primary key")
	}
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'rolled back')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'kept')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `UPDATE todos SET done = true WHERE body = 'kept'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `DELETE FROM todos WHERE body = 'kept'`); err != nil {
		t.Fatal(err)
	}
	p, err := store.New(rp.e.DB).GetProject(ctx, rp.pid)
	if err != nil {
		t.Fatal(err)
	}
	adm, err := rp.e.Service.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		t.Fatal(err)
	}
	defer adm.Close(ctx)
	rows, err := adm.Query(ctx, `SELECT op, coalesce(record ->> 'body', ''), coalesce(old_record::text, '-') FROM pgd_realtime.outbox ORDER BY xid, id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var op, body, old string
		if err := rows.Scan(&op, &body, &old); err != nil {
			t.Fatal(err)
		}
		got = append(got, op+" "+body+" "+old)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	want := []string{`INSERT kept -`, `UPDATE kept {"id": 2}`, `DELETE  {"id": 2}`}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("outbox: %q, want %q", got, want)
	}
	if _, err := app.Exec(ctx, `SELECT pgd_realtime.disable('todos')`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'after')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := adm.QueryRow(ctx, `SELECT count(*) FROM pgd_realtime.outbox WHERE record ->> 'body' = 'after'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("captured after disable: %d %v", n, err)
	}
}

// rtClient is a realtime WebSocket client speaking the Phoenix protocol
// (vsn 1.0.0), as supabase-js does.
type rtClient struct {
	t    *testing.T
	ws   *websocket.Conn
	in   chan rtMessage
	ref  int
	done chan struct{}
}

type rtMessage struct {
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Ref     *string         `json:"ref"`
	JoinRef *string         `json:"join_ref"`
}

// connectRT opens a WebSocket to ed for project ref with key.
func connectRT(t *testing.T, ed *testenv.Edge, ref, key string) *rtClient {
	t.Helper()
	u := strings.Replace(ed.URL, "http://", "ws://", 1) + "/realtime/v1/websocket?vsn=1.0.0&apikey=" + url.QueryEscape(key)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{Host: ref + "." + testenv.EdgeDomain})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		code := 0
		if res != nil {
			code = res.StatusCode
		}
		t.Fatalf("dial realtime: %d %v", code, err)
	}
	ws.SetReadLimit(1 << 20)
	c := &rtClient{t: t, ws: ws, in: make(chan rtMessage, 1000), done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for {
			_, b, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var m rtMessage
			if json.Unmarshal(b, &m) == nil {
				c.in <- m
			}
		}
	}()
	t.Cleanup(func() { _ = ws.Close(websocket.StatusNormalClosure, "") })
	return c
}

func (c *rtClient) send(topic, event string, payload any) string {
	c.t.Helper()
	c.ref++
	ref := fmt.Sprint(c.ref)
	b, _ := json.Marshal(map[string]any{"topic": topic, "event": event, "payload": payload, "ref": ref, "join_ref": ref})
	if err := c.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatalf("send %s: %v", event, err)
	}
	return ref
}

// next waits for a message matching ok, failing after d.
func (c *rtClient) next(d time.Duration, what string, ok func(rtMessage) bool) rtMessage {
	c.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m := <-c.in:
			if ok(m) {
				return m
			}
		case <-deadline:
			c.t.Fatalf("no %s within %s", what, d)
		}
	}
}

// quiet fails if a message matching ok arrives within d.
func (c *rtClient) quiet(d time.Duration, what string, ok func(rtMessage) bool) {
	c.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m := <-c.in:
			if ok(m) {
				c.t.Fatalf("unexpected %s: %s %s", what, m.Event, m.Payload)
			}
		case <-deadline:
			return
		}
	}
}

// join joins topic with config and waits for the reply.
func (c *rtClient) join(topic string, config map[string]any, token string) (status string, response json.RawMessage) {
	c.t.Helper()
	payload := map[string]any{"config": config}
	if token != "" {
		payload["access_token"] = token
	}
	ref := c.send(topic, "phx_join", payload)
	m := c.next(10*time.Second, "join reply", func(m rtMessage) bool {
		return m.Event == "phx_reply" && m.Ref != nil && *m.Ref == ref
	})
	var r struct {
		Status   string          `json:"status"`
		Response json.RawMessage `json:"response"`
	}
	_ = json.Unmarshal(m.Payload, &r)
	return r.Status, r.Response
}

// change is a postgres_changes message's data.
type rtChange struct {
	IDs  []int64 `json:"ids"`
	Data struct {
		Type      string         `json:"type"`
		Table     string         `json:"table"`
		Record    map[string]any `json:"record"`
		OldRecord map[string]any `json:"old_record"`
	} `json:"data"`
}

func isChange(op, body string) func(rtMessage) bool {
	return func(m rtMessage) bool {
		if m.Event != "postgres_changes" {
			return false
		}
		var c rtChange
		_ = json.Unmarshal(m.Payload, &c)
		if c.Data.Type != op {
			return false
		}
		return body == "" || c.Data.Record["body"] == body
	}
}

func anyChange(m rtMessage) bool { return m.Event == "postgres_changes" }

// TestRealtime covers V4-M34's done-when (V4 §6): two users subscribed to
// the same table each receive only the rows their policies allow, and a
// rolled-back insert produces nothing. Around it: updates and deletes
// (deletes reach only those who saw the row), filters, refused
// subscriptions, heartbeats, broadcast and presence across two edge
// processes, private channels, history, and the change rate cap.
func TestRealtime(t *testing.T) {
	rp := newRealtimeProject(t, "rt-app")
	ctx := context.Background()
	app := rp.ownerConn(t)
	defer app.Close(ctx)
	todos := map[string]any{"postgres_changes": []map[string]any{{"event": "*", "schema": "public", "table": "todos"}}}

	alice := connectRT(t, rp.ed, rp.ref, rp.pub)
	bob := connectRT(t, rp.ed, rp.ref, rp.pub)
	if st, resp := alice.join("realtime:todos", todos, rp.alice); st != "ok" || !strings.Contains(string(resp), `"table":"todos"`) {
		t.Fatalf("alice joins: %s %s", st, resp)
	}
	if st, resp := bob.join("realtime:todos", todos, rp.bob); st != "ok" {
		t.Fatalf("bob joins: %s %s", st, resp)
	}
	alice.next(5*time.Second, "subscribed", func(m rtMessage) bool {
		return m.Event == "system" && strings.Contains(string(m.Payload), "Subscribed")
	})

	// ---- Each sees only their own rows -------------------------------------------
	if _, err := app.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'alice 1'), ($2, 'bob 1')`, rp.aliceID, rp.bobID); err != nil {
		t.Fatal(err)
	}
	alice.next(10*time.Second, "alice's insert", isChange("INSERT", "alice 1"))
	bob.next(10*time.Second, "bob's insert", isChange("INSERT", "bob 1"))
	alice.quiet(time.Second, "bob's row at alice", isChange("INSERT", "bob 1"))
	bob.quiet(100*time.Millisecond, "alice's row at bob", isChange("INSERT", "alice 1"))

	// ---- A rolled-back insert produces nothing -----------------------------------
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'never')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	alice.quiet(2*time.Second, "a rolled-back insert", anyChange)

	// ---- Updates and deletes -------------------------------------------------------
	if _, err := app.Exec(ctx, `UPDATE todos SET done = true WHERE body = 'alice 1'`); err != nil {
		t.Fatal(err)
	}
	m := alice.next(10*time.Second, "alice's update", isChange("UPDATE", "alice 1"))
	var ch rtChange
	_ = json.Unmarshal(m.Payload, &ch)
	if ch.Data.Record["done"] != true || ch.Data.OldRecord["id"] == nil {
		t.Fatalf("update payload: %s", m.Payload)
	}
	// Carol (another publishable-key client, as bob) joins after alice's
	// row exists: its delete must not reach her.
	carol := connectRT(t, rp.ed, rp.ref, rp.pub)
	if st, _ := carol.join("realtime:todos", todos, rp.bob); st != "ok" {
		t.Fatal("carol joins")
	}
	if _, err := app.Exec(ctx, `DELETE FROM todos WHERE body = 'alice 1'`); err != nil {
		t.Fatal(err)
	}
	m = alice.next(10*time.Second, "alice's delete", isChange("DELETE", ""))
	_ = json.Unmarshal(m.Payload, &ch)
	if ch.Data.OldRecord["id"] == nil {
		t.Fatalf("delete payload: %s", m.Payload)
	}
	bob.quiet(time.Second, "alice's delete at bob", isChange("DELETE", ""))
	carol.quiet(100*time.Millisecond, "alice's delete at a later subscriber", isChange("DELETE", ""))

	// ---- Filters --------------------------------------------------------------------
	filtered := map[string]any{"postgres_changes": []map[string]any{{"event": "INSERT", "schema": "public", "table": "todos", "filter": "body=eq.wanted"}}}
	if st, _ := alice.join("realtime:wanted", filtered, rp.alice); st != "ok" {
		t.Fatal("filtered join")
	}
	if _, err := app.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'unwanted'), ($1, 'wanted')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	alice.next(10*time.Second, "the filtered insert", func(m rtMessage) bool {
		return m.Topic == "realtime:wanted" && isChange("INSERT", "wanted")(m)
	})
	alice.quiet(time.Second, "an insert the filter excludes", func(m rtMessage) bool {
		return m.Topic == "realtime:wanted" && isChange("INSERT", "unwanted")(m)
	})

	// ---- Refused subscriptions ------------------------------------------------------
	if _, err := app.Exec(ctx, `CREATE TABLE notes (id serial PRIMARY KEY, body text)`); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]map[string]any{
		"realtime off": {"postgres_changes": []map[string]any{{"event": "*", "schema": "public", "table": "notes"}}},
		"no table":     {"postgres_changes": []map[string]any{{"event": "*", "schema": "public", "table": "nope"}}},
		"bad filter":   {"postgres_changes": []map[string]any{{"event": "*", "schema": "public", "table": "todos", "filter": "body~x"}}},
		"bad event":    {"postgres_changes": []map[string]any{{"event": "TRUNCATE", "schema": "public", "table": "todos"}}},
	} {
		if st, resp := alice.join("realtime:bad-"+strings.ReplaceAll(name, " ", "-"), cfg, rp.alice); st != "error" {
			t.Fatalf("%s: %s %s", name, st, resp)
		}
	}
	if _, err := app.Exec(ctx, `SELECT pgd_realtime.enable('notes')`); err != nil {
		t.Fatal(err)
	}
	if st, resp := alice.join("realtime:notes", map[string]any{"postgres_changes": []map[string]any{{"event": "*", "schema": "public", "table": "notes"}}}, rp.alice); st != "error" ||
		!strings.Contains(string(resp), "row-level security") {
		t.Fatalf("a table without row-level security: %s %s", st, resp)
	}
	if st, resp := alice.join("realtime:expired", todos, "not.a.token"); st != "error" || !strings.Contains(string(resp), "invalid token") {
		t.Fatalf("a bad token: %s %s", st, resp)
	}

	// ---- Heartbeats ------------------------------------------------------------------
	ref := alice.send("phoenix", "heartbeat", map[string]any{})
	alice.next(5*time.Second, "heartbeat reply", func(m rtMessage) bool { return m.Event == "phx_reply" && m.Ref != nil && *m.Ref == ref })
}
