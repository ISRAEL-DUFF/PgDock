package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// phoneProject is a project with backend services and SMS sign-in.
func phoneProject(t *testing.T, e *testenv.Env, name string) (ed *testenv.Edge, api, admin authAPI) {
	t.Helper()
	creds := e.CreateProject(name)
	pid := creds.Project.Id
	var en gen.BackendServicesEnabled
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/services", nil, &en); code != http.StatusAccepted {
		t.Fatalf("enable: %d", code)
	}
	if op := e.WaitOperation(en.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var pub, sec string
	for _, k := range en.Keys {
		if k.Key.Kind == gen.ApiKeyKindPublishable {
			pub = k.Value
		} else {
			sec = k.Value
		}
	}
	if code := e.Do("PATCH", "/api/v1/projects/"+pid.String()+"/auth/config", gen.AuthConfigUpdate{Settings: &gen.AuthSettings{
		PhoneChannels: &[]gen.AuthSettingsPhoneChannels{gen.AuthSettingsPhoneChannelsSms}}}, nil); code != 200 {
		t.Fatalf("auth config: %d", code)
	}
	ref := *en.Services.Ref
	ed = e.StartEdge()
	api, admin = authAPI{t: t, ed: ed, ref: ref, key: pub}, authAPI{t: t, ed: ed, ref: ref, key: sec}
	waitFor(t, 30*time.Second, "SMS sign-in at the edge", func() bool {
		r := api.call("GET", "/auth/v1/settings", "", "")
		return r.Code == 200 && containsAll(r.Body, `"sms"`)
	})
	return ed, api, admin
}

func containsAll(s string, subs ...string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}

