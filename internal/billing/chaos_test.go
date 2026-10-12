package billing_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
)

// failureEmails counts "wallet couldn't pay" emails.
func failureEmails(w *payWorld) int {
	n := 0
	for _, m := range w.sent.msgs {
		if strings.Contains(m.Subject, "couldn't pay") {
			n++
		}
	}
	return n
}

// TestProviderOutage is the M27 chaos test for an outage of the wallet
// provider: a debit on issue that hits it is neither a decline nor a payment,
// and money that arrived while webhooks were lost is posted exactly once by
// the re-query, which also pays what is owed.
func TestProviderOutage(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	w.s.SetDunning(&fakeSuspender{db: w.db}, nil)
	org, wid := w.walletOrg(t, "acme", 100_000_00, true)
	va, err := w.s.EnsureTransferAccount(ctx, org)
	if err != nil {
		t.Fatal(err)
	}

	w.fake.SetDown(true)
	inv := issuedProInvoice(t, w.s, w.clk, org) // the debit on issue hits the outage
	if a, _ := w.s.Account(ctx, org); a.ChargeFailingSince != nil || a.DunningState != billing.DunningOK {
		t.Fatalf("an outage counted as a decline: %+v", a)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusIssued || failureEmails(w) != 0 {
		t.Fatalf("during the outage: %s, %d emails", got.Status, failureEmails(w))
	}
	from, to := w.window()
	if n, err := w.s.Requery(ctx, from, to); err == nil || n != 0 {
		t.Fatalf("re-query during an outage: %d %v", n, err)
	}
	// The daily run while still down: nothing changes.
	if err := w.s.RunDunning(ctx); err != nil {
		t.Fatal(err)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusIssued || failureEmails(w) != 0 {
		t.Fatalf("after a run during the outage: %s, %d emails", got.Status, failureEmails(w))
	}

	// A bank transfer lands during the outage and its webhook is lost.
	w.fake.DropWebhooks(1)
	if _, _, _, err := w.fake.Deposit(va.AccountNumber, 50_000_00); err != nil {
		t.Fatal(err)
	}
	w.noEvent(t)

	// Recovery: the re-query finds the transfer once, and the money that
	// arrives pays the open invoice, once.
	w.fake.SetDown(false)
	if n, err := w.s.Requery(ctx, from, to); err != nil || n != 1 {
		t.Fatalf("re-query after recovery: %d %v", n, err)
	}
	paid := w.waitPaid(t, inv.ID)
	if n, _ := w.s.Requery(ctx, from, to); n != 0 {
		t.Errorf("a second re-query posted %d", n)
	}
	if err := w.s.RunDunning(ctx); err != nil { // nothing more to charge
		t.Fatal(err)
	}
	var debits, deposits int
	if err := w.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE channel = 'wallet'), count(*) FILTER (WHERE channel = 'transfer')
		FROM payments WHERE org_id = $1 AND provider = 'iswallet'`, org).Scan(&debits, &deposits); err != nil || debits != 1 || deposits != 2 {
		t.Fatalf("debits and deposits: %d, %d, %v", debits, deposits, err)
	}
	if got := w.fake.OperatingBalance(); got != paid.TotalMinor {
		t.Errorf("PGDock's wallet got %d for one invoice of %d", got, paid.TotalMinor)
	}
	if got := w.held(t, org, billing.AccWalletBalance); got != w.fake.WalletBalance(wid) {
		t.Errorf("books %d, provider %d", got, w.fake.WalletBalance(wid))
	}
	if n := failureEmails(w); n != 0 {
		t.Errorf("%d failure emails", n)
	}
	mustCheck(t, w.db)
}

// TestChargeRetryDuringOutage: a retry day that falls in an outage isn't
// used up, and the customer isn't told their wallet was short.
func TestChargeRetryDuringOutage(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	w.s.SetDunning(&fakeSuspender{db: w.db}, nil)
	org, wid := w.walletOrg(t, "acme", 5_000_00, true) // not enough for the invoice
	va, err := w.s.EnsureTransferAccount(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	inv := issuedProInvoice(t, w.s, w.clk, org)
	a, _ := w.s.Account(ctx, org)
	if a.ChargeFailingSince == nil || failureEmails(w) != 1 {
		t.Fatalf("after the decline: %+v, %d emails", a, failureEmails(w))
	}
	start := *a.ChargeFailingSince

	// The wallet is topped up, but the webhook is lost: PGDock doesn't know yet.
	w.fake.DropWebhooks(1)
	if _, _, _, err := w.fake.Deposit(va.AccountNumber, 50_000_00); err != nil {
		t.Fatal(err)
	}
	w.noEvent(t)

	w.fake.SetDown(true)
	w.clk.t = start.Add(3*24*time.Hour + time.Hour)
	if err := w.s.RunDunning(ctx); err != nil {
		t.Fatal(err)
	}
	if n := failureEmails(w); n != 1 {
		t.Fatalf("an outage on retry day sent %d failure emails", n)
	}
	w.fake.SetDown(false)
	w.clk.t = start.Add(3*24*time.Hour + 2*time.Hour)
	if err := w.s.RunDunning(ctx); err != nil { // the day-3 retry, again
		t.Fatal(err)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid {
		t.Fatalf("after the deferred retry: %s", got.Status)
	}
	if a, _ := w.s.Account(ctx, org); a.ChargeFailingSince != nil || a.DunningState != billing.DunningOK {
		t.Errorf("after the retry: %+v", a)
	}
	// The re-query catches the books up with the deposit.
	from, to := start.Add(-time.Hour), w.clk.Now().Add(time.Hour)
	if n, err := w.s.Requery(ctx, from, to); err != nil || n != 1 {
		t.Fatalf("re-query: %d %v", n, err)
	}
	if got := w.held(t, org, billing.AccWalletBalance); got != w.fake.WalletBalance(wid) {
		t.Errorf("books %d, provider %d", got, w.fake.WalletBalance(wid))
	}
	mustCheck(t, w.db)
}
