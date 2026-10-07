package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/tenancy"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestBillingAccountsAndRoles covers who sees billing (V3 §3.2), plan
// changes with proration through the API, and price book publishing.
func TestBillingAccountsAndRoles(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	var org gen.Org
	if code := e.Do("POST", "/api/v1/orgs", map[string]string{"name": "Acme"}, &org); code != http.StatusCreated {
		t.Fatalf("create org: %d", code)
	}
	oid := org.Id.String()
	type doer interface {
		Do(method, path string, body, out any) int
	}
	invite := func(by doer, email, role string, want int) *testenv.Client {
		t.Helper()
		if code := by.Do("POST", "/api/v1/orgs/"+oid+"/members", map[string]string{"email": email, "role": role}, nil); code != want {
			t.Fatalf("invite %s as %s: %d, want %d", email, role, code, want)
		}
		if want != http.StatusCreated {
			return nil
		}
		return e.AcceptInvitation(e.MailToken(email, "invitation"), email)
	}
	admin := invite(e, "ada@example.com", "admin", http.StatusCreated)
	invite(admin, "mallory@example.com", "billing", http.StatusForbidden) // admins can't hand out billing
	fin := invite(e, "fin@example.com", "billing", http.StatusCreated)
	p := admin.CreateProject("Acme app", org.Id)

	// Owners and billing members see billing; admins don't.
	var acct gen.BillingAccount
	if code := fin.Do("GET", "/api/v1/orgs/"+oid+"/billing", nil, &acct); code != http.StatusOK || acct.Plan != "free" || len(acct.Plans) != 3 {
		t.Fatalf("billing member reads billing: %d %+v", code, acct)
	}
	if code := admin.Do("GET", "/api/v1/orgs/"+oid+"/billing", nil, nil); code != http.StatusForbidden {
		t.Errorf("admin reads billing: %d, want 403", code)
	}
	// The billing member sees the organisation but none of its projects.
	var list gen.ProjectList
	if code := fin.Do("GET", "/api/v1/projects?org="+oid, nil, &list); code != http.StatusOK || len(list.Items) != 0 {
		t.Errorf("billing member's projects: %d %d", code, len(list.Items))
	}
	if code := fin.Do("GET", "/api/v1/projects/"+p.Project.Id.String(), nil, nil); code != http.StatusNotFound {
		t.Errorf("billing member reads a project: %d, want 404", code)
	}
	if code := fin.Do("POST", "/api/v1/projects", map[string]any{"name": "Nope", "org_id": org.Id}, nil); code != http.StatusForbidden {
		t.Errorf("billing member creates a project: %d, want 403", code)
	}

	// Business details and contacts.
	legal, tin := "Acme Nigeria Ltd", "12345678-0001"
	if code := fin.Do("PATCH", "/api/v1/orgs/"+oid+"/billing", gen.BillingDetailsUpdate{LegalName: &legal, Tin: &tin, VatRegistered: true, DeductsWht: true}, &acct); code != http.StatusOK || !acct.DeductsWht || *acct.LegalName != legal {
		t.Fatalf("details: %d %+v", code, acct)
	}
	if code := fin.Do("POST", "/api/v1/orgs/"+oid+"/billing/contacts", gen.BillingContact{Email: "ap@acme.example"}, nil); code != http.StatusCreated {
		t.Fatalf("add contact: %d", code)
	}
	var contacts struct{ Items []gen.BillingContact }
	if fin.Do("GET", "/api/v1/orgs/"+oid+"/billing/contacts", nil, &contacts); len(contacts.Items) != 1 {
		t.Fatalf("contacts: %+v", contacts)
	}

	// Free → Pro: priced first, then applied, with the org's limits.
	yes := true
	var change gen.PlanChange
	if code := fin.Do("POST", "/api/v1/orgs/"+oid+"/billing/plan", gen.PlanChangeRequest{Plan: "pro", DryRun: &yes}, &change); code != http.StatusOK || !change.Upgrade || change.TotalMinor <= 0 {
		t.Fatalf("dry run: %d %+v", code, change)
	}
	if code := fin.Do("POST", "/api/v1/orgs/"+oid+"/billing/plan", gen.PlanChangeRequest{Plan: "pro"}, &change); code != http.StatusOK || !change.Applied || len(change.Lines) != 1 {
		t.Fatalf("upgrade: %d %+v", code, change)
	}
	var quotas gen.OrgQuotas
	if e.Do("GET", "/api/v1/orgs/"+oid+"/quotas", nil, &quotas); quotas.Plan != "Pro" {
		t.Errorf("quota plan after the upgrade: %s", quotas.Plan)
	}
	// Pro → Free waits for the next month.
	if code := fin.Do("POST", "/api/v1/orgs/"+oid+"/billing/plan", gen.PlanChangeRequest{Plan: "free"}, &change); code != http.StatusOK || change.Applied {
		t.Fatalf("downgrade: %d %+v", code, change)
	}
	if fin.Do("GET", "/api/v1/orgs/"+oid+"/billing", nil, &acct); acct.Plan != "pro" || acct.PendingChange == nil || acct.PendingChange.ToPlan != "free" {
		t.Errorf("after scheduling the downgrade: %+v %+v", acct.Plan, acct.PendingChange)
	}

	// This month's invoice: drafted by the admin, hidden from the org until
	// issued, then numbered, emailed, posted, and downloadable.
	period := time.Now().UTC().Format("2006-01")
	var drafted struct{ Drafts int }
	if code := e.Do("POST", "/api/v1/admin/invoices/draft", map[string]any{"period": period, "org_id": org.Id}, &drafted); code != http.StatusOK || drafted.Drafts != 1 {
		t.Fatalf("draft: %d %+v", code, drafted)
	}
	var invs gen.InvoiceList
	if e.Do("GET", "/api/v1/admin/invoices?status=draft&period="+period, nil, &invs); len(invs.Items) != 1 || invs.Items[0].OrgName == nil || *invs.Items[0].OrgName != "Acme" {
		t.Fatalf("admin drafts: %+v", invs)
	}
	inv := invs.Items[0]
	if fin.Do("GET", "/api/v1/orgs/"+oid+"/billing/invoices", nil, &invs); len(invs.Items) != 0 {
		t.Errorf("the org sees a draft: %+v", invs.Items)
	}
	if code := fin.Do("GET", "/api/v1/orgs/"+oid+"/billing/invoices/"+inv.Id.String(), nil, nil); code != http.StatusNotFound {
		t.Errorf("the org reads a draft: %d", code)
	}
	reason := "checking the storage numbers"
	var held gen.Invoice
	if code := e.Do("POST", "/api/v1/admin/invoices/"+inv.Id.String()+"/hold", map[string]any{"held": true, "reason": reason}, &held); code != http.StatusOK || !held.Held {
		t.Fatalf("hold: %d %+v", code, held)
	}
	var issued gen.Invoice
	if code := e.Do("POST", "/api/v1/admin/invoices/"+inv.Id.String()+"/issue", nil, &issued); code != http.StatusOK || issued.Number == nil || issued.Status != gen.InvoiceStatusIssued {
		t.Fatalf("issue: %d %+v", code, issued)
	}
	if code := e.Do("POST", "/api/v1/admin/invoices/"+inv.Id.String()+"/issue", nil, nil); code != http.StatusConflict {
		t.Errorf("issue twice: %d", code)
	}
	var detail gen.InvoiceDetail
	if code := fin.Do("GET", "/api/v1/orgs/"+oid+"/billing/invoices/"+inv.Id.String(), nil, &detail); code != http.StatusOK || len(detail.Lines) == 0 || detail.Invoice.TotalMinor != issued.TotalMinor {
		t.Fatalf("org reads the invoice: %d %+v", code, detail)
	}
	if code, body := fin.DoRaw("GET", "/api/v1/orgs/"+oid+"/billing/invoices/"+inv.Id.String()+"/pdf", nil); code != http.StatusOK || !strings.HasPrefix(string(body), "%PDF") {
		t.Errorf("invoice PDF: %d %.20q", code, body)
	}
	if code := admin.Do("GET", "/api/v1/orgs/"+oid+"/billing/invoices", nil, nil); code != http.StatusForbidden {
		t.Errorf("an admin lists invoices: %d", code)
	}
	if n := e.SMTP.Count("ap@acme.example", *issued.Number); n != 1 {
		t.Errorf("invoice emails to the contact: %d", n)
	}
	var cn gen.CreditNote
	if code := e.Do("POST", "/api/v1/admin/invoices/"+inv.Id.String()+"/credit-notes", map[string]any{"amount_minor": 1000, "reason": "goodwill"}, &cn); code != http.StatusCreated || cn.VatMinor != 75 {
		t.Fatalf("credit note: %d %+v", code, cn)
	}
	var check gen.LedgerCheck
	if code := e.Do("GET", "/api/v1/admin/ledger/check", nil, &check); code != http.StatusOK || !check.Balanced || check.Transactions != 2 || check.DebitsMinor != check.CreditsMinor {
		t.Fatalf("ledger check: %d %+v", code, check)
	}

	// At the spend cap, a new branch is refused with the reason; reading
	// and the cost estimate still work.
	if _, err := e.DB.Exec(context.Background(), `UPDATE billing_accounts SET capped = true WHERE org_id = $1`, org.Id); err != nil {
		t.Fatal(err)
	}
	live := gen.BranchRequestSourceLive
	var refused gen.Error
	if code := admin.Do("POST", "/api/v1/projects/"+p.Project.Id.String()+"/branches", gen.BranchRequest{Name: "capped", Source: &live}, &refused); code != http.StatusConflict ||
		!strings.Contains(refused.Message, "spend cap") {
		t.Errorf("branch at the spend cap: %d %+v", code, refused)
	}
	var est struct {
		MonthlyMinor int64 `json:"monthly_minor"`
	}
	if code := admin.Do("POST", "/api/v1/orgs/"+oid+"/billing/estimate", map[string]any{"cpus": 2, "memory_mb": 4096, "disk_gb": 40}, &est); code != http.StatusOK ||
		est.MonthlyMinor != billing.D("9655.76").Frac(730, 1).Round() {
		t.Errorf("estimate: %d %+v", code, est)
	}
	if _, err := e.DB.Exec(context.Background(), `UPDATE billing_accounts SET capped = false WHERE org_id = $1`, org.Id); err != nil {
		t.Fatal(err)
	}
	// Restricted for an overdue balance (dunning day 3): no new projects.
	if _, err := e.DB.Exec(context.Background(), `UPDATE billing_accounts SET dunning_state = 'restricted' WHERE org_id = $1`, org.Id); err != nil {
		t.Fatal(err)
	}
	if code := admin.Do("POST", "/api/v1/projects", map[string]any{"name": "Blocked", "org_id": org.Id}, &refused); code != http.StatusConflict || !strings.Contains(refused.Message, "overdue") {
		t.Errorf("a project while restricted: %d %+v", code, refused)
	}
	if _, err := e.DB.Exec(context.Background(), `UPDATE billing_accounts SET dunning_state = 'ok' WHERE org_id = $1`, org.Id); err != nil {
		t.Fatal(err)
	}

	// Price books: a draft can't be published inside the notice period.
	var books struct {
		CurrentVersion int `json:"current_version"`
		Items          []gen.PriceBook
	}
	if code := e.Do("GET", "/api/v1/admin/price-books", nil, &books); code != http.StatusOK || books.CurrentVersion != 1 || len(books.Items) != 1 {
		t.Fatalf("price books: %d %+v", code, books)
	}
	prices := books.Items[0].Prices
	pro := prices.Plans["pro"]
	pro.MonthlyMinor = 2_000_000
	prices.Plans["pro"] = pro
	var draft gen.PriceBook
	if code := e.Do("POST", "/api/v1/admin/price-books", gen.PriceBookInput{EffectiveAt: time.Now().Add(7 * 24 * time.Hour), Prices: prices}, &draft); code != http.StatusCreated || draft.Version != 2 {
		t.Fatalf("draft: %d %+v", code, draft)
	}
	path := "/api/v1/admin/price-books/2"
	var apiErr gen.Error
	if code := e.Do("POST", path+"/publish", nil, &apiErr); code != http.StatusBadRequest || !strings.Contains(apiErr.Message, "30 days") {
		t.Fatalf("early publish: %d %+v", code, apiErr)
	}
	bad := prices
	bad.Currency = "USD"
	if code := e.Do("PUT", path, gen.PriceBookInput{EffectiveAt: time.Now().Add(40 * 24 * time.Hour), Prices: bad}, nil); code != http.StatusBadRequest {
		t.Errorf("invalid prices: %d", code)
	}
	if code := e.Do("PUT", path, gen.PriceBookInput{EffectiveAt: time.Now().Add(40 * 24 * time.Hour), Prices: prices}, nil); code != http.StatusOK {
		t.Fatalf("move the date: %d", code)
	}
	var pub struct {
		PriceBook gen.PriceBook `json:"price_book"`
		Notified  int
	}
	// Keeping Pro cancels the scheduled downgrade.
	if code := fin.Do("POST", "/api/v1/orgs/"+oid+"/billing/plan", gen.PlanChangeRequest{Plan: "pro"}, nil); code != http.StatusOK {
		t.Fatalf("keep pro: %d", code)
	}
	acct = gen.BillingAccount{}
	if fin.Do("GET", "/api/v1/orgs/"+oid+"/billing", nil, &acct); acct.PendingChange != nil {
		t.Errorf("the downgrade is still scheduled: %+v", acct.PendingChange)
	}
	var preview struct {
		Items []struct {
			OrgName        string `json:"org_name"`
			CurrentMinor   int64  `json:"current_minor"`
			ProjectedMinor int64  `json:"projected_minor"`
		}
	}
	if code := e.Do("POST", path+"/preview", map[string]string{"period": period}, &preview); code != http.StatusOK || len(preview.Items) != 1 ||
		preview.Items[0].ProjectedMinor-preview.Items[0].CurrentMinor != 500_000 {
		// Pro goes from ₦15,000 to ₦20,000 a month: the org's next fee in advance.
		t.Errorf("preview: %d %+v", code, preview)
	}
	if code := e.Do("POST", path+"/publish", nil, &pub); code != http.StatusOK || pub.PriceBook.PublishedAt == nil || pub.Notified != 1 {
		t.Fatalf("publish: %d %+v", code, pub)
	}
	if n := e.SMTP.Count("ap@acme.example", "NGN 20,000.00"); n != 1 {
		t.Errorf("repricing notices to the billing contact: %d", n)
	}
	if code := e.Do("PUT", path, gen.PriceBookInput{EffectiveAt: time.Now().Add(40 * 24 * time.Hour), Prices: prices}, nil); code != http.StatusConflict {
		t.Errorf("edit a published book: %d", code)
	}

	// The admin's per-org settings.
	if code := e.Do("PATCH", "/api/v1/admin/orgs/"+oid+"/billing", gen.AdminBillingUpdate{Grandfathered: true, Mode: "postpaid", PaymentTermsDays: 30, PriceBookVersion: 1}, &acct); code != http.StatusOK || !acct.Grandfathered || acct.PaymentTermsDays != 30 {
		t.Fatalf("admin update: %d %+v", code, acct)
	}
	var seen gen.BillingAccount
	if code := e.Do("GET", "/api/v1/admin/orgs/"+oid+"/billing", nil, &seen); code != http.StatusOK || !seen.Grandfathered || seen.PaymentTermsDays != 30 {
		t.Errorf("admin reads the org's billing: %d %+v", code, seen)
	}
	if code := fin.Do("GET", "/api/v1/admin/orgs/"+oid+"/billing", nil, nil); code != http.StatusForbidden {
		t.Errorf("a billing member reads the admin view: %d", code)
	}
	if code := e.Do("PUT", "/api/v1/admin/billing/settings", map[string]any{
		"vat_rate": "0.075", "wht_rate": "0.05", "auto_issue": true,
		"seller": map[string]string{"legal_name": "PGDock Ltd", "address": "Lagos", "tin": "999", "vat_number": "VAT-1", "email": "billing@pgdock.test"},
	}, nil); code != http.StatusOK {
		t.Fatalf("billing settings: %d", code)
	}
	if code := e.Do("PUT", "/api/v1/admin/billing/settings", map[string]any{
		"vat_rate": "7.5", "wht_rate": "0.05", "auto_issue": true, "seller": map[string]string{"legal_name": "PGDock Ltd"},
	}, nil); code != http.StatusBadRequest {
		t.Errorf("a VAT rate of 7.5 (750%%): %d", code)
	}
}

