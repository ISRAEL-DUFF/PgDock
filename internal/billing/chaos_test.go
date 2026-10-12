package billing_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
)

// failureEmails counts "payment … failed" emails.
func failureEmails(w *payWorld) int {
	n := 0
	for _, m := range w.sent.msgs {
		if strings.Contains(m.Subject, "failed") {
			n++
		}
	}
	return n
}

// TestProviderOutage is the M27 chaos test for a Flutterwave and iSpend
// outage: checkout fails cleanly, an automatic charge that hits the outage
// is neither a decline nor a payment and is tried again once the provider
// is back, and money that moved while webhooks were lost is posted exactly
// once by the re-query.
func TestProviderOutage(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	w.s.SetDunning(&fakeSuspender{db: w.db}, nil)
	org := newOrg(t, w.db, "acme")
	if _, err := w.s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	in, _ := w.s.StartCheckout(ctx, billing.CheckoutStart{OrgID: org, Channel: billing.ChannelCard, Purpose: billing.PurposeCardSetup})
	_, _ = w.flw.CompleteCheckout(in.Reference, true)
	w.event(t)
	va, err := w.s.EnsureVirtualAccount(ctx, org)
	if err != nil {
		t.Fatal(err)
	}

	// Both providers go down.
	w.flw.SetDown(true)
	w.isp.SetDown(true)
	inv := issuedProInvoice(t, w.s, w.clk, org) // the charge on issue hits the outage
	if a, _ := w.s.Account(ctx, org); a.CardFailingSince != nil || a.DunningState != billing.DunningOK {
		t.Fatalf("an outage counted as a decline: %+v", a)
	}
	if n := failureEmails(w); n != 0 {
		t.Fatalf("%d failure emails during an outage", n)
	}
	if _, err := w.s.StartCheckout(ctx, billing.CheckoutStart{OrgID: org, Channel: billing.ChannelCard, Purpose: billing.PurposeInvoice, InvoiceID: &inv.ID}); err == nil {
		t.Fatal("checkout during an outage succeeded")
	}
	if n, err := w.s.Requery(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); err == nil || n != 0 {
		t.Fatalf("re-query during an outage: %d %v", n, err)
	}
	// The daily run while still down: tried again, still nothing changes.
	if err := w.s.RunDunning(ctx); err != nil {
		t.Fatal(err)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status == billing.StatusPaid || failureEmails(w) != 0 {
		t.Fatalf("during the outage: %s, %d emails", got.Status, failureEmails(w))
	}

	// A bank transfer lands while iSpend is down and its webhook is lost.
	w.isp.DropWebhooks(1)
	if _, err := w.isp.Transfer(va.AccountNumber, 50_000); err != nil {
		t.Fatal(err)
	}
	w.noEvent(t)

	// Recovery: the next run charges the card once, the re-query finds the
	// transfer once.
	w.flw.SetDown(false)
	w.isp.SetDown(false)
	w.flw.DropWebhooks(1) // the charge's webhook is lost too; the charge settles synchronously
	if err := w.s.RunDunning(ctx); err != nil {
		t.Fatal(err)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid {
		t.Fatalf("after recovery: %s", got.Status)
	}
	n, err := w.s.Requery(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("re-query after recovery: %d %v", n, err)
	}
	if n, _ := w.s.Requery(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("a second re-query posted %d", n)
	}
	if err := w.s.RunDunning(ctx); err != nil { // nothing more to charge
		t.Fatal(err)
	}
	var charges int
	if err := w.db.QueryRow(ctx, `SELECT count(*) FROM payments WHERE org_id = $1 AND provider = 'flutterwave' AND amount_minor > 10000`, org).Scan(&charges); err != nil || charges != 1 {
		t.Fatalf("card charges for the invoice: %d %v", charges, err)
	}
	if c, _ := w.s.CreditAvailable(ctx, w.db, org); c != 50_000 {
		t.Errorf("credit %d, want the transfer", c)
	}
	if n := failureEmails(w); n != 0 {
		t.Errorf("%d failure emails", n)
	}
	mustCheck(t, w.db)
}

// TestCardRetryDuringOutage: a retry day that falls in an outage isn't used
// up, and the customer isn't told their card failed.
func TestCardRetryDuringOutage(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	w.s.SetDunning(&fakeSuspender{db: w.db}, nil)
	org := newOrg(t, w.db, "acme")
	if _, err := w.s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	in, _ := w.s.StartCheckout(ctx, billing.CheckoutStart{OrgID: org, Channel: billing.ChannelCard, Purpose: billing.PurposeCardSetup})
	_, _ = w.flw.CompleteCheckout(in.Reference, true)
	w.event(t)
	token := "flw-t1-" + in.Reference
	w.flw.Decline(token, "Insufficient funds")
	inv := issuedProInvoice(t, w.s, w.clk, org)
	a, _ := w.s.Account(ctx, org)
	if a.CardFailingSince == nil || failureEmails(w) != 1 {
		t.Fatalf("after the decline: %+v, %d emails", a, failureEmails(w))
	}
	start := *a.CardFailingSince

	w.flw.SetDown(true)
	w.clk.t = start.Add(3*24*time.Hour + time.Hour)
	if err := w.s.RunDunning(ctx); err != nil {
		t.Fatal(err)
	}
	if n := failureEmails(w); n != 1 {
		t.Fatalf("an outage on retry day sent %d failure emails", n)
	}
	w.flw.SetDown(false)
	w.flw.Accept(token)
	w.clk.t = start.Add(3*24*time.Hour + 2*time.Hour)
	if err := w.s.RunDunning(ctx); err != nil { // the day-3 retry, again
		t.Fatal(err)
	}
	w.event(t)
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid {
		t.Fatalf("after the deferred retry: %s", got.Status)
	}
	if a, _ := w.s.Account(ctx, org); a.CardFailingSince != nil || a.DunningState != billing.DunningOK {
		t.Errorf("after the retry: %+v", a)
	}
	mustCheck(t, w.db)
}
