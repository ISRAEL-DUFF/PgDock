package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/outbound"
	"github.com/israel-duff/pgdock/test/testenv"
)

// hookReq is one request a receiver got.
type hookReq struct {
	At     time.Time
	Header http.Header
	Body   []byte
	Event  struct {
		ID        string          `json:"id"`
		Webhook   string          `json:"webhook"`
		Table     string          `json:"table"`
		Type      string          `json:"type"`
		Record    json.RawMessage `json:"record"`
		OldRecord json.RawMessage `json:"old_record"`
	}
}

// receiver is a webhook endpoint that answers with status (200 unless
// set) and records what it accepted.
type receiver struct {
	*httptest.Server
	status atomic.Int32
	mu     sync.Mutex
	got    []hookReq
	tries  atomic.Int64
	notify chan struct{}
}

func newReceiver(t *testing.T) *receiver {
	rc := &receiver{notify: make(chan struct{}, 1000)}
	rc.status.Store(200)
	rc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rc.tries.Add(1)
		code := int(rc.status.Load())
		if code == 200 {
			hr := hookReq{At: time.Now(), Header: r.Header.Clone(), Body: body}
			_ = json.Unmarshal(body, &hr.Event)
			rc.mu.Lock()
			rc.got = append(rc.got, hr)
			rc.mu.Unlock()
			rc.notify <- struct{}{}
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(rc.Close)
	return rc
}

func (rc *receiver) received() []hookReq {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]hookReq(nil), rc.got...)
}

// waitN waits until n requests were accepted.
func (rc *receiver) waitN(t *testing.T, n int, within time.Duration) []hookReq {
	t.Helper()
	deadline := time.After(within)
	for {
		if got := rc.received(); len(got) >= n {
			return got
		}
		select {
		case <-rc.notify:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("receiver got %d of %d events", len(rc.received()), n)
		}
	}
}

// allowLocal lets the org's webhooks reach the test receiver on loopback,
// as the platform admin can for local testing (V2 §9.1).
func allowLocal(t *testing.T, e *testenv.Env, org uuid.UUID) {
	t.Helper()
	if code := e.Do("PUT", "/api/v1/admin/orgs/"+org.String()+"/outbound", gen.OutboundAllowlist{Hosts: []string{"127.0.0.1"}}, nil); code != http.StatusOK {
		t.Fatalf("allow-list: %d", code)
	}
}

func createWebhook(t *testing.T, e *testenv.Env, pid string, req gen.WebhookRequest) gen.WebhookCreated {
	t.Helper()
	var c gen.WebhookCreated
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/webhooks", req, &c); code != http.StatusCreated {
		t.Fatalf("create webhook: %d", code)
	}
	return c
}

func orderID(t *testing.T, h hookReq) int {
	t.Helper()
	var r struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(h.Event.Record, &r); err != nil {
		t.Fatalf("record %s: %v", h.Event.Record, err)
	}
	return r.ID
}

