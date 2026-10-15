package integration

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/xenibase/internal/api/gen"
	"github.com/israel-duff/xenibase/internal/billing/iswallet"
	"github.com/israel-duff/xenibase/test/testenv"
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

// openWallet opens org's wallet (and its bank account) through the API.
func openWallet(t *testing.T, e *testenv.Env, org uuid.UUID, name string) gen.Wallet {
	t.Helper()
	var w gen.Wallet
	if code := e.Do("POST", "/api/v1/orgs/"+org.String()+"/billing/wallet", gen.WalletSetup{BusinessName: name + " Ltd", Email: "ap@" + strings.ToLower(strings.ReplaceAll(name, " ", "")) + ".example", Bvn: "22222222222"}, &w); code != http.StatusCreated ||
		w.WalletId == nil || w.Account == nil {
		t.Fatalf("open the wallet: %d %+v", code, w)
	}
	return w
}

func walletOf(e *testenv.Env, org uuid.UUID) gen.Wallet {
	var w gen.Wallet
	e.Do("GET", "/api/v1/orgs/"+org.String()+"/billing/wallet", nil, &w)
	return w
}

// TestPaymentsThroughWallets is the payment path end to end, against a fake
// of the wallet provider whose webhooks reach the API: a deposit lands in
// the org's wallet and settles nothing by itself; consent debits it for the
// invoice; a button pays one invoice without consent; duplicate, lost and
// forged webhooks; a deposit nobody owns attributed by hand; manual bank
// payments (short by WHT, partial, over); and reconciliation with zero
// differences.
func TestPaymentsThroughWallets(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	start := time.Now().Add(-time.Minute)
	countPays := func(org uuid.UUID) int {
		var pays gen.PaymentList
		e.Do("GET", "/api/v1/orgs/"+org.String()+"/billing/payments", nil, &pays)
		return len(pays.Items)
	}

	// 1. A deposit is held; agreeing to debits pays the invoice from it.
	held := newPayOrg(t, e, "Wallet Co", false)
	hid := held.id.String()
	if w := walletOf(e, held.id); !w.Available || w.WalletId != nil {
		t.Fatalf("before the wallet is opened: %+v", w)
	}
	w := openWallet(t, e, held.id, "Wallet Co")
	const gross = 5_000_000
	_, _, credited, err := e.ISWallet.Deposit(w.Account.AccountNumber, gross)
	if err != nil {
		t.Fatal(err)
	}
	awaitPay(t, "the deposit", func() bool { return walletOf(e, held.id).LedgerMinor == credited })
	if s := invoiceStatus(e, held.inv.Id); s != "issued" {
		t.Fatalf("a deposit settled the invoice: %s", s)
	}
	// A duplicate webhook changes nothing.
	e.ISWallet.RedeliverLast()
	time.Sleep(500 * time.Millisecond)
	if n := countPays(held.id); n != 1 {
		t.Fatalf("payments after a duplicate webhook: %d", n)
	}
	if got := walletOf(e, held.id); got.LedgerMinor != credited || got.OwedMinor != held.inv.TotalMinor {
		t.Fatalf("after a duplicate webhook: %+v", got)
	}
	if code := e.Do("POST", "/api/v1/orgs/"+hid+"/billing/debit-consent", gen.DebitConsentRequest{TermsVersion: w.TermsVersion}, nil); code != http.StatusOK {
		t.Fatalf("consent: %d", code)
	}
	awaitPay(t, "the debit", func() bool { return invoiceStatus(e, held.inv.Id) == "paid" })
	if got := walletOf(e, held.id); got.LedgerMinor != credited-held.inv.TotalMinor || got.OwedMinor != 0 || e.ISWallet.WalletBalance(*w.WalletId) != got.LedgerMinor {
		t.Errorf("the wallet after the debit: %+v, provider holds %d", got, e.ISWallet.WalletBalance(*w.WalletId))
	}

	// 2. Without consent, one invoice is paid on the org's say-so.
	btn := newPayOrg(t, e, "Button Co", false)
	bw := openWallet(t, e, btn.id, "Button Co")
	if _, _, _, err := e.ISWallet.Deposit(bw.Account.AccountNumber, gross); err != nil {
		t.Fatal(err)
	}
	awaitPay(t, "the deposit", func() bool { return walletOf(e, btn.id).LedgerMinor > 0 })
	var ch gen.WalletCharge
	if code := e.Do("POST", "/api/v1/orgs/"+btn.id.String()+"/billing/invoices/"+btn.inv.Id.String()+"/pay-from-wallet", nil, &ch); code != http.StatusOK || !ch.Succeeded || ch.PaidMinor != btn.inv.TotalMinor {
		t.Fatalf("pay from the wallet: %d %+v", code, ch)
	}
	if s := invoiceStatus(e, btn.inv.Id); s != "paid" {
		t.Errorf("after paying from the wallet: %s", s)
	}

	// 3. A lost webhook: the re-query finds the deposit.
	lost := newPayOrg(t, e, "Lost Co", false)
	lw := openWallet(t, e, lost.id, "Lost Co")
	e.ISWallet.DropWebhooks(1)
	if _, _, _, err := e.ISWallet.Deposit(lw.Account.AccountNumber, gross); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := walletOf(e, lost.id).LedgerMinor; got != 0 {
		t.Fatalf("a dropped webhook was posted: %d", got)
	}
	if n, err := e.Billing.Requery(ctx, start, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("re-query: %d %v", n, err)
	}
	if got := walletOf(e, lost.id).LedgerMinor; got != e.ISWallet.WalletBalance(*lw.WalletId) || got == 0 {
		t.Errorf("after the re-query: the books hold %d, the provider %d", got, e.ISWallet.WalletBalance(*lw.WalletId))
	}

	// 4. Money paid into an account Xenibase has no record of waits for an
	// admin to say whose it is.
	own := newPayOrg(t, e, "Owner Co", false)
	ow := openWallet(t, e, own.id, "Owner Co")
	if _, err := e.DB.Exec(ctx, `DELETE FROM virtual_accounts WHERE org_id = $1`, own.id); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.ISWallet.Deposit(ow.Account.AccountNumber, 300_000); err != nil {
		t.Fatal(err)
	}
	var events struct{ Items []gen.PaymentEvent }
	awaitPay(t, "the unmatched event", func() bool {
		e.Do("GET", "/api/v1/admin/payment-events?outcome=unmatched", nil, &events)
		return len(events.Items) == 1
	})
	if n := countPays(own.id); n != 0 {
		t.Fatalf("unmatched money was posted: %d", n)
	}
	var attributed gen.Payment
	if code := e.Do("POST", "/api/v1/admin/payment-events/"+strconv.FormatInt(events.Items[0].Id, 10)+"/attribute", map[string]any{"org_id": own.id}, &attributed); code != http.StatusOK ||
		attributed.OrgId != own.id || attributed.Provider != "iswallet" {
		t.Fatalf("attribute: %d %+v", code, attributed)
	}
	if got := walletOf(e, own.id).LedgerMinor; got != attributed.AmountMinor {
		t.Errorf("the wallet after attribution: %d, want %d", got, attributed.AmountMinor)
	}

	// 5. Manual bank payments: short by WHT, then partial and over.
	wht := newPayOrg(t, e, "Tax Co", true)
	var pay gen.Payment
	if code := e.Do("POST", "/api/v1/admin/payments", map[string]any{
		"org_id": wht.id, "amount_minor": wht.inv.TotalMinor - wht.inv.WhtExpectedMinor, "reference": "GTB-WHT-1", "invoice_id": wht.inv.Id,
	}, &pay); code != http.StatusCreated {
		t.Fatalf("WHT-short payment: %d", code)
	}
	if s := invoiceStatus(e, wht.inv.Id); s != "paid_wht_pending" {
		t.Fatalf("after the WHT-short payment: %s", s)
	}
	pdf := []byte("%PDF-1.4\n%%EOF")
	if code, body := e.DoBytes("POST", "/api/v1/orgs/"+wht.id.String()+"/billing/invoices/"+wht.inv.Id.String()+"/wht-certificate?filename=wht.pdf", "application/octet-stream", pdf); code != http.StatusCreated {
		t.Fatalf("WHT credit note: %d %s", code, body)
	}
	if s := invoiceStatus(e, wht.inv.Id); s != "paid" {
		t.Errorf("after the credit note: %s", s)
	}

	part := newPayOrg(t, e, "Partial Co", false)
	record := func(amount int64, ref string) {
		t.Helper()
		if code := e.Do("POST", "/api/v1/admin/payments", map[string]any{"org_id": part.id, "amount_minor": amount, "reference": ref}, nil); code != http.StatusCreated {
			t.Fatalf("record %s: %d", ref, code)
		}
	}
	record(part.inv.TotalMinor/2, "GTB-P-1")
	if s := invoiceStatus(e, part.inv.Id); s != "partially_paid" {
		t.Fatalf("after half: %s", s)
	}
	if code := e.Do("POST", "/api/v1/admin/payments", map[string]any{"org_id": part.id, "amount_minor": 1, "reference": "GTB-P-1"}, nil); code != http.StatusConflict {
		t.Errorf("a repeated bank reference: %d", code)
	}
	record(part.inv.TotalMinor-part.inv.TotalMinor/2+100_000, "GTB-P-2")
	if s := invoiceStatus(e, part.inv.Id); s != "paid" {
		t.Errorf("after the rest: %s", s)
	}
	var acct gen.BillingAccount
	if e.Do("GET", "/api/v1/orgs/"+part.id.String()+"/billing", nil, &acct); acct.CreditMinor == nil || *acct.CreditMinor != 100_000 {
		t.Errorf("credit from the overpayment: %+v", acct.CreditMinor)
	}

	// 6. Forged webhooks are refused.
	hook := e.URL + "/api/v1/payments/webhooks/iswallet"
	body := []byte(`{"id":"evt-forged","event_type":"wallet.credit.posted","data":{"txn_id":"t1"}}`)
	if code, _ := e.DoBytes("POST", "/api/v1/payments/webhooks/iswallet", "application/json", body); code != http.StatusUnauthorized {
		t.Errorf("unsigned webhook: %d", code)
	}
	ts := time.Now().Unix()
	req, err := http.NewRequest("POST", hook, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-iSpend-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-iSpend-Signature", iswallet.Sign("another-secret-0123456", ts, body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("webhook signed with another secret: %d", res.StatusCode)
	}

	// 7. Receipts, the ledger, and reconciliation against the provider.
	var pays gen.PaymentList
	e.Do("GET", "/api/v1/orgs/"+hid+"/billing/payments", nil, &pays)
	// The deposit, and the debit that paid its invoice.
	if len(pays.Items) != 2 {
		t.Fatalf("the wallet org's payments: %+v", pays)
	}
	if code, body := e.DoBytes("GET", "/api/v1/orgs/"+hid+"/billing/payments/"+pays.Items[0].Id.String()+"/receipt", "", nil); code != http.StatusOK || !strings.HasPrefix(string(body), "%PDF") {
		t.Errorf("receipt: %d", code)
	}
	var check gen.LedgerCheck
	if e.Do("GET", "/api/v1/admin/ledger/check", nil, &check); !check.Balanced {
		t.Fatalf("ledger: %+v", check)
	}
	var rec struct{ Items []gen.Reconciliation }
	if code := e.Do("POST", "/api/v1/admin/reconciliation", map[string]any{"from": start, "to": time.Now().Add(time.Minute)}, &rec); code != http.StatusOK || len(rec.Items) == 0 {
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
