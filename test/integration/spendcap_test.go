package integration

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestSpendCapBackendServices: an organisation at its spend cap keeps
// signing in and reading its data, but its data API's rate limits tighten,
// new image transforms pause (cached ones still serve) and new realtime
// connections are refused while open ones stay (V4 §12). Lifting the cap
// restores them.
func TestSpendCapBackendServices(t *testing.T) {
	sp := newStorageProject(t, "capped-app", false)
	e, ctx := sp.e, context.Background()
	rate := 600
	if code := e.Do("PATCH", "/api/v1/projects/"+sp.pid.String()+"/services", gen.BackendServicesUpdate{
		Settings: &gen.BackendServicesSettings{RatePerIp: &rate}}, nil); code != http.StatusOK {
		t.Fatalf("settings: %d", code)
	}
	if r := sp.admin.do("POST", "/storage/v1/bucket", strings.NewReader(`{"id":"img","public":true}`), "", "Content-Type", "application/json"); r.Code != 201 {
		t.Fatalf("bucket: %s", r)
	}
	if r := sp.admin.do("POST", "/storage/v1/object/img/logo.png", bytes.NewReader(pngOf(t, 64, 64)), "", "Content-Type", "image/png"); r.Code != 200 {
		t.Fatalf("upload: %s", r)
	}
	keyless := files{t: t, ed: sp.ed, ref: sp.ref}
	if r := keyless.do("GET", "/storage/v1/render/img/logo.png?width=32", nil, ""); r.Code != 200 {
		t.Fatalf("render: %s", r)
	}
	adminAuth := authAPI{t: t, ed: sp.ed, ref: sp.ref, key: sp.admin.key}
	anonAuth := authAPI{t: t, ed: sp.ed, ref: sp.ref, key: sp.anon.key}
	if r := adminAuth.post("/auth/v1/admin/users", `{"email":"capped@app.test","password":"capped-password-1","email_confirm":true}`); r.Code != 201 {
		t.Fatalf("user: %d %s", r.Code, r.Body)
	}
	open := connectRT(t, sp.ed, sp.ref, sp.anon.key)
	if st, _ := open.join("realtime:room-a", map[string]any{"broadcast": map[string]any{"self": false}}, ""); st != "ok" {
		t.Fatalf("join before the cap: %s", st)
	}

	setCapped := func(capped bool) {
		t.Helper()
		// The forecast sets the flag (TestForecastBudgetAndSpendCap); here
		// it is set directly.
		if _, err := e.Billing.Account(ctx, e.OrgID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.DB.Exec(ctx, `UPDATE billing_accounts SET capped = $2 WHERE org_id = (SELECT org_id FROM projects WHERE id = $1)`, sp.pid, capped); err != nil {
			t.Fatal(err)
		}
	}
	dial := func() int {
		u := strings.Replace(sp.ed.URL, "http://", "ws://", 1) + "/realtime/v1/websocket?vsn=1.0.0&apikey=" + url.QueryEscape(sp.anon.key)
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ws, res, err := websocket.Dial(dctx, u, &websocket.DialOptions{Host: sp.ref + "." + testenv.EdgeDomain})
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		if err == nil {
			_ = ws.Close(websocket.StatusNormalClosure, "")
			return http.StatusSwitchingProtocols
		}
		if res == nil {
			t.Fatalf("dial: %v", err)
		}
		return res.StatusCode
	}

	// Each poll asks for a size not yet rendered, so none is cached.
	width := 1
	render := func() fileResult {
		width++
		return keyless.do("GET", fmt.Sprintf("/storage/v1/render/img/logo.png?width=%d", width), nil, "")
	}
	setCapped(true)
	waitFor(t, 20*time.Second, "the cap reaching the edge", func() bool {
		return render().Error.Code == "spend_cap_reached"
	})
	// A cached render still serves.
	if r := keyless.do("GET", "/storage/v1/render/img/logo.png?width=32", nil, ""); r.Code != 200 || r.Header.Get("X-Cache") != "HIT" {
		t.Fatalf("a cached render under the cap: %d %v", r.Code, r.Header)
	}
	if code := dial(); code != http.StatusTooManyRequests {
		t.Fatalf("a new realtime connection under the cap: %d", code)
	}
	// The connection opened before the cap still works.
	if st, _ := open.join("realtime:room-b", map[string]any{"broadcast": map[string]any{"self": false}}, ""); st != "ok" {
		t.Fatalf("join on an open connection under the cap: %s", st)
	}
	// Data still reads, at a quarter of the rate: 600 a minute allows a
	// burst of 100, 150 a minute a burst of 25.
	if r := sp.anon.do("GET", "/data/v1/health", nil, ""); r.Code != 200 {
		t.Fatalf("data under the cap: %s", r)
	}
	limitedAt := 0
	for i := range 60 {
		if r := sp.anon.do("GET", "/data/v1/health", nil, ""); r.Code == http.StatusTooManyRequests {
			if r.Error.Code != "spend_cap_rate_limited" {
				t.Fatalf("limited with %s", r)
			}
			limitedAt = i
			break
		}
	}
	if limitedAt == 0 {
		t.Fatal("60 requests at once never hit the capped limit")
	}
	// Keyless downloads share the tightened per-IP limit.
	if r := keyless.do("GET", "/storage/v1/public/img/logo.png", nil, ""); r.Code != http.StatusTooManyRequests || r.Error.Code != "spend_cap_rate_limited" {
		t.Fatalf("a public download past the capped limit: %s", r)
	}
	// Sign-in keeps its own limits.
	for i := range 3 {
		if r := anonAuth.post("/auth/v1/signin/password", `{"email":"capped@app.test","password":"capped-password-1"}`); r.Code != 200 {
			t.Fatalf("sign-in %d under the cap: %d %s", i, r.Code, r.Body)
		}
	}

	setCapped(false)
	waitFor(t, 20*time.Second, "lifting the cap reaching the edge", func() bool {
		return render().Code == 200
	})
	if code := dial(); code != http.StatusSwitchingProtocols {
		t.Fatalf("a new realtime connection after the cap: %d", code)
	}
	for i := range 40 {
		if r := sp.anon.do("GET", "/data/v1/health", nil, ""); r.Code != 200 {
			t.Fatalf("request %d after the cap: %s", i, r)
		}
	}
}
