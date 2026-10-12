package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/edge"
	"github.com/israel-duff/pgdock/internal/statusapi"
	"github.com/israel-duff/pgdock/internal/statuspage"
	"github.com/israel-duff/pgdock/test/testenv"
)

// startStatusPageV41 runs pgdock-status with V4.1's components: billing,
// and backend services per region (eu-central and lagos), and email
// through smtp when given.
func startStatusPageV41(t *testing.T, smtp *testenv.SMTPServer) (*statuspage.Service, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	cfg := `public_url = "https://status.pgdock.test"
push_secret = "` + statusSecret + `"
data = "` + filepath.Join(dir, "status.db") + `"
`
	if smtp != nil {
		host, port, _ := strings.Cut(smtp.Addr, ":")
		cfg += "[smtp]\nhost = \"" + host + "\"\nport = " + port + "\ntls = \"none\"\nfrom = \"status@pgdock.test\"\n"
	}
	for _, id := range []string{"dashboard", "edge-pooler", "shared-tier"} {
		cfg += "[[component]]\nid = \"" + id + "\"\n  [[component.probe]]\n  kind = \"tcp\"\n  addr = \"127.0.0.1:1\"\n"
	}
	for _, id := range []string{"dedicated", "backups", "webhooks-jobs", "billing"} {
		cfg += "[[component]]\nid = \"" + id + "\"\nheartbeat = true\n"
	}
	for _, r := range []string{"eu-central", "lagos"} {
		cfg += "[[component]]\nid = \"backend-services-" + r + "\"\nregion = \"" + r + "\"\nheartbeat = true\nheartbeat_id = \"backend-services\"\n"
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

func managedEmails(t *testing.T, svc *statuspage.Service) []string {
	t.Helper()
	list, err := svc.Managed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range list {
		out = append(out, m.Email)
	}
	return out
}

// TestStatusSubscriptions is V4.1-M6's subscriptions done-when (V4.1
// §7.1): a Pro organisation's billing contact is on the status page's
// subscriber list after a sync and gets an incident email; an unsubscribe
// survives the next sync; a Free organisation's owner isn't added.
func TestStatusSubscriptions(t *testing.T) {
	mail := testenv.StartSMTP(t)
	status, ts := startStatusPageV41(t, mail)
	e := testenv.Start(t, testenv.Options{StatusURL: ts.URL, StatusSecret: statusSecret})
	ctx := context.Background()

	// A Pro organisation (the owner's) with a project and two contacts.
	e.CreateProject("Shop")
	var orgs gen.OrgList
	e.Do("GET", "/api/v1/orgs", nil, &orgs)
	org := orgs.Items[0].Id.String()
	if code := e.Do("GET", "/api/v1/orgs/"+org+"/billing", nil, nil); code != http.StatusOK {
		t.Fatalf("billing account: %d", code)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE billing_accounts SET plan = 'pro' WHERE org_id = $1`, orgs.Items[0].Id); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"ap@shop.example", "cfo@shop.example"} {
		if code := e.Do("POST", "/api/v1/orgs/"+org+"/billing/contacts", gen.BillingContact{Email: c}, nil); code != http.StatusCreated {
			t.Fatalf("add contact %s: %d", c, code)
		}
	}
	var contact gen.BillingContact
	if code := e.Do("PATCH", "/api/v1/orgs/"+org+"/billing/contacts/cfo@shop.example", map[string]bool{"status_emails": false}, &contact); code != http.StatusOK ||
		contact.StatusEmails == nil || *contact.StatusEmails {
		t.Fatalf("turn off a contact's status emails: %d %+v", code, contact)
	}
	// A Free organisation's owner, with a project.
	bob := e.InviteUser("bob@hobby.example")
	bob.CreateProject("Hobby", bob.OrgID)

	if err := e.Incidents.SyncSubscribers(ctx); err != nil {
		t.Fatal(err)
	}
	got := managedEmails(t, status)
	if !slices.Contains(got, "ap@shop.example") || !slices.Contains(got, strings.ToLower(testenv.OwnerEmail)) {
		t.Fatalf("managed subscribers: %v", got)
	}
	if slices.Contains(got, "bob@hobby.example") || slices.Contains(got, "cfo@shop.example") {
		t.Fatalf("a Free owner or a contact with status emails off was added: %v", got)
	}
	list, _ := status.Managed(ctx)
	for _, m := range list {
		if m.Email == "ap@shop.example" && (!slices.Contains(m.Components, "shared-tier") || !slices.Equal(m.Regions, []string{"eu-central"})) {
			t.Fatalf("the contact's components and regions: %+v", m)
		}
	}

	// An incident on the shared tier in eu-central reaches the contact;
	// one in lagos doesn't.
	post := func(title, region string) {
		var inc gen.Incident
		if code := e.Do("POST", "/api/v1/incidents", gen.CreateIncidentRequest{Title: title, Components: []string{"shared-tier"}, Region: &region,
			Severity: gen.IncidentSeverityMajor, Status: gen.IncidentStatusInvestigating, Body: "Looking into it."}, &inc); code != http.StatusCreated {
			t.Fatalf("incident %q: %d", title, code)
		}
	}
	post("Shared tier is slow", "eu-central")
	post("Lagos is slow", "lagos")
	if err := e.Incidents.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if err := status.DeliverMail(ctx); err != nil {
		t.Fatal(err)
	}
	if mail.Count("ap@shop.example", "Shared tier is slow") != 1 {
		t.Fatal("the contact didn't get the incident email")
	}
	if mail.Count("ap@shop.example", "Lagos is slow") != 0 || mail.Count("bob@hobby.example", "slow") != 0 {
		t.Fatal("an incident outside the contact's region, or a Free owner, was mailed")
	}

	// Unsubscribing through the email's link survives the next sync.
	var token string
	for _, m := range mail.Mail() {
		if slices.Contains(m.To, "ap@shop.example") {
			if mt := regexp.MustCompile(`unsubscribe\?token=([0-9a-f]+)`).FindStringSubmatch(m.Data); mt != nil {
				token = mt[1]
			}
		}
	}
	if token == "" {
		t.Fatal("no unsubscribe link in the email")
	}
	resp, err := http.PostForm(ts.URL+"/unsubscribe", map[string][]string{"token": {token}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unsubscribe: %d", resp.StatusCode)
	}
	if err := e.Incidents.SyncSubscribers(ctx); err != nil {
		t.Fatal(err)
	}
	if got := managedEmails(t, status); slices.Contains(got, "ap@shop.example") || !slices.Contains(got, strings.ToLower(testenv.OwnerEmail)) {
		t.Fatalf("after unsubscribing and a sync: %v", got)
	}
}

// TestStatusComponents is V4.1-M6's components done-when (V4.1 §7.2): a
// stopped edge marks backend services degraded in its region only, and a
// payment provider outage marks billing degraded.
func TestStatusComponents(t *testing.T) {
	status, ts := startStatusPageV41(t, nil)
	e := testenv.Start(t, testenv.Options{StatusURL: ts.URL, StatusSecret: statusSecret})
	e.Incidents.SetEdgeSilentAfter(3 * time.Second)
	ctx := context.Background()
	edgeIn := func(name, region string) *testenv.Edge {
		return e.StartEdge(func(c *edge.Config) {
			c.Name, c.Region, c.ReportEvery, c.AliveEvery = name, region, 200*time.Millisecond, 500*time.Millisecond
		})
	}
	edgeIn("edge-eu-1", "eu-central")
	stopped := edgeIn("edge-eu-2", "eu-central")
	edgeIn("edge-lagos-1", "lagos")
	waitFor(t, 20*time.Second, "three edges reporting", func() bool {
		var n int
		_ = e.DB.QueryRow(ctx, `SELECT count(*) FROM edges`).Scan(&n)
		return n == 3
	})

	state := func(id string) string {
		t.Helper()
		if err := e.Incidents.Heartbeat(ctx); err != nil {
			t.Fatal(err)
		}
		if err := status.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		var st struct {
			Components []struct{ ID, Status, Detail string } `json:"components"`
		}
		statusOf(t, ts.URL, "/api/v1/status", &st)
		for _, c := range st.Components {
			if c.ID == id {
				return c.Status
			}
		}
		return ""
	}
	for _, id := range []string{"backend-services-eu-central", "backend-services-lagos", "billing"} {
		if s := state(id); s != statusapi.Operational {
			t.Fatalf("%s with every edge up: %s", id, s)
		}
	}

	stopped.Stop()
	time.Sleep(4 * time.Second)
	if s := state("backend-services-eu-central"); s != statusapi.Degraded {
		t.Fatalf("eu-central with one of two edges stopped: %s", s)
	}
	if s := state("backend-services-lagos"); s != statusapi.Operational {
		t.Fatalf("lagos with its edge up: %s", s)
	}

	// A provider outage on the last automatic charge: billing degraded.
	org := e.CreateOrg("Payer")
	if _, err := e.DB.Exec(ctx, `INSERT INTO payment_intents (org_id, reference, provider, channel, purpose, amount_minor, status, automatic, error)
VALUES ($1, 'pgd-outage-'||$2, 'flutterwave', 'saved_card', 'topup', 1000, 'failed', true, 'provider unavailable: timeout')`, org, strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
		t.Fatal(err)
	}
	if s := state("billing"); s != statusapi.Degraded {
		t.Fatalf("billing during a provider outage: %s", s)
	}
}
