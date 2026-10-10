package integration

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/outbound"
	"github.com/israel-duff/pgdock/test/testenv"
)

// webhookFixture is one signed sample delivery (Taskiem S2), for
// integrators to test their verifier and parser against.
type webhookFixture struct {
	Description string            `json:"description"`
	Secret      string            `json:"secret"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
}

const fixtureDir = "../../docs/integrations/fixtures/webhooks"

// TestWebhookFixtures captures one real delivery of each kind and checks
// the published samples in docs/integrations/fixtures/webhooks still match
// what PGDock sends: the same fields, and signatures that verify with the
// sample's throwaway secret. PGDOCK_WRITE_FIXTURES=1 rewrites them.
func TestWebhookFixtures(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	c := e.CreateProject("Fixtures")
	pid := c.Project.Id.String()
	app := e.MustConnect(c.Connection.SessionUrl)
	defer app.Close(ctx)
	if _, err := app.Exec(ctx, `CREATE TABLE orders (
		id bigserial PRIMARY KEY, status text NOT NULL, total numeric(12,2), paid boolean NOT NULL DEFAULT false,
		tags text[], meta jsonb, created_at timestamptz NOT NULL DEFAULT now(), notes text)`); err != nil {
		t.Fatal(err)
	}
	allowLocal(t, e, e.OrgID)
	rc := newReceiver(t)
	wh := createWebhook(t, e, pid, gen.WebhookRequest{Name: "taskiem-wf_sample", Tables: []string{"orders"}, Url: rc.URL + "/hook",
		Events: []gen.WebhookRequestEvents{gen.WebhookRequestEventsINSERT, gen.WebhookRequestEventsUPDATE, gen.WebhookRequestEventsDELETE}})

	if _, err := app.Exec(ctx, `
		INSERT INTO orders (status, total, tags, meta) VALUES ('new', 1500.00, '{priority,lagos}', '{"channel":"web"}');
		UPDATE orders SET status = 'paid', paid = true WHERE id = 1;
		DELETE FROM orders WHERE id = 1;
		INSERT INTO orders (status, notes) VALUES ('big', repeat('x', 300000))`); err != nil {
		t.Fatal(err)
	}
	rc.waitN(t, 4, 15*time.Second) // the changes first, so the test event is last
	var tr gen.WebhookTestResult
	if code := e.Do("POST", "/api/v1/projects/"+pid+"/webhooks/"+wh.Webhook.Id.String()+"/test", nil, &tr); code != http.StatusOK || !tr.Ok {
		t.Fatalf("test event: %d %+v", code, tr)
	}
	got := rc.waitN(t, 5, 15*time.Second)

	names := []struct{ file, desc string }{
		{"insert.json", "An INSERT: record is the new row; old_record is null."},
		{"update.json", "An UPDATE: record is the new row, old_record the whole row before it (every column)."},
		{"delete.json", "A DELETE: record is null, old_record the deleted row."},
		{"insert-truncated.json", "An INSERT of a row over 256 KB: truncated is true, record and old_record are null, primary_key identifies the row to fetch."},
		{"test.json", "The event POST /projects/{id}/webhooks/{id}/test sends: type TEST, no table or row."},
	}
	write := os.Getenv("PGDOCK_WRITE_FIXTURES") == "1"
	for i, n := range names {
		h := got[i]
		fresh := webhookFixture{Description: n.desc, Secret: wh.Secret, Body: string(h.Body), Headers: map[string]string{}}
		for _, k := range []string{"Content-Type", "User-Agent", "PGDock-Event-Id", "PGDock-Webhook", "PGDock-Signature"} {
			fresh.Headers[k] = h.Header.Get(k)
		}
		path := filepath.Join(fixtureDir, n.file)
		if write {
			if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
				t.Fatal(err)
			}
			b, _ := json.MarshalIndent(fresh, "", "  ")
			if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil { //nolint:gosec // published documentation
				t.Fatal(err)
			}
			continue
		}
		raw, err := os.ReadFile(path) //nolint:gosec // a fixed path in the repository
		if err != nil {
			t.Fatalf("%s: %v (PGDOCK_WRITE_FIXTURES=1 writes it)", path, err)
		}
		var pub webhookFixture
		if err := json.Unmarshal(raw, &pub); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		// The sample's signature verifies with its secret, at its own time.
		sig := pub.Headers["PGDock-Signature"]
		ts, _, _ := strings.Cut(strings.TrimPrefix(sig, "t="), ",")
		unix, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			t.Fatalf("%s: signature %q", path, sig)
		}
		if err := outbound.Verify(pub.Secret, sig, []byte(pub.Body), time.Unix(unix, 0), time.Minute); err != nil {
			t.Fatalf("%s doesn't verify: %v", path, err)
		}
		// And it has the fields a delivery has today, no more and no fewer.
		if a, b := topKeys(t, pub.Body), topKeys(t, fresh.Body); !slices.Equal(a, b) {
			t.Fatalf("%s is out of date: fields %v, a delivery today has %v (PGDOCK_WRITE_FIXTURES=1 rewrites it)", path, a, b)
		}
		if a, b := slices.Sorted(maps.Keys(pub.Headers)), slices.Sorted(maps.Keys(fresh.Headers)); !slices.Equal(a, b) {
			t.Fatalf("%s's headers: %v, want %v", path, a, b)
		}
	}
}

func topKeys(t *testing.T, body string) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(m))
}