// TestSMSProviderOutage is V4-M37's SMS failure injection (V4 §13, §15):
// with Termii down, codes go by Africa's Talking and are billed at its
// price; with both down, a code waits in the outbox, nothing is billed,
// password sign-in carries on, and the code goes once a provider is back;
// a code that can't go after its tries is recorded as failed, unbilled.
func TestSMSProviderOutage(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	_, api, admin := phoneProject(t, e, "sms-outage")
	if r := admin.post("/auth/v1/admin/users", `{"email":"pw@outage.test","password":"outage-password-1","email_confirm":true}`); r.Code != 201 {
		t.Fatalf("user: %d %s", r.Code, r.Body)
	}
	billed := func() (n int, kobo float64) {
		t.Helper()
		if err := e.DB.QueryRow(ctx, `SELECT coalesce(sum(quantity) FILTER (WHERE metric = 'messages_sms'), 0)::int,
			coalesce(sum(quantity) FILTER (WHERE metric = 'messages_sms_cost_kobo'), 0)::float8 FROM usage_records`).Scan(&n, &kobo); err != nil {
			t.Fatal(err)
		}
		return n, kobo
	}
	sends := func(status string) map[string]int {
		t.Helper()
		out := map[string]int{}
		rows, err := e.DB.Query(ctx, `SELECT provider, count(*) FROM message_sends WHERE channel = 'sms' AND status = $1 GROUP BY provider`, status)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			var n int
			if err := rows.Scan(&p, &n); err != nil {
				t.Fatal(err)
			}
			out[p] = n
		}
		return out
	}
	signIn := func(phone string, after int, fake *testenv.FakePhone) {
		t.Helper()
		code := fake.WaitCode(t, "sms", phone, after)
		if r := api.post("/auth/v1/verify", fmt.Sprintf(`{"type":"sms","phone":%q,"token":%q}`, phone, code)); r.Code != 200 || r.Session.AccessToken == "" {
			t.Fatalf("verify %s: %d %s", phone, r.Code, r.Body)
		}
	}
	otp := func(phone string) {
		t.Helper()
		if r := api.post("/auth/v1/signin/otp", fmt.Sprintf(`{"phone":%q,"channel":"sms"}`, phone)); r.Code != 200 {
			t.Fatalf("otp %s: %d %s", phone, r.Code, r.Body)
		}
	}
	send := func() {
		t.Helper()
		if err := e.Services.SendAuthEmails(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// ---- Healthy: Termii sends ------------------------------------------------------
	const ada = "+2348030000001"
	otp(ada)
	send()
	signIn(ada, 0, e.Phone)
	if s := sends("sent"); s["termii"] != 1 || len(s) != 1 {
		t.Fatalf("sent while healthy: %v", s)
	}

	// ---- Termii down: Africa's Talking sends, at its price ------------------------------
	e.Phone.SetDown(true)
	const bayo = "+2348030000002"
	otp(bayo)
	send()
	signIn(bayo, 0, e.PhoneFallback)
	const chi = "+2348030000003"
	tried := e.Phone.Tried()
	otp(chi)
	send()
	signIn(chi, 0, e.PhoneFallback)
	if e.Phone.Tried() != tried {
		t.Fatal("Termii was tried first again within its cooldown")
	}
	if s := sends("sent"); s["termii"] != 1 || s["africastalking"] != 2 {
		t.Fatalf("sent with Termii down: %v", s)
	}
	if n, kobo := billed(); n != 3 || kobo != 450+320+320 {
		t.Fatalf("billed %d messages, %v kobo", n, kobo)
	}

	// ---- Both down: the code waits, nothing is billed, passwords still work -------------
	e.PhoneFallback.SetDown(true)
	const dayo = "+2348030000004"
	otp(dayo)
	var attempts int
	var lastErr string
	waitFor(t, 20*time.Second, "a failed try of the waiting code", func() bool {
		send()
		_ = e.DB.QueryRow(ctx, `SELECT attempts, coalesce(last_error, '') FROM auth_message_outbox
			WHERE channel = 'sms' AND sent_at IS NULL`).Scan(&attempts, &lastErr)
		return attempts > 0
	})
	if attempts != 1 || !containsAll(lastErr, "termii", "africastalking") {
		t.Fatalf("the waiting code: %d tries, %q", attempts, lastErr)
	}
	if n, _ := billed(); n != 3 {
		t.Fatalf("billed %d messages during the outage", n)
	}
	if r := api.post("/auth/v1/signin/password", `{"email":"pw@outage.test","password":"outage-password-1"}`); r.Code != 200 {
		t.Fatalf("password sign-in during the outage: %d %s", r.Code, r.Body)
	}
	// Africa's Talking is back; the retry is due (its backoff, brought forward).
	e.PhoneFallback.SetDown(false)
	if _, err := e.DB.Exec(ctx, `UPDATE auth_message_outbox SET next_attempt_at = now() WHERE sent_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	send()
	signIn(dayo, 0, e.PhoneFallback)

	// ---- An outage past the tries: failed, recorded, not billed -----------------------
	e.PhoneFallback.SetDown(true)
	const ebi = "+2348030000005"
	otp(ebi)
	waitFor(t, 20*time.Second, "the code queued", func() bool {
		var n int
		_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM auth_message_outbox WHERE sent_at IS NULL AND message_enc IS NOT NULL`).Scan(&n)
		return n == 1
	})
	for range 5 {
		if _, err := e.DB.Exec(ctx, `UPDATE auth_message_outbox SET next_attempt_at = now() WHERE sent_at IS NULL AND message_enc IS NOT NULL`); err != nil {
			t.Fatal(err)
		}
		send()
	}
	var waiting int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM auth_message_outbox WHERE sent_at IS NULL AND message_enc IS NOT NULL`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if f := sends("failed"); waiting != 0 || len(f) != 1 {
		t.Fatalf("after 5 failed tries: %d waiting, failed %v", waiting, f)
	}
	if n, _ := billed(); n != 4 {
		t.Fatalf("billed %d messages, want 4", n)
	}
}

// TestEdgeCrashMidUpload is V4-M37's edge failure injection (V4 §13): a
// pgdock-edge process killed (SIGKILL) while a file streams through it
// leaves no object row and, after the storage sweep, no stored bytes; the
// client sees the connection drop; another edge serves the same upload
// when retried, and the file reads back whole.
func TestEdgeCrashMidUpload(t *testing.T) {
	sp := newStorageProject(t, "edge-crash", false)
	e, ctx := sp.e, context.Background()
	if r := sp.admin.do("POST", "/storage/v1/bucket", strings.NewReader(`{"id":"uploads"}`), "", "Content-Type", "application/json"); r.Code != 201 {
		t.Fatalf("bucket: %s", r)
	}
	if err := e.Services.StorageSweep(ctx); err != nil {
		t.Fatal(err)
	}
	before := len(objectKeys(t, sp))
	victim := e.StartEdgeProcess("edge-victim")
	waitFor(t, 30*time.Second, "the edge process reaches the project", func() bool {
		req, _ := http.NewRequest(http.MethodGet, victim.URL+"/data/v1/health", nil)
		req.Host = sp.ref + "." + testenv.EdgeDomain
		req.Header.Set("apikey", sp.admin.key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == 200
	})

	const size = 8 << 20
	content := bytes.Repeat([]byte("pgdock-crash-test-"), size/18+1)[:size]
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, victim.URL+"/storage/v1/object/uploads/big.bin", pr)
	req.Host = sp.ref + "." + testenv.EdgeDomain
	req.ContentLength = size
	req.Header.Set("apikey", sp.admin.key)
	req.Header.Set("Content-Type", "application/octet-stream")
	errc := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = res.Body.Close()
			err = fmt.Errorf("the upload finished: %d", res.StatusCode)
		}
		errc <- err
	}()
	// A third of the file has gone through when the edge dies.
	if _, err := pw.Write(content[:size/3]); err != nil {
		t.Fatal(err)
	}
	victim.Kill()
	_ = pw.CloseWithError(errors.New("the client gave up"))
	select {
	case err := <-errc:
		if err == nil || strings.Contains(err.Error(), "finished") {
			t.Fatalf("the client's upload: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the client never saw the edge go")
	}

	adm := sp.adminConn(t)
	defer adm.Close(ctx)
	var rows int
	if err := adm.QueryRow(ctx, `SELECT count(*) FROM pgd_storage.objects WHERE bucket = 'uploads'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d object rows after the crash", rows)
	}
	if err := e.Services.StorageSweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(objectKeys(t, sp)); n != before {
		t.Fatalf("stored objects after the crash and a sweep: %d, before %d", n, before)
	}

	// The client retries through the edge still running.
	if r := sp.admin.do("POST", "/storage/v1/object/uploads/big.bin", bytes.NewReader(content), "", "Content-Type", "application/octet-stream"); r.Code != 200 {
		t.Fatalf("the retried upload: %s", r)
	}
	if r := sp.admin.do("GET", "/storage/v1/object/uploads/big.bin", nil, ""); r.Code != 200 || !bytes.Equal(r.Body, content) {
		t.Fatalf("read back: %d, %d bytes", r.Code, len(r.Body))
	}
}

