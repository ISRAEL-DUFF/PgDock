package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// awaitPay polls cond until it holds (webhooks arrive asynchronously).
func awaitPay(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// payOrg is an org on Pro with an issued invoice for this month.
type payOrg struct {
	id  uuid.UUID
	inv gen.Invoice
}

func newPayOrg(t *testing.T, e *testenv.Env, name string, wht bool) payOrg {
	t.Helper()
	var org gen.Org
	if code := e.Do("POST", "/api/v1/orgs", map[string]string{"name": name}, &org); code != http.StatusCreated {
		t.Fatalf("create org: %d", code)
	}
	oid := org.Id.String()
	if wht {
		if code := e.Do("PATCH", "/api/v1/orgs/"+oid+"/billing", gen.BillingDetailsUpdate{DeductsWht: true}, nil); code != http.StatusOK {
			t.Fatalf("WHT: %d", code)
		}
	}
	if code := e.Do("POST", "/api/v1/orgs/"+oid+"/billing/contacts", gen.BillingContact{Email: "ap@" + strings.ToLower(strings.ReplaceAll(name, " ", "")) + ".example"}, nil); code != http.StatusCreated {
		t.Fatalf("contact: %d", code)
	}
	if code := e.Do("POST", "/api/v1/orgs/"+oid+"/billing/plan", gen.PlanChangeRequest{Plan: "pro"}, nil); code != http.StatusOK {
		t.Fatalf("pro: %d", code)
	}
	return payOrg{id: org.Id, inv: issueInvoice(t, e, org.Id)}
}

// issueInvoice drafts and issues this month's invoice for org.
func issueInvoice(t *testing.T, e *testenv.Env, org uuid.UUID) gen.Invoice {
	t.Helper()
	period := time.Now().UTC().Format("2006-01")
	var dr struct{ Drafts int }
	if code := e.Do("POST", "/api/v1/admin/invoices/draft", map[string]any{"period": period, "org_id": org}, &dr); code != http.StatusOK || dr.Drafts != 1 {
		t.Fatalf("draft: %d %+v", code, dr)
	}
	var list gen.InvoiceList
	e.Do("GET", "/api/v1/admin/invoices?status=draft&period="+period, nil, &list)
	for _, inv := range list.Items {
		if inv.OrgId == org {
			var issued gen.Invoice
			if code := e.Do("POST", "/api/v1/admin/invoices/"+inv.Id.String()+"/issue", nil, &issued); code != http.StatusOK {
				t.Fatalf("issue: %d", code)
			}
			return issued
		}
	}
	t.Fatal("no draft")
	return gen.Invoice{}
}

func invoiceStatus(e *testenv.Env, id uuid.UUID) string {
	var d gen.InvoiceDetail
	e.Do("GET", "/api/v1/admin/invoices/"+id.String(), nil, &d)
	return string(d.Invoice.Status)
}

// TestPaymentsAcrossProviders is M21's done-when, against fakes of the
// Flutterwave and iSpend sandboxes whose webhooks reach the API: card
// payments and tokenised charges, transfers into both kinds of virtual
// account (full, short by WHT, partial, over), wallet payments and mandate
// charges, a revoked mandate, a failed card, duplicate and missed
// webhooks, an iSpend outage falling back to a Flutterwave account, and
// reconciliation against both providers with zero differences.
func TestPaymentsAcrossProviders(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	q := store.New(e.DB)
	start := time.Now().Add(-time.Minute)

	// 1. Cards: pay an invoice on the hosted page; the card is saved.
	card := newPayOrg(t, e, "Card Co", false)
	cid := card.id.String()
	var co struct {
		Reference   string `json:"reference"`
		CheckoutURL string `json:"checkout_url"`
		AmountMinor int64  `json:"amount_minor"`
	}
	if code := e.Do("POST", "/api/v1/orgs/"+cid+"/billing/checkout", map[string]any{"channel": "card", "purpose": "invoice", "invoice_id": card.inv.Id}, &co); code != http.StatusCreated ||
		co.AmountMinor != card.inv.TotalMinor || !strings.Contains(co.CheckoutURL, co.Reference) {
		t.Fatalf("checkout: %d %+v", code, co)
	}
	if _, err := e.Flutterwave.CompleteCheckout(co.Reference, true); err != nil {
		t.Fatal(err)
	}
	awaitPay(t, "the card payment", func() bool { return invoiceStatus(e, card.inv.Id) == "paid" })
	// The card is saved just after the payment settles the invoice.
	var methods struct{ Items []gen.PaymentMethod }
	awaitPay(t, "the saved card", func() bool {
		e.Do("GET", "/api/v1/orgs/"+cid+"/billing/payment-methods", nil, &methods)
		return len(methods.Items) > 0
	})
	if len(methods.Items) != 1 || *methods.Items[0].Last4 != "4081" {
		t.Fatalf("saved card: %+v", methods)
	}
	// A duplicate webhook changes nothing.
	countPays := func(org uuid.UUID) int {
		var pays gen.PaymentList
		e.Do("GET", "/api/v1/orgs/"+org.String()+"/billing/payments", nil, &pays)
		return len(pays.Items)
	}
	e.Flutterwave.RedeliverLast()
	time.Sleep(500 * time.Millisecond)
	if n := countPays(card.id); n != 1 {
		t.Fatalf("payments after a duplicate webhook: %d", n)
	}
	// A tokenised charge, then a failed one.
	m, err := q.DefaultPaymentMethod(ctx, card.id)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := e.Billing.ChargeMethod(ctx, m, nil, 750_000); err != nil || !out.Succeeded {
		t.Fatalf("tokenised charge: %+v %v", out, err)
	}
	e.Flutterwave.Decline("flw-t1-"+co.Reference, "Do not honour")
	if out, err := e.Billing.ChargeMethod(ctx, m, nil, 750_000); err != nil || out.Succeeded || out.Unavailable {
		t.Fatalf("failed card: %+v %v", out, err)
	}

	// 2. Transfers into an iSpend account: short by WHT, then partial and over.
	wht := newPayOrg(t, e, "Tax Co", true)
	var va gen.VirtualAccount
	if code := e.Do("POST", "/api/v1/orgs/"+wht.id.String()+"/billing/virtual-account", nil, &va); code != http.StatusOK || va.Provider != "ispend" {
		t.Fatalf("iSpend account: %d %+v", code, va)
	}
	if _, err := e.ISpend.Transfer(va.AccountNumber, wht.inv.TotalMinor-wht.inv.WhtExpectedMinor); err != nil {
		t.Fatal(err)
	}
	awaitPay(t, "the WHT-short transfer", func() bool { return invoiceStatus(e, wht.inv.Id) == "paid_wht_pending" })
	pdf := []byte("%PDF-1.4\n%%EOF")
	if code, body := e.DoBytes("POST", "/api/v1/orgs/"+wht.id.String()+"/billing/invoices/"+wht.inv.Id.String()+"/wht-certificate?filename=wht.pdf", "application/octet-stream", pdf); code != http.StatusCreated {
		t.Fatalf("WHT credit note: %d %s", code, body)
	}
	if s := invoiceStatus(e, wht.inv.Id); s != "paid" {
		t.Errorf("after the credit note: %s", s)
	}

	part := newPayOrg(t, e, "Partial Co", false)
	var pva gen.VirtualAccount
	e.Do("POST", "/api/v1/orgs/"+part.id.String()+"/billing/virtual-account", nil, &pva)
	if _, err := e.ISpend.Transfer(pva.AccountNumber, part.inv.TotalMinor/2); err != nil {
		t.Fatal(err)
	}
	awaitPay(t, "the partial transfer", func() bool { return invoiceStatus(e, part.inv.Id) == "partially_paid" })
	// The rest and ₦1,000 more, whose webhook is lost: re-query finds it.
	e.ISpend.DropWebhooks(1)
	if _, err := e.ISpend.Transfer(pva.AccountNumber, part.inv.TotalMinor-part.inv.TotalMinor/2+100_000); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if s := invoiceStatus(e, part.inv.Id); s != "partially_paid" {
		t.Fatalf("a dropped webhook was posted: %s", s)
	}
	if n, err := e.Billing.Requery(ctx, start, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("re-query: %d %v", n, err)
	}
	if s := invoiceStatus(e, part.inv.Id); s != "paid" {
		t.Errorf("after the re-query: %s", s)
	}
	var acct gen.BillingAccount
	if e.Do("GET", "/api/v1/orgs/"+part.id.String()+"/billing", nil, &acct); acct.CreditMinor == nil || *acct.CreditMinor != 100_000 {
		t.Errorf("credit from the overpayment: %+v", acct.CreditMinor)
	}

	// 3. An iSpend outage: the account comes from Flutterwave and settles alike.
	fb := newPayOrg(t, e, "Fallback Co", false)
	e.ISpend.SetVirtualAccountsDown(true)
	var fva gen.VirtualAccount
	if code := e.Do("POST", "/api/v1/orgs/"+fb.id.String()+"/billing/virtual-account", nil, &fva); code != http.StatusOK || fva.Provider != "flutterwave" {
		t.Fatalf("fallback account: %d %+v", code, fva)
	}
	e.ISpend.SetVirtualAccountsDown(false)
	if _, err := e.Flutterwave.Transfer(fva.AccountNumber, fb.inv.TotalMinor); err != nil {
		t.Fatal(err)
	}
	awaitPay(t, "the transfer into the Flutterwave account", func() bool { return invoiceStatus(e, fb.inv.Id) == "paid" })

	// 4. Pay with iSpend with a mandate; a mandate charge; the mandate revoked.
	wal := newPayOrg(t, e, "Wallet Co", false)
	var wco struct {
		Reference   string `json:"reference"`
		CheckoutURL string `json:"checkout_url"`
	}
	if code := e.Do("POST", "/api/v1/orgs/"+wal.id.String()+"/billing/checkout", map[string]any{"channel": "wallet", "purpose": "invoice", "invoice_id": wal.inv.Id,
		"mandate_limit_minor": 10_000_000}, &wco); code != http.StatusCreated {
		t.Fatalf("wallet checkout: %d", code)
	}
	if _, err := e.ISpend.Approve(wco.CheckoutURL, 20_000_000); err != nil {
		t.Fatal(err)
	}
	awaitPay(t, "the wallet payment", func() bool { return invoiceStatus(e, wal.inv.Id) == "paid" })
	// The mandate is saved just after the payment settles the invoice.
	var mm store.PaymentMethod
	awaitPay(t, "the saved mandate", func() bool {
		mm, err = q.DefaultPaymentMethod(ctx, wal.id)
		return err == nil
	})
	if mm.Kind != "mandate" {
		t.Fatalf("mandate: %+v %v", mm, err)
	}
	if out, err := e.Billing.ChargeMethod(ctx, mm, nil, 1_000_000); err != nil || !out.Succeeded {
		t.Fatalf("mandate charge: %+v %v", out, err)
	}
	e.ISpend.RevokeMandate(*mm.ProviderRef)
	awaitPay(t, "the revocation", func() bool {
		var ms struct{ Items []gen.PaymentMethod }
		e.Do("GET", "/api/v1/orgs/"+wal.id.String()+"/billing/payment-methods", nil, &ms)
		return len(ms.Items) == 0
	})

	// 5. Forged webhooks are refused.
	if code, _ := e.DoBytes("POST", "/api/v1/payments/webhooks/flutterwave", "application/json", []byte(`{"event":"charge.completed","data":{"id":1}}`)); code != http.StatusUnauthorized {
		t.Errorf("unsigned Flutterwave webhook: %d", code)
	}

	// 6. Receipts, the ledger, and reconciliation against both providers.
	var pays gen.PaymentList
	e.Do("GET", "/api/v1/orgs/"+cid+"/billing/payments", nil, &pays)
	if len(pays.Items) < 2 {
		t.Fatalf("card payments: %+v", pays)
	}
	if code, body := e.DoBytes("GET", "/api/v1/orgs/"+cid+"/billing/payments/"+pays.Items[0].Id.String()+"/receipt", "", nil); code != http.StatusOK || !strings.HasPrefix(string(body), "%PDF") {
		t.Errorf("receipt: %d", code)
	}
	var check gen.LedgerCheck
	if e.Do("GET", "/api/v1/admin/ledger/check", nil, &check); !check.Balanced {
		t.Fatalf("ledger: %+v", check)
	}
	var rec struct{ Items []gen.Reconciliation }
	if code := e.Do("POST", "/api/v1/admin/reconciliation", map[string]any{"from": start, "to": time.Now().Add(time.Minute)}, &rec); code != http.StatusOK || len(rec.Items) != 2 {
		t.Fatalf("reconciliation: %d %+v", code, rec)
	}
	for _, r := range rec.Items {
		if len(r.Differences) != 0 || r.Matched == 0 || r.Error != nil && *r.Error != "" {
			t.Errorf("%s: matched %d, differences %+v", r.Provider, r.Matched, r.Differences)
		}
	}
}

// TestRefundReopeningInvoice refunds a manual payment that paid an
// invoice: refused from credit alone, then allowed by reopening it.
func TestRefundReopeningInvoice(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	o := newPayOrg(t, e, "Refunded Co", false)
	var pay gen.Payment
	if code := e.Do("POST", "/api/v1/admin/payments", map[string]any{
		"org_id": o.id, "amount_minor": o.inv.TotalMinor, "reference": "GTB-R-1", "invoice_id": o.inv.Id,
	}, &pay); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("record: %d", code)
	}
	if s := invoiceStatus(e, o.inv.Id); s != "paid" {
		t.Fatalf("after the payment: %s", s)
	}
	path := "/api/v1/admin/payments/" + pay.Id.String() + "/refund"
	if code := e.Do("POST", path, map[string]any{"amount_minor": 100_000, "reason": "wrong org"}, nil); code != http.StatusBadRequest {
		t.Errorf("refund without reopening: %d", code)
	}
	var rf struct{ Status string }
	if code := e.Do("POST", path, map[string]any{"amount_minor": 100_000, "reason": "wrong org", "reopen_invoices": true}, &rf); code != http.StatusOK || rf.Status != "completed" {
		t.Fatalf("refund reopening: %d %+v", code, rf)
	}
	if s := invoiceStatus(e, o.inv.Id); s != "partially_paid" {
		t.Errorf("after the refund: %s", s)
	}
	var check gen.LedgerCheck
	if e.Do("GET", "/api/v1/admin/ledger/check", nil, &check); !check.Balanced {
		t.Errorf("ledger: %+v", check)
	}
}