// TestWebhookDelivery is most of the M15 done-when: a committed insert
// reaches the receiver within one second with a valid signature, a
// rolled-back insert never does, and a webhook to the metadata address is
// refused.
func TestWebhookDelivery(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Shop")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, item text NOT NULL, qty int NOT NULL DEFAULT 1, note text)`); err != nil {
		t.Fatal(err)
	}
	rc := newReceiver(t)

	// Internal destinations are refused, the metadata address always.
	var apiErr gen.Error
	for _, u := range []string{"http://169.254.169.254/latest/meta-data", "https://169.254.169.254/", rc.URL + "/hook", "https://10.1.2.3/hook", "https://[::1]/hook"} {
		if code := e.Do("POST", "/api/v1/projects/"+pid+"/webhooks", gen.WebhookRequest{
			Name: "nope", Tables: []string{"orders"}, Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT}, Url: u,
		}, &apiErr); code != http.StatusBadRequest {
			t.Fatalf("a webhook to %s: %d %+v", u, code, apiErr)
		}
	}
	if code := e.Do("PUT", "/api/v1/admin/orgs/"+e.OrgID.String()+"/outbound", gen.OutboundAllowlist{Hosts: []string{"169.254.169.254"}}, nil); code != http.StatusBadRequest {
		t.Fatalf("allow-listing the metadata address: %d", code)
	}
	allowLocal(t, e, e.OrgID)
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/webhooks", gen.WebhookRequest{
		Name: "metadata", Tables: []string{"orders"}, Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT}, Url: "http://169.254.169.254/latest/meta-data",
	}, &apiErr); code != http.StatusBadRequest || !strings.Contains(apiErr.Message, "metadata") {
		t.Fatalf("a webhook to the metadata address with an allow-list: %d %+v", code, apiErr)
	}
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/webhooks", gen.WebhookRequest{
		Name: "missing", Tables: []string{"no_such"}, Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT}, Url: rc.URL,
	}, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("a webhook on a missing table: %d", code)
	}

	hdrs := map[string]string{"Authorization": "Bearer receiver-token"}
	wh := createWebhook(t, e, pid, gen.WebhookRequest{
		Name: "orders-to-shop", Tables: []string{"orders"}, Url: rc.URL + "/hook", Headers: &hdrs,
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT, gen.WebhookRequestEventsUPDATE, gen.WebhookRequestEventsDELETE},
	})
	if !strings.HasPrefix(wh.Secret, "whsec_") || wh.Webhook.Status != gen.WebhookStatusHealthy || len(wh.Webhook.HeaderNames) != 1 {
		t.Fatalf("created: %+v", wh)
	}

	// The project role can't touch PGDock's schema.
	if _, err := app.Exec(ctx, `INSERT INTO pgdock.webhook_outbox (webhook_id, table_name, op) VALUES ($1, 'x', 'INSERT')`, wh.Webhook.Id); err == nil {
		t.Fatal("the project role wrote to the outbox")
	}

	// A committed insert arrives within a second, signed.
	start := time.Now()
	if _, err := app.Exec(ctx, `INSERT INTO orders (item) VALUES ('kettle')`); err != nil {
		t.Fatal(err)
	}
	got := rc.waitN(t, 1, 5*time.Second)
	if lat := got[0].At.Sub(start); lat > time.Second {
		t.Errorf("the event arrived after %s, want under a second", lat)
	}
	first := got[0]
	if err := outbound.Verify(wh.Secret, first.Header.Get("PGDock-Signature"), first.Body, time.Now(), 5*time.Minute); err != nil {
		t.Fatalf("signature: %v (%q)", err, first.Header.Get("PGDock-Signature"))
	}
	if err := outbound.Verify("whsec_wrong", first.Header.Get("PGDock-Signature"), first.Body, time.Now(), 5*time.Minute); err == nil {
		t.Fatal("a wrong secret verified")
	}
	if first.Header.Get("Authorization") != "Bearer receiver-token" || first.Header.Get("PGDock-Event-Id") != first.Event.ID ||
		first.Event.Type != "INSERT" || first.Event.Table != "public.orders" || first.Event.Webhook != "orders-to-shop" ||
		!strings.Contains(string(first.Event.Record), `"kettle"`) || string(first.Event.OldRecord) != "null" {
		t.Fatalf("first event: %+v %v", first.Event, first.Header)
	}

	// A rolled-back insert never arrives; the commit after it does.
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO orders (item) VALUES ('ghost')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `UPDATE orders SET qty = 2 WHERE item = 'kettle'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `DELETE FROM orders WHERE item = 'kettle'`); err != nil {
		t.Fatal(err)
	}
	rc.waitN(t, 3, 5*time.Second)
	time.Sleep(time.Second) // anything else in flight would have arrived
	got = rc.received()
	if len(got) != 3 || got[1].Event.Type != "UPDATE" || !strings.Contains(string(got[1].Event.OldRecord), `"qty": 1`) && !strings.Contains(string(got[1].Event.OldRecord), `"qty":1`) ||
		got[2].Event.Type != "DELETE" || string(got[2].Event.Record) != "null" {
		t.Fatalf("events after the rollback: %d %+v", len(got), got)
	}
	for _, h := range got {
		if strings.Contains(string(h.Body), "ghost") {
			t.Fatal("a rolled-back insert was delivered")
		}
	}

	// The delivery log, and a test event.
	var log gen.WebhookDeliveryList
	e.Do("GET", "/api/v1/projects/"+pid+"/webhooks/"+wh.Webhook.Id.String()+"/deliveries", nil, &log)
	if len(log.Items) != 3 || !log.Items[0].Succeeded || log.Items[0].StatusCode == nil || *log.Items[0].StatusCode != 200 {
		t.Fatalf("delivery log: %+v", log.Items)
	}
	var tr gen.WebhookTestResult
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/webhooks/"+wh.Webhook.Id.String()+"/test", nil, &tr); code != http.StatusOK || !tr.Ok {
		t.Fatalf("test event: %d %+v", code, tr)
	}
	if got := rc.received(); got[len(got)-1].Event.Type != "TEST" {
		t.Fatalf("the test event: %+v", got[len(got)-1].Event)
	}

	// An UPDATE filter on one column.
	cols := []string{"qty"}
	only := createWebhook(t, e, pid, gen.WebhookRequest{
		Name: "qty-changes", Tables: []string{"public.orders"}, Url: rc.URL + "/qty", Columns: &cols,
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsUPDATE},
	})
	before := len(rc.received())
	if _, err := app.Exec(ctx, `INSERT INTO orders (item) VALUES ('mug')`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `UPDATE orders SET note = 'gift' WHERE item = 'mug'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, `UPDATE orders SET qty = 5 WHERE item = 'mug'`); err != nil {
		t.Fatal(err)
	}
	rc.waitN(t, before+4, 5*time.Second) // INSERT, two UPDATEs on the first; one UPDATE on the filtered one
	time.Sleep(500 * time.Millisecond)
	var filtered int
	for _, h := range rc.received()[before:] {
		if h.Event.Webhook == "qty-changes" {
			filtered++
		}
	}
	if filtered != 1 {
		t.Fatalf("the column filter sent %d events, want 1", filtered)
	}

	// Paused, a webhook stops recording changes; dropped triggers mark it broken.
	off := false
	if code := e.Do("PATCH", "/api/v1/projects/"+pid+"/webhooks/"+only.Webhook.Id.String(), gen.WebhookUpdate{Enabled: &off}, nil); code != http.StatusOK {
		t.Fatalf("pause: %d", code)
	}
	if _, err := app.Exec(ctx, `UPDATE orders SET qty = 6 WHERE item = 'mug'`); err != nil {
		t.Fatal(err)
	}
	var list gen.WebhookList
	e.Do("GET", "/api/v1/projects/"+pid+"/webhooks", nil, &list)
	for _, w := range list.Items {
		if w.Name == "qty-changes" && (w.Status != gen.WebhookStatusPaused || w.Backlog != 0) {
			t.Fatalf("paused webhook: %+v", w)
		}
	}
	var org gen.OrgOutbound
	e.Do("GET", "/api/v1/admin/orgs/"+e.OrgID.String()+"/outbound", nil, &org)
	if len(org.Hosts) != 1 || org.Hosts[0].Host != "127.0.0.1" || org.Hosts[0].Requests < 5 {
		t.Fatalf("outbound counters: %+v", org)
	}

	// Deleting the webhook removes its triggers.
	if code := e.Do("DELETE", "/api/v1/projects/"+pid+"/webhooks/"+only.Webhook.Id.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	var triggers int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgrelid = 'orders'::regclass AND NOT tgisinternal`).Scan(&triggers); err != nil || triggers != 2 {
		t.Fatalf("triggers left: %d %v", triggers, err)
	}
}

// TestWebhookReceiverDownAnHour is the done-when's "a receiver down for an
// hour receives every event in order afterwards": the head event is
// retried with backoff while the ones behind it wait, and nothing is lost.
func TestWebhookReceiverDownAnHour(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Ledger hooks")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, item text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	rc := newReceiver(t)
	allowLocal(t, e, e.OrgID)
	wh := createWebhook(t, e, pid, gen.WebhookRequest{Name: "down", Tables: []string{"orders"}, Url: rc.URL,
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT}})
	whPath := "/api/v1/projects/" + pid + "/webhooks/" + wh.Webhook.Id.String()

	rc.status.Store(503)
	const n = 8
	for i := range n {
		if _, err := app.Exec(ctx, `INSERT INTO orders (item) VALUES ($1)`, strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 5*time.Second, "the first failed attempt", func() bool { return rc.tries.Load() > 0 })
	// An hour passes, in 5-minute steps, with the receiver down.
	for range 12 {
		e.AutomationAdvance(5 * time.Minute)
		time.Sleep(400 * time.Millisecond)
	}
	var log gen.WebhookDeliveryList
	e.Do("GET", whPath+"/deliveries?limit=500", nil, &log)
	events := map[string]bool{}
	for _, d := range log.Items {
		if d.Succeeded || d.DeadLettered {
			t.Fatalf("while the receiver was down: %+v", d)
		}
		events[d.EventId] = true
	}
	if len(events) != 1 || len(log.Items) < 5 {
		t.Fatalf("attempts while down: %d over %d event(s), want several on the first event only", len(log.Items), len(events))
	}
	var w gen.Webhook
	e.Do("GET", whPath, nil, &w)
	if w.Status != gen.WebhookStatusFailing || !w.Enabled || w.Backlog != n {
		t.Fatalf("webhook while the receiver is down: %+v", w)
	}

	// Back up: everything arrives, in commit order, once each.
	rc.status.Store(200)
	deadline := time.Now().Add(60 * time.Second)
	for len(rc.received()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("after the receiver came back: %d of %d events", len(rc.received()), n)
		}
		e.AutomationAdvance(10 * time.Minute)
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	got := rc.received()
	if len(got) != n {
		t.Fatalf("delivered %d events, want %d", len(got), n)
	}
	for i, h := range got {
		if id := orderID(t, h); id != i+1 {
			t.Fatalf("event %d is order %d: out of order", i, id)
		}
	}
	e.Do("GET", whPath, nil, &w)
	if w.Status != gen.WebhookStatusHealthy || w.Backlog != 0 || w.ConsecutiveFailures != 0 {
		t.Fatalf("webhook after recovery: %+v", w)
	}
}

// TestWebhookRateLimitQueues is the done-when's "an org over its delivery
// rate sees deliveries queue, not drop".
func TestWebhookRateLimitQueues(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	team := e.CreateOrg("Busy team")
	setOverride(t, e, team, "webhook_deliveries_per_min", 5)
	c := e.CreateProjectIn("Busy app", team)
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, item text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	rc := newReceiver(t)
	allowLocal(t, e, team)
	wh := createWebhook(t, e, pid, gen.WebhookRequest{Name: "busy", Tables: []string{"orders"}, Url: rc.URL,
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT}})
	const n = 20
	if _, err := app.Exec(ctx, `INSERT INTO orders (item) SELECT 'item ' || g FROM generate_series(1, 20) g`); err != nil {
		t.Fatal(err)
	}
	rc.waitN(t, 5, 5*time.Second)
	time.Sleep(1500 * time.Millisecond)
	if got := len(rc.received()); got != 5 {
		t.Fatalf("within the first minute: %d deliveries, want the rate's 5", got)
	}
	var w gen.Webhook
	e.Do("GET", "/api/v1/projects/"+pid+"/webhooks/"+wh.Webhook.Id.String(), nil, &w)
	if w.Backlog != n-5 || w.Status != gen.WebhookStatusHealthy {
		t.Fatalf("queued, not dropped: %+v", w)
	}
	for minute := 0; len(rc.received()) < n; minute++ {
		if minute > 10 {
			t.Fatalf("after %d minutes: %d of %d", minute, len(rc.received()), n)
		}
		e.AutomationAdvance(time.Minute)
		time.Sleep(700 * time.Millisecond)
		if got := len(rc.received()); got > 5*(minute+2) {
			t.Fatalf("minute %d: %d deliveries, over the rate", minute+1, got)
		}
	}
	got := rc.received()
	for i, h := range got {
		if id := orderID(t, h); id != i+1 {
			t.Fatalf("event %d is order %d", i, id)
		}
	}
	var log gen.WebhookDeliveryList
	e.Do("GET", "/api/v1/projects/"+pid+"/webhooks/"+wh.Webhook.Id.String()+"/deliveries?limit=500", nil, &log)
	for _, d := range log.Items {
		if !d.Succeeded {
			t.Fatalf("a queued event counted as a failure: %+v", d)
		}
	}
}

// TestWebhookDeadLettersAndReplay: a 4xx dead-letters at once, the next
// event goes on, and a replay sends the dead letter again.
func TestWebhookDeadLettersAndReplay(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Replays")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, item text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	rc := newReceiver(t)
	allowLocal(t, e, e.OrgID)
	wh := createWebhook(t, e, pid, gen.WebhookRequest{Name: "picky", Tables: []string{"orders"}, Url: rc.URL,
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT}})
	whPath := "/api/v1/projects/" + pid + "/webhooks/" + wh.Webhook.Id.String()
	rc.status.Store(422)
	if _, err := app.Exec(ctx, `INSERT INTO orders (item) VALUES ('rejected')`); err != nil {
		t.Fatal(err)
	}
	var dead gen.WebhookDeliveryList
	waitFor(t, 5*time.Second, "a dead letter", func() bool {
		e.Do("GET", whPath+"/deliveries?dead=true", nil, &dead)
		return len(dead.Items) == 1
	})
	rc.status.Store(200)
	if _, err := app.Exec(ctx, `INSERT INTO orders (item) VALUES ('accepted')`); err != nil {
		t.Fatal(err)
	}
	got := rc.waitN(t, 1, 5*time.Second)
	if orderID(t, got[0]) != 2 {
		t.Fatalf("the event after a dead letter: %+v", got[0].Event)
	}
	var res gen.ReplayResult
	all := true
	if code := e.Do("POST", whPath+"/replay", gen.ReplayRequest{All: &all}, &res); code != http.StatusOK || res.Queued != 1 {
		t.Fatalf("replay: %d %+v", code, res)
	}
	got = rc.waitN(t, 2, 5*time.Second)
	if orderID(t, got[1]) != 1 || got[1].Event.ID != dead.Items[0].EventId {
		t.Fatalf("the replayed event: %+v (dead letter %s)", got[1].Event, dead.Items[0].EventId)
	}
	e.Do("GET", whPath+"/deliveries?dead=true", nil, &dead)
	if len(dead.Items) != 0 {
		t.Fatalf("dead letters after the replay: %+v", dead.Items)
	}
	// The secret rotates; the next event is signed with the new one.
	var sec gen.WebhookSecret
	if code := e.Do("POST", whPath+"/rotate-secret", nil, &sec); code != http.StatusOK || sec.Secret == wh.Secret {
		t.Fatalf("rotate: %d", code)
	}
	if _, err := app.Exec(ctx, `INSERT INTO orders (item) VALUES ('after rotation')`); err != nil {
		t.Fatal(err)
	}
	got = rc.waitN(t, 3, 5*time.Second)
	if err := outbound.Verify(sec.Secret, got[2].Header.Get("PGDock-Signature"), got[2].Body, time.Now(), 5*time.Minute); err != nil {
		t.Fatalf("signature after rotation: %v", err)
	}
}

// TestWebhooksAcrossCopies covers V2 §9.3: branches and restored copies
// don't carry webhooks, a restore in place reinstalls the triggers with an
// empty outbox, and a promotion moves the outbox with the data. A pgdock
// schema the project role made itself is replaced.
func TestWebhooksAcrossCopies(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	ctx := context.Background()
	c := e.CreateProject("Copies")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	// The tenant squats the schema name, with a table of its own in it.
	if _, err := app.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, item text NOT NULL);
		CREATE SCHEMA pgdock; CREATE TABLE pgdock.webhook_outbox (id int)`); err != nil {
		t.Fatal(err)
	}
	rc := newReceiver(t)
	allowLocal(t, e, e.OrgID)
	wh := createWebhook(t, e, pid, gen.WebhookRequest{Name: "copies", Tables: []string{"orders"}, Url: rc.URL,
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT}})
	whPath := "/api/v1/projects/" + pid + "/webhooks/" + wh.Webhook.Id.String()
	admin := e.SharedAdmin(c.Project.DbName)
	defer admin.Close(ctx)
	var superOwned bool
	if err := admin.QueryRow(ctx, `SELECT r.rolsuper FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner WHERE n.nspname = 'pgdock'`).Scan(&superOwned); err != nil || !superOwned {
		t.Fatalf("the pgdock schema is not the superuser's: %v %v", superOwned, err)
	}

	// Events wait in the outbox while the receiver is down; a backup takes them along.
	rc.status.Store(503)
	if _, err := app.Exec(ctx, `INSERT INTO orders (item) VALUES ('queued 1'), ('queued 2')`); err != nil {
		t.Fatal(err)
	}
	op := backupNow(t, e, pid)
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("backup: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var backups gen.BackupList
	e.Do("GET", "/api/v1/backups?project_id="+pid, nil, &backups)
	if len(backups.Items) == 0 {
		t.Fatal("no backup")
	}
	noSchema := func(url, what string) {
		t.Helper()
		conn := e.MustConnect(url)
		defer conn.Close(ctx)
		var present bool
		if err := conn.QueryRow(ctx, `SELECT to_regnamespace('pgdock') IS NOT NULL`).Scan(&present); err != nil || present {
			t.Fatalf("%s kept the webhook schema: %v %v", what, present, err)
		}
		var trg int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgrelid = 'orders'::regclass AND NOT tgisinternal`).Scan(&trg); err != nil || trg != 0 {
			t.Fatalf("%s kept %d trigger(s): %v", what, trg, err)
		}
	}

	// A branch doesn't get the parent's webhooks.
	live := gen.BranchRequestSourceLive
	var bc gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/branches", gen.BranchRequest{Name: "feature", Source: &live}, &bc); code != http.StatusAccepted {
		t.Fatalf("branch: %d", code)
	}
	if op = e.WaitOperation(bc.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("branch: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	noSchema(bc.Connection.PooledUrl, "a branch")

	// Nor does a restore into a new project.
	mode := gen.New
	name := "Copies restored"
	var rr gen.RestoreResponse
	if code := e.Do("POST", "/api/v1/backups/"+backups.Items[0].Id.String()+"/restore", gen.RestoreRequest{Mode: &mode, Name: &name}, &rr); code != http.StatusAccepted {
		t.Fatalf("restore to new: %d", code)
	}
	if op = e.WaitOperation(rr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("restore to new: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	noSchema(rr.Credentials.Connection.PooledUrl, "a restored copy")

	// In place: the triggers come back from the configuration, and the
	// restored past's events are not sent.
	e.Reauth()
	inPlace, confirm := gen.InPlace, "Copies"
	rr = gen.RestoreResponse{}
	if code := e.Do("POST", "/api/v1/backups/"+backups.Items[0].Id.String()+"/restore", gen.RestoreRequest{Mode: &inPlace, Confirm: &confirm}, &rr); code != http.StatusAccepted {
		t.Fatalf("restore in place: %d", code)
	}
	if op = e.WaitOperation(rr.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("restore in place: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var w gen.Webhook
	e.Do("GET", whPath, nil, &w)
	if w.Backlog != 0 {
		t.Fatalf("outbox after the restore: %d events", w.Backlog)
	}
	rc.status.Store(200)
	app2 := e.MustConnect(c.Connection.SessionUrl)
	defer app2.Close(ctx)
	if _, err := app2.Exec(ctx, `INSERT INTO orders (item) VALUES ('after restore')`); err != nil {
		t.Fatal(err)
	}
	rc.waitN(t, 1, 10*time.Second)
	time.Sleep(time.Second)
	if got := rc.received(); len(got) != 1 || !strings.Contains(string(got[0].Body), "after restore") {
		t.Fatalf("after the restore in place: %d events %s", len(got), got[0].Body)
	}

	// A promotion takes the outbox and triggers with the data.
	if os.Getenv("PGDOCK_TEST_PG_IMAGE") == "" {
		return
	}
	rc.status.Store(503)
	if _, err := app2.Exec(ctx, `INSERT INTO orders (item) VALUES ('queued before promotion')`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "a failed attempt", func() bool { return rc.tries.Load() > 1 })
	promoteSmall(t, e, pid)
	rc.status.Store(200)
	app3 := e.MustConnect(c.Connection.PooledUrl)
	defer app3.Close(ctx)
	if _, err := app3.Exec(ctx, `INSERT INTO orders (item) VALUES ('on dedicated')`); err != nil {
		t.Fatal(err)
	}
	for !strings.Contains(string(rc.received()[len(rc.received())-1].Body), "on dedicated") {
		e.AutomationAdvance(10 * time.Minute)
		time.Sleep(300 * time.Millisecond)
		if len(rc.received()) > 5 {
			break
		}
	}
	got := rc.received()
	if len(got) != 3 || !strings.Contains(string(got[1].Body), "queued before promotion") || !strings.Contains(string(got[2].Body), "on dedicated") {
		var bodies []string
		for _, g := range got {
			bodies = append(bodies, string(g.Body))
		}
		t.Fatalf("after the promotion: %v", bodies)
	}
}