// TestCLIBilling drives billing from the pgdock binary with an admin token.
func TestCLIBilling(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	bin := buildCLI(t)
	token := e.CreateToken(map[string]any{"name": "finance", "org_id": e.OrgID, "scopes": []string{"read", "write", "admin"}})
	env := []string{"PGDOCK_SERVER=" + e.URL, "PGDOCK_TOKEN=" + token, "PGDOCK_CONFIG_DIR=" + t.TempDir()}

	if r := runCLI(t, bin, env, "billing", "show"); r.code != 0 || !strings.Contains(r.stdout, "Plan: Free (monthly)") {
		t.Fatalf("billing show: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if r := runCLI(t, bin, env, "billing", "plan", "pro", "--dry-run"); r.code != 0 || !strings.Contains(r.stdout, "would take effect") || !strings.Contains(r.stdout, "+ VAT") {
		t.Fatalf("dry run: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if r := runCLI(t, bin, env, "billing", "plan", "pro"); r.code != 0 || !strings.Contains(r.stdout, "Now on pro (monthly)") {
		t.Fatalf("plan: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	var drafted struct{ Drafts int }
	e.Do("POST", "/api/v1/admin/invoices/draft", map[string]any{"period": time.Now().UTC().Format("2006-01"), "org_id": e.OrgID}, &drafted)
	var invs gen.InvoiceList
	e.Do("GET", "/api/v1/admin/invoices?status=draft", nil, &invs)
	if len(invs.Items) != 1 {
		t.Fatalf("drafts: %+v", invs)
	}
	var issued gen.Invoice
	if code := e.Do("POST", "/api/v1/admin/invoices/"+invs.Items[0].Id.String()+"/issue", nil, &issued); code != http.StatusOK {
		t.Fatalf("issue: %d", code)
	}
	if r := runCLI(t, bin, env, "billing", "invoices"); r.code != 0 || !strings.Contains(r.stdout, *issued.Number) {
		t.Fatalf("invoices: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	pdf := filepath.Join(t.TempDir(), "invoice.pdf")
	if r := runCLI(t, bin, env, "billing", "invoice", *issued.Number, "--pdf", pdf); r.code != 0 {
		t.Fatalf("download: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if b, err := os.ReadFile(pdf); err != nil || !strings.HasPrefix(string(b), "%PDF") {
		t.Fatalf("the PDF: %v %.10q", err, b)
	}
	if r := runCLI(t, bin, env, "billing", "invoice", *issued.Number); r.code != 0 || !strings.Contains(r.stdout, "Pro (monthly)") {
		t.Fatalf("invoice: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}

	// Paying: a checkout link, then a transfer to the org's own account.
	if r := runCLI(t, bin, env, "billing", "pay", *issued.Number); r.code != 0 || !strings.Contains(r.stdout, "http") || !strings.Contains(r.stdout, ngn(issued.TotalMinor)) {
		t.Fatalf("pay: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	r := runCLI(t, bin, env, "billing", "transfer")
	fields := strings.Fields(strings.TrimPrefix(r.stdout, "Transfer to "))
	if r.code != 0 || len(fields) == 0 {
		t.Fatalf("transfer: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if _, err := e.ISpend.Transfer(strings.TrimSuffix(fields[0], ","), issued.TotalMinor); err != nil {
		t.Fatal(err)
	}
	awaitPay(t, "the transfer", func() bool { return invoiceStatus(e, issued.Id) == "paid" })
	if r := runCLI(t, bin, env, "billing", "payments"); r.code != 0 || !strings.Contains(r.stdout, ngn(issued.TotalMinor)) || !strings.Contains(r.stdout, "transfer") {
		t.Fatalf("payments: exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
}

// TestMonthOfRealUsageInvoices is M20's done-when: a month of V2 usage,
// recorded by the V2 recorder from projects' measured sizes and a running
// dedicated instance, produces correct, balanced invoices for two
// organisations, one of which upgrades mid-month; and the ledger invariant
// holds.
func TestMonthOfRealUsageInvoices(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "both")
	ctx := context.Background()

	// Org A: a shared project and a small dedicated instance (1 vCPU,
	// 1,024 MB, 5 GB). Org B: a shared project of 15 GB.
	shop := e.CreateProject("Shop")
	tier, profile, vol := gen.ProjectTierDedicated, "small", 5
	var ded gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Shop Pro", Tier: &tier, Profile: &profile, VolumeGb: &vol}, &ded); code != http.StatusAccepted {
		t.Fatalf("create dedicated: %d", code)
	}
	if op := e.WaitOperation(ded.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create dedicated: %s %s", op.Status, deref(op.Error))
	}
	orgB := e.CreateOrg("Bakery")
	bakes := e.CreateProjectIn("Bakes", orgB)
	orgA := e.OrgID

	// Last month, as the V2 recorder sees it: the projects existed all
	// month, and the shared ones measured 2 GB and 15 GB every hour.
	now := time.Now().UTC()
	month := billing.MonthStart(now).AddDate(0, -1, 0)
	next := month.AddDate(0, 1, 0)
	n := int64(next.Sub(month).Hours() / 24) // days in the month
	for _, id := range []uuid.UUID{shop.Project.Id, ded.Project.Id, bakes.Project.Id} {
		if _, err := e.DB.Exec(ctx, `UPDATE projects SET created_at = $2 WHERE id = $1`, id, month.Add(-24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for id, bytes := range map[uuid.UUID]float64{shop.Project.Id: 2e9, bakes.Project.Id: 15e9} {
		if _, err := e.DB.Exec(ctx, `
			INSERT INTO metric_points (scope, scope_id, metric, ts, resolution, value)
			SELECT 'project', $1, 'size_bytes', h, '1h', $4
			FROM generate_series($2::timestamptz, $3::timestamptz - interval '1 hour', interval '1 hour') AS h
			ON CONFLICT DO NOTHING`, id, month, next, bytes); err != nil {
			t.Fatal(err)
		}
	}
	wm, _ := json.Marshal(map[string]time.Time{"hour": month, "day": month})
	if _, err := e.DB.Exec(ctx, `INSERT INTO settings (key, value) VALUES ('usage.watermark', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, wm); err != nil {
		t.Fatal(err)
	}
	if err := e.Tenancy.RecordUsage(ctx); err != nil {
		t.Fatal(err)
	}
	usageSum := func(org uuid.UUID, metric string, from, to time.Time) billing.Dec {
		var s string
		if err := e.DB.QueryRow(ctx, `SELECT coalesce(sum(quantity), 0)::text FROM usage_records
			WHERE org_id = $1 AND metric = $2 AND period_start >= $3 AND period_start < $4`, org, metric, from, to).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return billing.D(s)
	}
	hours := billing.DecInt(24 * n)
	if got := usageSum(orgA, tenancy.MetricDedicatedCPU, month, next); got.Cmp(hours) != 0 {
		t.Fatalf("recorded %s vCPU-hours, want %s (1 vCPU all month)", got, hours)
	}
	if got := usageSum(orgB, tenancy.MetricSharedStorage, month, next); got.Cmp(hours.Mul(billing.D("15"))) != 0 {
		t.Fatalf("recorded %s GB-hours for Bakes, want %s", got, hours.Mul(billing.D("15")))
	}

	// Plans: both on Pro from the 1st; A upgrades to Team on the 13th.
	at := func(t time.Time) { e.Billing.Now = func() time.Time { return t } }
	at(month)
	for _, org := range []uuid.UUID{orgA, orgB} {
		if _, err := e.Billing.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
			t.Fatal(err)
		}
	}
	at(month.AddDate(0, 0, 12).Add(15 * time.Hour))
	if _, err := e.Billing.ChangePlan(ctx, orgA, billing.PlanRequest{Plan: billing.PlanTeam}); err != nil {
		t.Fatal(err)
	}
	at(now)
	defer func() { e.Billing.Now = time.Now }()

	if _, err := e.Billing.DraftAll(ctx, month); err != nil {
		t.Fatal(err)
	}
	var drafts gen.InvoiceList
	e.Do("GET", "/api/v1/admin/invoices?status=draft&period="+month.Format("2006-01"), nil, &drafts)
	if len(drafts.Items) != 2 {
		t.Fatalf("%d drafts for %s, want 2", len(drafts.Items), month.Format("2006-01"))
	}
	for _, d := range drafts.Items {
		if code := e.Do("POST", "/api/v1/admin/invoices/"+d.Id.String()+"/issue", nil, nil); code != http.StatusOK {
			t.Fatalf("issue: %d", code)
		}
	}

	// What each invoice should say, from the usage records and the
	// default price book, worked out here independently of the rater.
	rd := func(d billing.Dec) int64 { return d.Round() }
	frac := func(fee, num int64) int64 { return billing.DecInt(fee).Frac(num, n).Round() }
	left := n - 12
	wantA := map[string]int64{
		"Pro (monthly), " + fmt.Sprintf("%d of %d days", n, n):      1_500_000,
		fmt.Sprintf("Unused Pro (monthly), %d of %d days", left, n): -frac(1_500_000, left),
		fmt.Sprintf("Team (monthly), %d of %d days", left, n):       frac(6_000_000, left),
		"Dedicated Shop Pro: vCPU-hours":                            rd(hours.Mul(billing.D("2740"))),
		"Dedicated Shop Pro: RAM GB-hours":                          rd(hours.Mul(billing.D("1.024")).Mul(billing.D("685"))),
		"Dedicated Shop Pro: Disk GB-hours":                         rd(hours.Mul(billing.D("5")).Mul(billing.D("34.25"))),
		"Team plan, " + next.Format("January 2006"):                 6_000_000,
	}
	// Bakes: 15 GB all month, 10 GB-months (7,300 GB-hours) included.
	over := hours.Mul(billing.D("15")).Sub(billing.D("7300"))
	wantB := map[string]int64{
		fmt.Sprintf("Pro (monthly), %d of %d days", n, n):           1_500_000,
		"Shared storage (GB-hours) above the Pro allowance of 7300": rd(over.Mul(billing.D("34.25"))),
		"Pro plan, " + next.Format("January 2006"):                  1_500_000,
	}
	for org, want := range map[uuid.UUID]map[string]int64{orgA: wantA, orgB: wantB} {
		var list gen.InvoiceList
		e.Do("GET", "/api/v1/orgs/"+org.String()+"/billing/invoices", nil, &list)
		if len(list.Items) != 1 {
			t.Fatalf("org %s: %d invoices", org, len(list.Items))
		}
		var d gen.InvoiceDetail
		e.Do("GET", "/api/v1/orgs/"+org.String()+"/billing/invoices/"+list.Items[0].Id.String(), nil, &d)
		var subtotal int64
		for _, l := range d.Lines {
			subtotal += l.Amount
		}
		if len(d.Lines) != len(want) {
			for _, l := range d.Lines {
				t.Logf("  %-70s %d", l.Description, l.Amount)
			}
			t.Fatalf("org %s: %d lines, want %d", org, len(d.Lines), len(want))
		}
		var wantSub int64
		for desc, amount := range want {
			wantSub += amount
			found := false
			for _, l := range d.Lines {
				if l.Description == desc {
					found = true
					if l.Amount != amount {
						t.Errorf("org %s: %q is %d, want %d", org, desc, l.Amount, amount)
					}
				}
			}
			if !found {
				t.Errorf("org %s: no line %q", org, desc)
			}
		}
		inv := d.Invoice
		vat := billing.DecInt(wantSub).Mul(billing.D("0.075")).Round()
		if subtotal != wantSub || inv.SubtotalMinor != wantSub || inv.VatMinor != vat || inv.TotalMinor != wantSub+vat {
			t.Errorf("org %s: subtotal %d (lines %d, want %d), VAT %d (want %d), total %d", org, inv.SubtotalMinor, subtotal, wantSub, inv.VatMinor, vat, inv.TotalMinor)
		}
		if b, err := billing.Balance(ctx, e.DB, &org, billing.AccReceivable); err != nil || b != inv.TotalMinor {
			t.Errorf("org %s: receivable %d, want the total %d (%v)", org, b, inv.TotalMinor, err)
		}
	}
	var check gen.LedgerCheck
	if e.Do("GET", "/api/v1/admin/ledger/check", nil, &check); !check.Balanced || check.Transactions != 2 {
		t.Fatalf("ledger: %+v", check)
	}
}

// ngn formats kobo the way the CLI does: 1500000 → "NGN 15,000.00".
func ngn(kobo int64) string {
	whole := fmt.Sprint(kobo / 100)
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return fmt.Sprintf("NGN %s.%02d", b.String(), kobo%100)
}