// TestRealtimeNodeLoss is V4-M37's realtime failure injection (V4 §6.5,
// §13): of a project's two edge processes, one is killed (SIGKILL) with
// clients on it. Clients on the other keep their changes and broadcasts;
// the dead process's presence leaves within the peer expiry (it never
// said goodbye); its clients see the connection drop, reconnect to the
// survivor and pick up where they were.
func TestRealtimeNodeLoss(t *testing.T) {
	rp := newRealtimeProject(t, "rt-node-loss")
	ctx := context.Background()
	victim := rp.e.StartEdgeProcess("rt-victim")
	waitFor(t, 30*time.Second, "the edge process reaches the project", func() bool {
		req, _ := http.NewRequest(http.MethodGet, victim.URL+"/data/v1/health", nil)
		req.Host = rp.ref + "." + testenv.EdgeDomain
		req.Header.Set("apikey", rp.pub)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == 200
	})
	room := func(key string) map[string]any {
		return map[string]any{"broadcast": map[string]any{"self": false}, "presence": map[string]any{"key": key},
			"postgres_changes": []map[string]any{{"event": "*", "schema": "public", "table": "todos"}}}
	}
	alice := connectRT(t, rp.ed, rp.ref, rp.pub)
	if st, resp := alice.join("realtime:room1", room("alice"), rp.alice); st != "ok" {
		t.Fatalf("alice joins: %s", resp)
	}
	bob := connectRTAt(t, victim.URL, rp.ref, rp.pub)
	if st, resp := bob.join("realtime:room1", room("bob"), rp.bob); st != "ok" {
		t.Fatalf("bob joins: %s", resp)
	}
	waitFor(t, 30*time.Second, "the two processes hear each other", func() bool {
		alice.send("realtime:room1", "broadcast", map[string]any{"type": "broadcast", "event": "ping", "payload": map[string]any{}})
		select {
		case m := <-bob.in:
			return isBroadcast("ping")(m)
		case <-time.After(500 * time.Millisecond):
			return false
		}
	})
	bob.send("realtime:room1", "presence", map[string]any{"type": "presence", "event": "track", "payload": map[string]any{"status": "online"}})
	alice.next(10*time.Second, "bob's presence at alice", func(m rtMessage) bool {
		return m.Event == "presence_diff" && strings.Contains(string(m.Payload), `"joins":{"bob"`)
	})

	killed := time.Now()
	victim.Kill()
	select {
	case <-bob.done:
	case <-time.After(10 * time.Second):
		t.Fatal("bob's connection outlived its edge")
	}

	// Alice, on the survivor, carries on.
	app := rp.ownerConn(t)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'after the loss')`, rp.aliceID); err != nil {
		t.Fatal(err)
	}
	alice.next(10*time.Second, "alice's change after the loss", isChange("INSERT", "after the loss"))
	carol := connectRT(t, rp.ed, rp.ref, rp.pub)
	if st, _ := carol.join("realtime:room1", map[string]any{"broadcast": map[string]any{"self": false}}, ""); st != "ok" {
		t.Fatal("carol joins")
	}
	alice.send("realtime:room1", "broadcast", map[string]any{"type": "broadcast", "event": "still-here", "payload": map[string]any{}})
	carol.next(5*time.Second, "a broadcast on the survivor", isBroadcast("still-here"))

	// The dead process's presence goes once it has been silent long enough.
	alice.next(60*time.Second, "bob's presence leaving", func(m rtMessage) bool {
		return m.Event == "presence_diff" && strings.Contains(string(m.Payload), `"leaves":{"bob"`)
	})
	t.Logf("bob's presence left %s after the kill", time.Since(killed).Round(time.Second))

	// Bob reconnects to the survivor and is back: presence and changes.
	bob2 := connectRT(t, rp.ed, rp.ref, rp.pub)
	if st, resp := bob2.join("realtime:room1", room("bob"), rp.bob); st != "ok" {
		t.Fatalf("bob rejoins: %s", resp)
	}
	bob2.send("realtime:room1", "presence", map[string]any{"type": "presence", "event": "track", "payload": map[string]any{"status": "back"}})
	alice.next(10*time.Second, "bob back at alice", func(m rtMessage) bool {
		return m.Event == "presence_diff" && strings.Contains(string(m.Payload), `"joins":{"bob"`) && strings.Contains(string(m.Payload), "back")
	})
	if _, err := app.Exec(ctx, `INSERT INTO todos (owner, body) VALUES ($1, 'bob is back')`, rp.bobID); err != nil {
		t.Fatal(err)
	}
	bob2.next(10*time.Second, "bob's change after reconnecting", isChange("INSERT", "bob is back"))
}
