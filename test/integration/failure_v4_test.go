package integration

import (
	"context"
	"fmt"
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
