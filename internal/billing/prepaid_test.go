package billing_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/xenibase/internal/billing"
	"github.com/israel-duff/xenibase/internal/store"
	"github.com/israel-duff/xenibase/internal/tenancy"
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
	if _, err := s.RecordPayment(context.Background(), billing.PaymentIn{OrgID: org, Provider: billing.ProviderBank, Channel: billing.ChannelManual,
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

// Switching a prepaid org to postpaid mid-month returns the month's
// deductions, so the invoice recognises its revenue once; switching to
// prepaid is refused while issued invoices are open.
func TestSwitchingBillingMode(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	makePrepaid(t, s, org)
	clk.t = day(1)
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	topup(t, s, org, "isp-1", 5_000_000)
	clk.t = day(10)
	if n, err := s.DeductPrepaid(ctx, org); err != nil || n == 0 {
		t.Fatalf("deducted %d %v", n, err)
	}
	a, _ := s.Account(ctx, org)
	settings := billing.AdminSettings{Mode: billing.ModePostpaid, PaymentTermsDays: 14, PriceBookVersion: a.PriceBookVersion}
	if _, err := s.AdminUpdate(ctx, org, settings); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.CreditAvailable(ctx, db, org); c != 5_000_000 {
		t.Errorf("credit after switching to postpaid %d", c)
	}
	if b := balance(t, db, &org, "revenue:pro"); b != 0 {
		t.Errorf("Pro revenue left recognised %d", b)
	}

	// The month's invoice is a receivable, paid from the returned credit.
	clk.t = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	inv, err := s.Draft(ctx, org, day(1))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.Issue(ctx, inv.ID, nil)
	if err != nil || issued.Status != billing.StatusPaid {
		t.Fatalf("issue: %+v %v", issued, err)
	}
	if b := balance(t, db, &org, "revenue:pro"); b != -3_000_000 {
		t.Errorf("Pro revenue %d, want October's and November's fee once", b)
	}
	if c, _ := s.CreditAvailable(ctx, db, org); c != 5_000_000-issued.TotalMinor {
		t.Errorf("credit %d after the invoice of %d", c, issued.TotalMinor)
	}
	mustCheck(t, db)

	// Back to prepaid: refused while an issued invoice is open.
	org2 := newOrg(t, db, "globex")
	inv2 := issuedProInvoice(t, s, clk, org2)
	a2, _ := s.Account(ctx, org2)
	pre := billing.AdminSettings{Mode: billing.ModePrepaid, PaymentTermsDays: a2.PaymentTermsDays, PriceBookVersion: a2.PriceBookVersion}
	if _, err := s.AdminUpdate(ctx, org2, pre); !errors.Is(err, billing.ErrInvalid) {
		t.Errorf("prepaid with an open invoice: %v", err)
	}
	topup(t, s, org2, "isp-2", inv2.TotalMinor)
	if _, err := s.AdminUpdate(ctx, org2, pre); err != nil {
		t.Errorf("prepaid once paid: %v", err)
	}
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
