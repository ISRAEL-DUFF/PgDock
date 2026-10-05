package billing_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

func makePrepaid(t *testing.T, s *billing.Service, org uuid.UUID) {
	t.Helper()
	a, err := s.Account(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminUpdate(context.Background(), org, billing.AdminSettings{Mode: billing.ModePrepaid, PaymentTermsDays: a.PaymentTermsDays, PriceBookVersion: a.PriceBookVersion}); err != nil {
		t.Fatal(err)
	}
}

func topup(t *testing.T, s *billing.Service, org uuid.UUID, ref string, amount int64) {
	t.Helper()
	if _, err := s.RecordPayment(context.Background(), billing.PaymentIn{OrgID: org, Provider: billing.ProviderISpend, Channel: billing.ChannelTransfer,
		ProviderRef: ref, AmountMinor: amount}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepaidDailyDeductionAndTrueUp(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	makePrepaid(t, s, org)
	clk.t = day(1)
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	topup(t, s, org, "isp-1", 5_000_000)
	if c, _ := s.CreditAvailable(ctx, db, org); c != 5_000_000 {
		t.Fatalf("a prepaid payment is credit: %d", c)
	}
	ded := uuid.New()
	usage(t, db, org, ded, tenancy.MetricDedicatedCPU, day(1), day(10), "2") // 432 vCPU-hours

	clk.t = day(10).Add(2 * time.Hour)
	n, err := s.DeductPrepaid(ctx, org)
	// October's Pro (₦15,000) + 432 × 2,740 kobo, and VAT on both.
	sub := int64(1_500_000 + 1_183_680)
	want := sub + billing.DecInt(sub).Mul(billing.D("0.075")).Round()
	if err != nil || n != want {
		t.Fatalf("deducted %d, want %d (%v)", n, want, err)
	}
	if n, _ := s.DeductPrepaid(ctx, org); n != 0 {
		t.Errorf("a second deduction the same day: %d", n)
	}
	usage(t, db, org, ded, tenancy.MetricDedicatedCPU, day(10), day(15), "2") // 240 more
	n, _ = s.DeductPrepaid(ctx, org)
	more := int64(240 * 2740)
	if want := more + billing.DecInt(sub+more).Mul(billing.D("0.075")).Round() - billing.DecInt(sub).Mul(billing.D("0.075")).Round(); n != want {
		t.Errorf("the next day's deduction %d, want %d", n, want)
	}
	if b := balance(t, db, &org, "revenue:dedicated"); b != -(1_183_680 + more) {
		t.Errorf("dedicated revenue recognised %d", b)
	}

	// 1 November: the invoice trues up (November's fee in advance) and is
	// paid from the balance; nothing is receivable.
	clk.t = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	inv, err := s.Draft(ctx, org, day(1))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.Issue(ctx, inv.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Status != billing.StatusPaid {
		t.Errorf("prepaid invoice %s", issued.Status)
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 0 {
		t.Errorf("receivable %d", b)
	}
	if c, _ := s.CreditAvailable(ctx, db, org); c != 5_000_000-issued.TotalMinor {
		t.Errorf("balance %d after the invoice of %d", c, issued.TotalMinor)
	}
	if b := balance(t, db, &org, "revenue:pro"); b != -3_000_000 {
		t.Errorf("Pro revenue %d", b)
	}
	mustCheck(t, db)
}

func TestPrepaidAlertsAndZeroBalance(t *testing.T) {
	s, db, clk, sent := newService(t)
	ctx := context.Background()
	q := store.New(db)
	org := newOrg(t, db, "acme")
	if _, err := s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	makePrepaid(t, s, org)
	clk.t = day(1)
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	topup(t, s, org, "isp-1", 2_000_000) // ₦20,000 against ₦16,125 of October so far
	clk.t = day(2)
	putWatermark(t, db, day(2))
	if _, err := s.DeductPrepaid(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPrepaid(ctx, org); err != nil {
		t.Fatal(err)
	}
	subjects := func() string {
		var out []string
		for _, m := range sent.msgs {
			out = append(out, m.Subject)
		}
		return strings.Join(out, " | ")
	}
	if !strings.Contains(subjects(), "prepaid balance is low") {
		t.Fatalf("no low-balance alert: %s", subjects())
	}
	n := len(sent.msgs)
	if err := s.CheckPrepaid(ctx, org); err != nil {
		t.Fatal(err)
	}
	if len(sent.msgs) != n {
		t.Errorf("the alert was sent twice: %s", subjects())
	}

	// Usage past the balance: zero, an email, and the grace starts.
	usage(t, db, org, uuid.New(), tenancy.MetricDedicatedCPU, day(1), day(2), "40") // 960 × 2,740
	if _, err := s.DeductPrepaid(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPrepaid(ctx, org); err != nil {
		t.Fatal(err)
	}
	a, _ := q.GetBillingAccount(ctx, org)
	if a.ZeroBalanceAt == nil || !strings.Contains(subjects(), "has run out") {
		t.Fatalf("zero balance: %v / %s", a.ZeroBalanceAt, subjects())
	}
	// A top-up ends it.
	topup(t, s, org, "isp-2", 3_000_000)
	if err := s.CheckPrepaid(ctx, org); err != nil {
		t.Fatal(err)
	}
	if a, _ := q.GetBillingAccount(ctx, org); a.ZeroBalanceAt != nil {
		t.Error("still at zero after a top-up")
	}
	mustCheck(t, db)
}

func TestAutoTopupAndChargeOnIssue(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)

	// A postpaid org with a saved card: its invoice is charged on issue.
	org := newOrg(t, w.db, "acme")
	saveCard(t, w, org)
	inv := issuedProInvoice(t, w.s, w.clk, org)
	w.event(t) // the charge's webhook
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid {
		t.Fatalf("charged on issue: %s", got.Status)
	}

	// A prepaid org tops up from its card when it falls below ₦10,000.
	pre := newOrg(t, w.db, "prepaid")
	makePrepaid(t, w.s, pre)
	saveCard(t, w, pre)
	before, _ := w.s.CreditAvailable(ctx, w.db, pre)
	if err := w.s.SetAutoTopup(ctx, pre, &billing.AutoTopup{BelowMinor: 1_000_000, AmountMinor: 2_500_000}); err != nil {
		t.Fatal(err)
	}
	if err := w.s.CheckPrepaid(ctx, pre); err != nil {
		t.Fatal(err)
	}
	w.event(t)
	if c, _ := w.s.CreditAvailable(ctx, w.db, pre); c != before+2_500_000 {
		t.Fatalf("balance %d after an auto top-up from %d", c, before)
	}
	// Not again the same day.
	if err := w.s.CheckPrepaid(ctx, pre); err != nil {
		t.Fatal(err)
	}
	w.noEvent(t)
	mustCheck(t, w.db)
}

// saveCard saves a card for org by a ₦100 card checkout (kept as credit).
func saveCard(t *testing.T, w *payWorld, org uuid.UUID) {
	t.Helper()
	in, err := w.s.StartCheckout(context.Background(), billing.CheckoutStart{OrgID: org, Channel: billing.ChannelCard, Purpose: billing.PurposeCardSetup})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.flw.CompleteCheckout(in.Reference, true); err != nil {
		t.Fatal(err)
	}
	if e := w.event(t); e != "payment.succeeded posted" {
		t.Fatalf("card setup: %s", e)
	}
}

func TestCardExpiryReminders(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	org := newOrg(t, w.db, "acme")
	if _, err := w.s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	saveCard(t, w, org) // expires 09/32
	count := func() int {
		n := 0
		for _, m := range w.sent.msgs {
			if strings.Contains(m.Subject, "expires soon") {
				n++
			}
		}
		return n
	}
	for _, at := range []struct {
		t    time.Time
		want int
	}{
		{time.Date(2032, 8, 15, 0, 0, 0, 0, time.UTC), 0},
		{time.Date(2032, 9, 5, 0, 0, 0, 0, time.UTC), 1},
		{time.Date(2032, 9, 6, 0, 0, 0, 0, time.UTC), 1},
		{time.Date(2032, 9, 25, 0, 0, 0, 0, time.UTC), 2},
		{time.Date(2032, 9, 28, 0, 0, 0, 0, time.UTC), 2},
	} {
		w.clk.t = at.t
		if err := w.s.CardReminders(ctx); err != nil {
			t.Fatal(err)
		}
		if got := count(); got != at.want {
			t.Errorf("%s: %d reminders, want %d", at.t.Format("2 Jan"), got, at.want)
		}
	}
	w.clk.t = time.Date(2032, 10, 1, 0, 0, 0, 0, time.UTC)
	_ = w.s.CardReminders(ctx)
	if left, _ := store.New(w.db).ListPaymentMethods(ctx, org); len(left) != 0 {
		t.Error("an expired card is still offered")
	}
}
