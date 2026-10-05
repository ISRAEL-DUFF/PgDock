package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
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
