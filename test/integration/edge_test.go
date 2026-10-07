package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestEdgeFoundation is V4-M28's done-when (V4 §2): a project with
// backend services enabled answers GET /data/v1/health at its own hostname,
// rejects keys from another project, and records request usage. Around it:
// the per-request role and claims (anon, service, a user's token), CORS,
// the secret key refused from browsers, rate limits, key revocation, the
// feed pushing changes to the edge, disabling, and the roles dropped with
// the project.
func TestEdgeFoundation(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	q := store.New(e.DB)

	pa := e.CreateProject("api-a").Project.Id
	pb := e.CreateProject("api-b").Project.Id

	enable := func(id uuid.UUID) (pub, sec string, svc gen.BackendServices) {
		t.Helper()
		var en gen.BackendServicesEnabled
		if code := e.Do("POST", "/api/v1/projects/"+id.String()+"/services", nil, &en); code != http.StatusAccepted {
			t.Fatalf("enable: %d", code)
		}
		if op := e.WaitOperation(en.Operation.Id); op.Status != gen.OperationStatusSucceeded {
			t.Fatalf("enable: %s\n%s", op.Status, testenv.FormatLog(op))
		}
		for _, k := range en.Keys {
			switch k.Key.Kind {
			case gen.ApiKeyKindPublishable:
				pub = k.Value
			case gen.ApiKeyKindSecret:
				sec = k.Value
			}
		}
		if !strings.HasPrefix(pub, "pgd_pub_") || !strings.HasPrefix(sec, "pgd_sec_") {
			t.Fatalf("first keys: %+v", en.Keys)
		}
		if code := e.Do("GET", "/api/v1/projects/"+id.String()+"/services", nil, &svc); code != http.StatusOK || !svc.Enabled ||
			svc.Ref == nil || svc.Url == nil || *svc.Url != "https://"+*svc.Ref+"."+testenv.EdgeDomain || len(svc.Keys) != 2 {
			t.Fatalf("services: %d %+v", code, svc)
		}
		for _, k := range svc.Keys {
			if (k.Kind == gen.ApiKeyKindPublishable) != (k.Key != nil) {
				t.Fatalf("only the publishable key is shown again: %+v", k)
			}
		}
		return pub, sec, svc
	}
	pubA, secA, svcA := enable(pa)
	refA := *svcA.Ref
	// Enabling twice is refused.
	var apiErr gen.Error
	if code := e.Do("POST", "/api/v1/projects/"+pa.String()+"/services", nil, &apiErr); code != http.StatusConflict {
		t.Fatalf("enable again: %d", code)
	}

	// The feed is for edges alone: unsigned or wrongly signed requests are
	// refused.
	res, err := http.Get(e.URL + "/api/v1/edge/config?since=0")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned feed request: %d", res.StatusCode)
	}

	ed := e.StartEdge()
	health := func(ref string, headers ...string) (int, http.Header, map[string]any) {
		t.Helper()
		code, h, body := ed.Do(ref, "GET", "/data/v1/health", nil, headers...)
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		return code, h, m
	}
	errCode := func(m map[string]any) string {
		if e, ok := m["error"].(map[string]any); ok {
			c, _ := e["code"].(string)
			return c
		}
		return ""
	}

	// The done-when: health at its own hostname, as anon with the
	// publishable key and as service with the secret key, through the
	// pooler in transaction mode (the roles don't leak between requests).
	for i, c := range []struct{ key, role string }{{pubA, "anon"}, {secA, "service"}, {pubA, "anon"}, {secA, "service"}} {
		code, h, m := health(refA, "apikey", c.key)
		if code != http.StatusOK || m["role"] != c.role || m["project"] != refA || h.Get("X-Request-Id") == "" {
			t.Fatalf("health %d as %s: %d %v", i, c.role, code, m)
		}
	}

	// Another project, enabled while the edge runs: the feed brings it in.
	pubB, secB, svcB := enable(pb)
	refB := *svcB.Ref
	waitFor(t, 15*time.Second, "the edge serving project B", func() bool {
		code, _, _ := health(refB, "apikey", pubB)
		return code == http.StatusOK
	})
	// Keys from another project are refused, both ways.
	for _, c := range []struct{ ref, key string }{{refA, pubB}, {refA, secB}, {refB, pubA}, {refB, secA}} {
		if code, _, m := health(c.ref, "apikey", c.key); code != http.StatusUnauthorized || errCode(m) != "invalid_key" {
			t.Fatalf("a key of another project on %s: %d %v", c.ref, code, m)
		}
	}
	if code, _, m := health(refA); code != http.StatusUnauthorized || errCode(m) != "key_required" {
		t.Fatalf("no key: %d %v", code, m)
	}
	if code, _, m := health("zzzzzzzz", "apikey", pubA); code != http.StatusNotFound || errCode(m) != "project_not_found" {
		t.Fatalf("unknown project: %d %v", code, m)
	}
	// A host that isn't <ref>.<domain> names no project; the edge's own
	// health check answers there.
	if code, _, body := ed.Do("", "GET", "/data/v1/health", nil, "apikey", pubA); code != http.StatusNotFound || !strings.Contains(body, "unknown_host") {
		t.Fatalf("bare domain: %d %s", code, body)
	}
	if code, _, body := ed.Do("", "GET", "/healthz", nil); code != http.StatusOK {
		t.Fatalf("edge health: %d %s", code, body)
	}

	// A user's access token: role user and its claims, for this project
	// only.
	uid := uuid.New()
	tok, err := e.Services.MintToken(ctx, pa, map[string]any{"sub": uid.String(), "role": "user"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, m := health(refA, "apikey", pubA, "Authorization", "Bearer "+tok); code != http.StatusOK || m["role"] != "user" || m["user_id"] != uid.String() {
		t.Fatalf("user token: %d %v", code, m)
	}
	if code, _, m := health(refB, "apikey", pubB, "Authorization", "Bearer "+tok); code != http.StatusUnauthorized || errCode(m) != "invalid_token" {
		t.Fatalf("A's token on B: %d %v", code, m)
	}
	if code, _, m := health(refA, "apikey", pubA, "Authorization", "Bearer "+tok[:len(tok)-4]+"AAAA"); code != http.StatusUnauthorized {
		t.Fatalf("tampered token: %d %v", code, m)
	}

	// Browsers: any origin until the project lists some; the secret key
	// is refused from a page.
	if code, h, _ := health(refA, "apikey", pubA, "Origin", "https://app.example"); code != http.StatusOK || h.Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("CORS: %d %v", code, h)
	}
	if code, _, m := health(refA, "apikey", secA, "Origin", "https://app.example"); code != http.StatusForbidden || errCode(m) != "secret_key_in_browser" {
		t.Fatalf("secret key from a page: %d %v", code, m)
	}
	ten := 10
	var svc gen.BackendServices
	if code := e.Do("PATCH", "/api/v1/projects/"+pa.String()+"/services", gen.BackendServicesUpdate{
		CorsOrigins: &[]string{"https://app.example"}, Settings: &gen.BackendServicesSettings{RatePerIp: &ten}}, &svc); code != http.StatusOK ||
		len(svc.CorsOrigins) != 1 || svc.Settings.RatePerIp == nil || *svc.Settings.RatePerIp != 10 {
		t.Fatalf("settings: %d %+v", code, svc)
	}
	waitFor(t, 15*time.Second, "the edge applying the allowed origins", func() bool {
		code, _, _ := health(refA, "apikey", pubA, "Origin", "https://evil.example")
		return code == http.StatusForbidden
	})
	if code, h, _ := ed.Do(refA, "OPTIONS", "/data/v1/health", nil, "Origin", "https://app.example", "Access-Control-Request-Method", "GET"); code != http.StatusNoContent ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "apikey") {
		t.Fatalf("preflight: %d %v", code, h)
	}
	limited := false
	for range 30 {
		if code, h, _ := health(refA, "apikey", pubA); code == http.StatusTooManyRequests && h.Get("Retry-After") != "" {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("30 requests at once never hit a 10 per minute limit")
	}

	// Usage and logs reach the server; a retried report counts once.
	ed.Flush(ctx)
	var requests float64
	if err := e.DB.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::float8 FROM usage_records WHERE project_id = $1 AND metric = 'api_requests'`, pa).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests < 8 {
		t.Fatalf("api_requests recorded for A: %v", requests)
	}
	var logs gen.ApiRequestLogList
	if code := e.Do("GET", "/api/v1/projects/"+pa.String()+"/services/logs?limit=500", nil, &logs); code != http.StatusOK || len(logs.Items) < 10 {
		t.Fatalf("request logs: %d %d", code, len(logs.Items))
	}
	statuses := map[int]int{}
	for _, l := range logs.Items {
		statuses[l.Status]++
	}
	if statuses[200] == 0 || statuses[401] == 0 || statuses[429] == 0 {
		t.Fatalf("logged statuses: %v", statuses)
	}
	keys, err := q.ListAPIKeys(ctx, pa)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.LastUsedAt == nil {
			t.Fatalf("key %s has no last use", k.Prefix)
		}
	}

	// Revoking a key: refused within seconds.
	var secKeyID uuid.UUID
	for _, k := range keys {
		if k.Kind == "secret" {
			secKeyID = k.ID
		}
	}
	if code := e.Do("DELETE", "/api/v1/projects/"+pa.String()+"/services/keys/"+secKeyID.String(), nil, nil); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	waitFor(t, 15*time.Second, "the edge refusing the revoked key", func() bool {
		code, _, _ := health(refA, "apikey", secA)
		return code == http.StatusUnauthorized
	})
	var created gen.CreatedApiKey
	if code := e.Do("POST", "/api/v1/projects/"+pa.String()+"/services/keys", gen.CreateApiKeyRequest{Kind: gen.CreateApiKeyRequestKindSecret, Name: "rotated"}, &created); code != http.StatusCreated {
		t.Fatalf("new key: %d", code)
	}
	waitFor(t, 15*time.Second, "the edge accepting the new key", func() bool {
		code, _, _ := health(refA, "apikey", created.Value)
		return code == http.StatusOK
	})

	// Suspension refuses the organisation's projects at the edge.
	p, err := q.GetProject(ctx, pa)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE organizations SET status = 'suspended' WHERE id = $1`, p.OrgID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "the edge refusing a suspended organisation", func() bool {
		code, _, m := health(refA, "apikey", pubA)
		return code == http.StatusForbidden && errCode(m) == "project_suspended"
	})
	if _, err := e.DB.Exec(ctx, `UPDATE organizations SET status = 'active' WHERE id = $1`, p.OrgID); err != nil {
		t.Fatal(err)
	}

	// Disabling B: not found at the edge, its edge login switched off.
	var op gen.Operation
	if code := e.Do("DELETE", "/api/v1/projects/"+pb.String()+"/services", nil, &op); code != http.StatusAccepted {
		t.Fatalf("disable: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("disable: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	waitFor(t, 15*time.Second, "the edge dropping project B", func() bool {
		code, _, _ := health(refB, "apikey", pubB)
		return code == http.StatusNotFound
	})
	b, err := q.GetProject(ctx, pb)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := e.Service.AdminConn(ctx, b.InstanceID, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var canLogin bool
	if err := conn.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, store.EdgeRole(b.DbName)).Scan(&canLogin); err != nil || canLogin {
		t.Fatalf("B's edge login after disabling: login %v, %v", canLogin, err)
	}

	// Deleting A drops its four roles.
	if code := e.Do("DELETE", "/api/v1/projects/"+pa.String()+"?confirm="+url.QueryEscape(p.Name), nil, &op); code != http.StatusAccepted {
		t.Fatalf("delete A: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("delete A: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = ANY($1)`, store.ServiceRoles(p.DbName)).Scan(&left); err != nil || left != 0 {
		t.Fatalf("A's roles after deleting it: %d %v", left, err)
	}
	waitFor(t, 15*time.Second, "the edge dropping the deleted project", func() bool {
		code, _, _ := health(refA, "apikey", pubA)
		return code == http.StatusNotFound
	})
}
