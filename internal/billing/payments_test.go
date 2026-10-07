package billing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
)

// issuedProInvoice puts org on Pro from 1 October and issues October's
// invoice on 1 November: ₦15,000 for October and ₦15,000 for November in
// advance, plus VAT: ₦32,250.
func issuedProInvoice(t *testing.T, s *billing.Service, clk *clock, org uuid.UUID) store.Invoice {
	t.Helper()
	ctx := context.Background()
	clk.t = day(1)
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	clk.t = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	inv, err := s.Draft(ctx, org, day(1))
	if err != nil || inv == nil {
		t.Fatalf("draft: %v", err)
	}
	issued, err := s.Issue(ctx, inv.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if issued.TotalMinor != 3_225_000 {
		t.Fatalf("total %d", issued.TotalMinor)
	}
	return issued
}

func invoiceNow(t *testing.T, q *store.Queries, id uuid.UUID) store.Invoice {
	t.Helper()
	inv, err := q.GetInvoice(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestPaymentShortByWHT(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	q := store.New(db)
	org := newOrg(t, db, "acme")
	if _, err := s.UpdateDetails(ctx, org, billing.Details{DeductsWHT: true}); err != nil {
		t.Fatal(err)
	}
	inv := issuedProInvoice(t, s, clk, org)
	if inv.WhtExpectedMinor != 150_000 { // 5% of ₦30,000
		t.Fatalf("expected WHT %d", inv.WhtExpectedMinor)
	}
	// A transfer of the total less the expected WHT.
	st, err := s.RecordPayment(ctx, billing.PaymentIn{OrgID: org, Provider: billing.ProviderISpend, Channel: billing.ChannelTransfer,
		ProviderRef: "isp-1", AmountMinor: 3_075_000, FeeMinor: 5_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Allocations) != 1 || st.Allocations[0].WHT != 150_000 || st.Allocations[0].Status != billing.StatusPaidWHTPending || st.Credit != 0 {
		t.Fatalf("settlement %+v", st)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaidWHTPending || got.WhtDeductedMinor != 150_000 {
		t.Errorf("invoice %s, WHT %d", got.Status, got.WhtDeductedMinor)
	}
	for acct, want := range map[string]int64{billing.AccReceivable: 0, billing.AccWHTReceivable: 150_000} {
		if b := balance(t, db, &org, acct); b != want {
			t.Errorf("%s = %d, want %d", acct, b, want)
		}
	}
	if b := balance(t, db, nil, "cash:ispend"); b != 3_070_000 {
		t.Errorf("cash %d", b)
	}
	if b := balance(t, db, nil, "fees:ispend"); b != 5_000 {
		t.Errorf("fees %d", b)
	}
	mustCheck(t, db)
}

func TestPartialOverAndDuplicatePayments(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	q := store.New(db)
	org := newOrg(t, db, "acme")
	inv := issuedProInvoice(t, s, clk, org)

	// Partial, and a WHT-sized shortfall from an org that doesn't deduct WHT is partial too.
	pay := func(ref string, amount int64) billing.Settlement {
		t.Helper()
		st, err := s.RecordPayment(ctx, billing.PaymentIn{OrgID: org, Provider: billing.ProviderFlutterwave, Channel: billing.ChannelTransfer, ProviderRef: ref, AmountMinor: amount})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := pay("flw-1", 1_000_000); st.Allocations[0].Status != billing.StatusPartiallyPaid {
		t.Fatalf("partial: %+v", st)
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 2_225_000 {
		t.Errorf("receivable after the partial %d", b)
	}
	// The same transfer again changes nothing.
	if st := pay("flw-1", 1_000_000); !st.Duplicate {
		t.Fatal("a duplicate was recorded")
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 2_225_000 {
		t.Errorf("receivable after the duplicate %d", b)
	}
	// Over: the rest and ₦5,000 more, which becomes credit.
	if st := pay("flw-2", 2_725_000); st.Allocations[0].Amount != 2_225_000 || st.Credit != 500_000 {
		t.Fatalf("over: %+v", st)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid || got.PaidMinor != inv.TotalMinor || got.PaidAt == nil {
		t.Errorf("invoice %+v", got)
	}
	if c, _ := s.CreditAvailable(ctx, db, org); c != 500_000 {
		t.Errorf("credit %d", c)
	}

	// November's invoice is paid from the credit as far as it goes.
	clk.t = time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC)
	nov, err := s.Draft(ctx, org, clk.t.AddDate(0, -1, 0))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.Issue(ctx, nov.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if issued.PaidMinor != 500_000 || issued.Status != billing.StatusPartiallyPaid {
		t.Errorf("November after credit: paid %d, %s", issued.PaidMinor, issued.Status)
	}
	if c, _ := s.CreditAvailable(ctx, db, org); c != 0 {
		t.Errorf("credit left %d", c)
	}
	mustCheck(t, db)
}

func TestManualPaymentAndRefund(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	inv := issuedProInvoice(t, s, clk, org)

	if _, err := s.RecordManual(ctx, billing.ManualPayment{OrgID: org, AmountMinor: 1000}); !errors.Is(err, billing.ErrInvalid) {
		t.Errorf("no reference: %v", err)
	}
	st, err := s.RecordManual(ctx, billing.ManualPayment{OrgID: org, AmountMinor: inv.TotalMinor + 200_000, Reference: "GTB/0042", InvoiceID: &inv.ID, Note: "cheque"})
	if err != nil || st.Credit != 200_000 {
		t.Fatalf("manual: %+v %v", st, err)
	}
	if b := balance(t, db, nil, "cash:bank"); b != inv.TotalMinor+200_000 {
		t.Errorf("cash:bank %d", b)
	}

	// Refunds come from credit.
	if _, err := s.Refund(ctx, st.Payment.ID, 300_000, "overpaid", false, nil); !errors.Is(err, billing.ErrInvalid) {
		t.Errorf("refund beyond the credit: %v", err)
	}
	r, err := s.Refund(ctx, st.Payment.ID, 150_000, "overpaid", false, nil)
	if err != nil || r.Status != "completed" {
		t.Fatalf("refund: %+v %v", r, err)
	}
	if c, _ := s.CreditAvailable(ctx, db, org); c != 50_000 {
		t.Errorf("credit after the refund %d", c)
	}
	if b := balance(t, db, nil, "cash:bank"); b != inv.TotalMinor+50_000 {
		t.Errorf("cash:bank after the refund %d", b)
	}
	mustCheck(t, db)
}

// A refund of money that paid an invoice reopens the invoice, when asked;
// a refund the provider refuses pays it again.
func TestRefundReopensInvoices(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	q := store.New(db)
	org := newOrg(t, db, "acme")
	inv := issuedProInvoice(t, s, clk, org)
	st, err := s.RecordManual(ctx, billing.ManualPayment{OrgID: org, AmountMinor: inv.TotalMinor, Reference: "GTB/0043", InvoiceID: &inv.ID})
	if err != nil || invoiceNow(t, q, inv.ID).Status != billing.StatusPaid {
		t.Fatalf("manual: %+v %v", st, err)
	}
	if _, err := s.Refund(ctx, st.Payment.ID, 300_000, "paid the wrong org", false, nil); !errors.Is(err, billing.ErrInvalid) {
		t.Errorf("refund of applied money without reopening: %v", err)
	}
	r, err := s.Refund(ctx, st.Payment.ID, 300_000, "paid the wrong org", true, nil)
	if err != nil || r.Status != "completed" {
		t.Fatalf("refund: %+v %v", r, err)
	}
	now := invoiceNow(t, q, inv.ID)
	if now.Status != billing.StatusPartiallyPaid || now.PaidMinor != inv.TotalMinor-300_000 || now.PaidAt != nil {
		t.Errorf("reopened: %s paid %d at %v", now.Status, now.PaidMinor, now.PaidAt)
	}
	if b := balance(t, db, &org, "receivable"); b != 300_000 {
		t.Errorf("receivable %d", b)
	}
	if b := balance(t, db, nil, "cash:bank"); b != inv.TotalMinor-300_000 {
		t.Errorf("cash:bank %d", b)
	}
	if c, _ := s.CreditAvailable(ctx, db, org); c != 0 {
		t.Errorf("credit %d", c)
	}
	if _, err := s.Refund(ctx, st.Payment.ID, inv.TotalMinor, "the rest", true, nil); !errors.Is(err, billing.ErrInvalid) {
		t.Errorf("refund beyond the payment: %v", err)
	}
	r, err = s.Refund(ctx, st.Payment.ID, inv.TotalMinor-300_000, "the rest", true, nil)
	if err != nil || r.Status != "completed" || invoiceNow(t, q, inv.ID).Status != billing.StatusIssued {
		t.Fatalf("refund the rest: %+v %v %s", r, err, invoiceNow(t, q, inv.ID).Status)
	}
	mustCheck(t, db)

	// The provider (here, not configured) refuses: the invoice is paid again.
	org2 := newOrg(t, db, "globex")
	inv2 := issuedProInvoice(t, s, clk, org2)
	st2, err := s.RecordPayment(ctx, billing.PaymentIn{OrgID: org2, Provider: billing.ProviderFlutterwave, Channel: billing.ChannelCard, ProviderRef: "c-ref", AmountMinor: inv2.TotalMinor})
	if err != nil {
		t.Fatal(err)
	}
	if r, err := s.Refund(ctx, st2.Payment.ID, inv2.TotalMinor, "chargeback", true, nil); err == nil || r.Status != "failed" {
		t.Fatalf("refused refund: %+v %v", r, err)
	}
	if now := invoiceNow(t, q, inv2.ID); now.Status != billing.StatusPaid || now.PaidMinor != inv2.TotalMinor {
		t.Errorf("after a refused refund: %s paid %d", now.Status, now.PaidMinor)
	}
	if c, _ := s.CreditAvailable(ctx, db, org2); c != 0 {
		t.Errorf("credit %d", c)
	}
	mustCheck(t, db)
}

// A credit note on a paid invoice becomes credit, which a top-up-free
// refund can return.
func TestCreditNoteOnPaidInvoiceBecomesCredit(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	inv := issuedProInvoice(t, s, clk, org)
	if _, err := s.RecordPayment(ctx, billing.PaymentIn{OrgID: org, Provider: billing.ProviderFlutterwave, Channel: billing.ChannelCard, ProviderRef: "c1", AmountMinor: inv.TotalMinor}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreditNote(ctx, inv.ID, 100_000, "outage", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.CreditAvailable(ctx, db, org); c != 107_500 {
		t.Errorf("credit %d, want the note with VAT", c)
	}
	mustCheck(t, db)
}
