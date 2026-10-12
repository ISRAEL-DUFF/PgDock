package integration

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/outbound"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestWebhookIntegratorContract is what integrators such as Taskiem build
// on (Taskiem P1-G3, Q22, Q23, Q28): a description and metadata to tag the
// webhooks a tool created, the project's id and the row's primary key on
// every event, and a secret rotation whose old secret keeps signing for
// an overlap.
func TestWebhookIntegratorContract(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Integrations")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE orders (id serial PRIMARY KEY, status text NOT NULL);
		CREATE TABLE audit (at timestamptz NOT NULL DEFAULT now(), what text)`); err != nil {
		t.Fatal(err)
	}
	allowLocal(t, e, e.OrgID)
	rc := newReceiver(t)
	base := "/api/v1/projects/" + pid + "/webhooks"

	// ---- Description and metadata --------------------------------------------------
	bad := map[string]string{"has space": "x"}
	var apiErr gen.Error
	if code := e.Do("POST", base, gen.WebhookRequest{Name: "bad", Tables: []string{"orders"}, Url: rc.URL,
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT}, Metadata: &bad}, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("a metadata key with a space: %d %+v", code, apiErr)
	}
	desc := "Created by Taskiem for workflow wf_1"
	meta := map[string]string{"created_by": "taskiem", "taskiem:workflow": "wf_1"}
	wh := createWebhook(t, e, pid, gen.WebhookRequest{
		Name: "taskiem-wf_1", Tables: []string{"orders", "audit"}, Url: rc.URL + "/hook", Description: &desc, Metadata: &meta,
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT, gen.WebhookRequestEventsUPDATE, gen.WebhookRequestEventsDELETE},
	})
	if wh.Webhook.Description != desc || !maps.Equal(wh.Webhook.Metadata, meta) || wh.Webhook.PreviousSecretExpiresAt != nil {
		t.Fatalf("created: %+v", wh.Webhook)
	}
	get := func() gen.Webhook {
		t.Helper()
		var w gen.Webhook
		if code := e.Do("GET", base+"/"+wh.Webhook.Id.String(), nil, &w); code != http.StatusOK {
			t.Fatalf("get: %d", code)
		}
		return w
	}
	if w := get(); w.Description != desc || !maps.Equal(w.Metadata, meta) {
		t.Fatalf("got: %+v", w)
	}
	// PATCH replaces metadata when given, keeps it when not; same for the
	// description.
	meta2 := map[string]string{"created_by": "taskiem", "taskiem:workflow": "wf_2"}
	if code := e.Do("PATCH", base+"/"+wh.Webhook.Id.String(), gen.WebhookUpdate{Metadata: &meta2}, nil); code != http.StatusOK {
		t.Fatalf("patch metadata: %d", code)
	}
	url := rc.URL + "/hook"
	if code := e.Do("PATCH", base+"/"+wh.Webhook.Id.String(), gen.WebhookUpdate{Url: &url}, nil); code != http.StatusOK {
		t.Fatalf("patch url: %d", code)
	}
	if w := get(); w.Description != desc || !maps.Equal(w.Metadata, meta2) {
		t.Fatalf("after the patches: %+v", w)
	}

	// ---- project_id and primary_key on every event ---------------------------------------
	type payload struct {
		Type       string         `json:"type"`
		Table      string         `json:"table"`
		Project    string         `json:"project"`
		ProjectID  string         `json:"project_id"`
		PrimaryKey map[string]any `json:"primary_key"`
	}
	if _, err := app.Exec(ctx, `INSERT INTO orders (status) VALUES ('new'); UPDATE orders SET status = 'paid' WHERE id = 1;
		DELETE FROM orders WHERE id = 1; INSERT INTO audit (what) VALUES ('no key')`); err != nil {
		t.Fatal(err)
	}
	got := rc.waitN(t, 4, 15*time.Second)
	for i, want := range []string{"INSERT", "UPDATE", "DELETE", "INSERT"} {
		var p payload
		if err := json.Unmarshal(got[i].Body, &p); err != nil {
			t.Fatal(err)
		}
		if p.Type != want || p.ProjectID != pid || !strings.HasPrefix(p.Project, "p_") {
			t.Fatalf("event %d: %s", i, got[i].Body)
		}
		switch p.Table {
		case "public.orders":
			if len(p.PrimaryKey) != 1 || p.PrimaryKey["id"] != float64(1) {
				t.Fatalf("event %d's primary key: %s", i, got[i].Body)
			}
		case "public.audit":
			if p.PrimaryKey != nil {
				t.Fatalf("a table without a primary key: %s", got[i].Body)
			}
		}
	}

	// ---- Rotation with an overlap: both secrets sign -------------------------------------------
	rotate := func(body any) (gen.WebhookSecret, int) {
		t.Helper()
		var s gen.WebhookSecret
		code := e.Do("POST", base+"/"+wh.Webhook.Id.String()+"/rotate-secret", body, &s)
		return s, code
	}
	if _, code := rotate(map[string]int{"overlap_seconds": 90000}); code != http.StatusBadRequest {
		t.Fatalf("an overlap over a day: %d", code)
	}
	old := wh.Secret
	rot, code := rotate(map[string]int{"overlap_seconds": 3600})
	if code != http.StatusOK || rot.Secret == old || rot.PreviousSecretExpiresAt == nil ||
		time.Until(*rot.PreviousSecretExpiresAt) < 59*time.Minute {
		t.Fatalf("rotate with an overlap: %d %+v", code, rot)
	}
	if w := get(); w.PreviousSecretExpiresAt == nil {
		t.Fatal("the webhook doesn't show the overlap")
	}
	verifyBoth := func(h hookReq, secrets ...string) {
		t.Helper()
		for _, s := range secrets {
			if err := outbound.Verify(s, h.Header.Get("PGDock-Signature"), h.Body, time.Now(), 5*time.Minute); err != nil {
				t.Fatalf("verify with %s…: %v (%s)", s[:10], err, h.Header.Get("PGDock-Signature"))
			}
		}
	}
	if _, err := app.Exec(ctx, `INSERT INTO orders (status) VALUES ('during')`); err != nil {
		t.Fatal(err)
	}
	got = rc.waitN(t, 5, 15*time.Second)
	sig := got[4].Header.Get("PGDock-Signature")
	if strings.Count(sig, "v1=") != 2 {
		t.Fatalf("during the overlap: %s", sig)
	}
	verifyBoth(got[4], old, rot.Secret)

	// ---- Rotation without one: only the new secret ---------------------------------------------
	rot2, code := rotate(nil)
	if code != http.StatusOK || rot2.PreviousSecretExpiresAt != nil {
		t.Fatalf("rotate without an overlap: %d %+v", code, rot2)
	}
	if _, err := app.Exec(ctx, `INSERT INTO orders (status) VALUES ('after')`); err != nil {
		t.Fatal(err)
	}
	got = rc.waitN(t, 6, 15*time.Second)
	if strings.Count(got[5].Header.Get("PGDock-Signature"), "v1=") != 1 {
		t.Fatalf("after a rotation without an overlap: %s", got[5].Header.Get("PGDock-Signature"))
	}
	verifyBoth(got[5], rot2.Secret)
	for _, s := range []string{old, rot.Secret} {
		if outbound.Verify(s, got[5].Header.Get("PGDock-Signature"), got[5].Body, time.Now(), 5*time.Minute) == nil {
			t.Fatal("a rotated-out secret still verifies")
		}
	}
	if w := get(); w.PreviousSecretExpiresAt != nil {
		t.Fatalf("no overlap, but the webhook shows one: %+v", w.PreviousSecretExpiresAt)
	}
}
