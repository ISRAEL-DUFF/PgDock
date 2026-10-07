package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/statusapi"
	"github.com/israel-duff/pgdock/internal/statuspage"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

const statusSecret = "integration-status-push-secret-0123456789"

// startStatusPage runs pgdock-status in-process with PGDock's default
// components (probes always pass).
func startStatusPage(t *testing.T) (*statuspage.Service, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	cfg := `public_url = "https://status.pgdock.test"
push_secret = "` + statusSecret + `"
data = "` + filepath.Join(dir, "status.db") + `"
`
	for _, id := range []string{"dashboard", "edge-pooler", "shared-tier"} {
		cfg += "[[component]]\nid = \"" + id + "\"\n  [[component.probe]]\n  kind = \"tcp\"\n  addr = \"127.0.0.1:1\"\n"
	}
	for _, id := range []string{"dedicated", "backups", "webhooks-jobs"} {
		cfg += "[[component]]\nid = \"" + id + "\"\nheartbeat = true\n"
	}
	path := filepath.Join(dir, "status.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := statuspage.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := statuspage.New(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	svc.Probe = func(context.Context, statuspage.Probe) error { return nil }
	t.Cleanup(func() { _ = svc.Close() })
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	return svc, ts
}

func statusOf(t *testing.T, base, path string, v any) {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

// TestIncidentsReachTheStatusPage covers Admin → Incidents (V3 §2.6):
// incidents and their updates are pushed, signed, to pgdock-status, a
// failed push is kept and retried, and heartbeats report the components
// the status page can't probe.
func TestIncidentsReachTheStatusPage(t *testing.T) {
	status, ts := startStatusPage(t)
	e := testenv.Start(t, testenv.Options{StatusURL: ts.URL, StatusSecret: statusSecret})
	ctx := context.Background()

	var list gen.IncidentList
	if code := e.Do("GET", "/api/v1/incidents", nil, &list); code != http.StatusOK || !list.StatusPageConfigured || len(list.Components) != 6 {
		t.Fatalf("list: %d %+v", code, list)
	}
	var apiErr gen.Error
	if code := e.Do("POST", "/api/v1/incidents", gen.CreateIncidentRequest{Title: "x", Components: []string{"nope"},
		Severity: gen.IncidentSeverityMinor, Status: gen.IncidentStatusInvestigating, Body: "b"}, &apiErr); code != http.StatusBadRequest {
		t.Fatalf("unknown component: %d %+v", code, apiErr)
	}
	var inc gen.Incident
	if code := e.Do("POST", "/api/v1/incidents", gen.CreateIncidentRequest{Title: "Slow backups", Components: []string{"backups"},
		Severity: gen.IncidentSeverityMinor, Status: gen.IncidentStatusInvestigating, Body: "Backups are running late."}, &inc); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if inc.PushedAt != nil || len(inc.Updates) != 1 || inc.Updates[0].PostedBy == nil {
		t.Fatalf("created: %+v", inc)
	}
	if err := e.Incidents.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if code := e.Do("GET", "/api/v1/incidents/"+inc.Id.String(), nil, &inc); code != http.StatusOK || inc.PushedAt == nil || inc.PushError != nil {
		t.Fatalf("after push: %d %+v", code, inc)
	}
	var pub statusapi.Incident
	statusOf(t, ts.URL, "/api/v1/incidents/"+inc.Id.String(), &pub)
	if pub.Title != "Slow backups" || pub.Auto || len(pub.Updates) != 1 || pub.ResolvedAt != nil {
		t.Fatalf("on the status page: %+v", pub)
	}

	// Resolve it: the status page shows the resolution.
	inc = gen.Incident{Id: inc.Id}
	if code := e.Do("POST", "/api/v1/incidents/"+inc.Id.String()+"/updates", gen.IncidentUpdateRequest{
		Status: gen.IncidentStatusResolved, Body: "Caught up."}, &inc); code != http.StatusCreated || inc.ResolvedAt == nil || inc.PushedAt != nil {
		t.Fatalf("resolve: %d %+v", code, inc)
	}
	if err := e.Incidents.Push(ctx); err != nil {
		t.Fatal(err)
	}
	statusOf(t, ts.URL, "/api/v1/incidents/"+inc.Id.String(), &pub)
	if pub.Status != "resolved" || pub.ResolvedAt == nil || len(pub.Updates) != 2 {
		t.Fatalf("resolved on the status page: %+v", pub)
	}

	// Heartbeats: operational, then degraded while a backup alert fires.
	if err := e.Incidents.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := status.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	componentState := func(id string) string {
		var st struct {
			Components []struct{ ID, Status string } `json:"components"`
		}
		statusOf(t, ts.URL, "/api/v1/status", &st)
		for _, c := range st.Components {
			if c.ID == id {
				return c.Status
			}
		}
		return ""
	}
	if s := componentState("backups"); s != statusapi.Operational {
		t.Fatalf("backups after a heartbeat: %s", s)
	}
	if s := componentState("dedicated"); s != statusapi.Operational {
		t.Fatalf("dedicated after a heartbeat: %s", s)
	}
	if _, err := store.New(e.DB).FireAlert(ctx, store.FireAlertParams{Kind: "backup_failed", Key: "backup_failed:project:x", Severity: "warning",
		TargetType: "project", TargetID: "x", TargetName: "x", Summary: "x", Detail: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	if err := e.Incidents.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	if err := status.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if s := componentState("backups"); s != statusapi.Degraded {
		t.Fatalf("backups with a failed backup: %s", s)
	}

	// A push that fails is kept, with its error, and retried.
	ts.Close()
	title := "Slow backups (all regions)"
	inc = gen.Incident{Id: inc.Id}
	if code := e.Do("PATCH", "/api/v1/incidents/"+inc.Id.String(), gen.UpdateIncidentRequest{Title: &title}, &inc); code != http.StatusOK || inc.PushedAt != nil {
		t.Fatalf("edit: %d %+v", code, inc)
	}
	if err := e.Incidents.Push(ctx); err == nil {
		t.Fatal("a push to a closed status page succeeded")
	}
	inc = gen.Incident{Id: inc.Id}
	if e.Do("GET", "/api/v1/incidents/"+inc.Id.String(), nil, &inc); inc.PushError == nil || inc.PushedAt != nil || !strings.Contains(inc.Title, "all regions") {
		t.Fatalf("failed push not recorded: %+v", inc)
	}

	var ph gen.PoolerHosts
	if code := e.Do("GET", "/api/v1/pooler-hosts", nil, &ph); code != http.StatusOK || ph.Enabled {
		t.Fatalf("pooler hosts without an arbiter: %d %+v", code, ph)
	}
}
