package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
)

// TestCreditNoteBeyondWhatIsOwed is from the M27 billing audit: a credit
// note on a partly paid invoice can be larger than what is still owed.
// The excess is credit the org holds (credit_balance), not a negative
// receivable, and an invoice credited down to nothing is settled, so
// dunning doesn't chase it.
func TestCreditNoteBeyondWhatIsOwed(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	q := store.New(db)
	org := newOrg(t, db, "acme")
	inv := issuedProInvoice(t, s, clk, org) // ₦30,000 + 7.5% VAT = ₦32,250
	if _, err := s.RecordPayment(ctx, billing.PaymentIn{OrgID: org, Provider: billing.ProviderFlutterwave, Channel: billing.ChannelTransfer,
		ProviderRef: "flw-1", AmountMinor: 2_000_000}); err != nil {
		t.Fatal(err)
	}
	// ₦12,250 is owed; a ₦20,000 (+ ₦1,500 VAT) credit note leaves ₦9,250 to the org's credit.
	if _, err := s.CreditNote(ctx, inv.ID, 2_000_000, "SLA credit", nil); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 0 {
		t.Errorf("receivable %d, want 0", b)
	}
	if b := balance(t, db, &org, billing.AccCreditBalance); b != -925_000 {
		t.Errorf("credit balance %d, want -925000", b)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid {
		t.Errorf("invoice %s, want paid", got.Status)
	}
	mustCheck(t, db)

	// An unpaid invoice credited in full is settled too: nothing overdue.
	org2 := newOrg(t, db, "beta")
	inv2 := issuedProInvoice(t, s, clk, org2)
	if _, err := s.CreditNote(ctx, inv2.ID, 3_000_000, "goodwill", nil); err != nil {
		t.Fatal(err)
	}
	if got := invoiceNow(t, q, inv2.ID); got.Status != billing.StatusPaid {
		t.Errorf("fully credited invoice %s, want paid", got.Status)
	}
	if _, err := q.OrgOverdueSince(ctx, store.OrgOverdueSinceParams{OrgID: org2, At: time.Now().AddDate(1, 0, 0)}); err == nil {
		t.Error("a fully credited invoice still makes the org overdue")
	}
	if b := balance(t, db, &org2, billing.AccReceivable); b != 0 {
		t.Errorf("receivable %d, want 0", b)
	}
	mustCheck(t, db)
}

// TestRoundingRules pins the rounding the audit reviewed: kobo amounts
// round half away from zero, once per line; VAT and WHT round once on the
// subtotal.
func TestRoundingRules(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"0.4999", 0}, {"0.5", 1}, {"2.5", 3}, {"-0.5", -1}, {"-2.5", -3}, {"-2.4", -2},
		{"34.25", 34}, {"1234567.5", 1234568},
		// VAT at 7.5% on small subtotals: 1 kobo has none, 7 kobo rounds up.
		{"0.075", 0}, {"0.525", 1},
	} {
		if got := billing.D(c.in).Round(); got != c.want {
			t.Errorf("Round(%s) = %d, want %d", c.in, got, c.want)
		}
	}
	// Exact decimals: no float drift over many small quantities.
	sum := billing.D("0")
	for range 1000 {
		sum = sum.Add(billing.D("0.001"))
	}
	if sum.Cmp(billing.D("1")) != 0 {
		t.Errorf("1000 × 0.001 = %s", sum)
	}
}

// TestCreditNoteVATAndLimits: a credit note's VAT is at the invoice's
// rate, rounded on its own; the credits on an invoice, VAT included, can
// never exceed its total.
func TestCreditNoteVATAndLimits(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	inv := issuedProInvoice(t, s, clk, org) // ₦30,000 + ₦2,250 VAT
	cn, err := s.CreditNote(ctx, inv.ID, 7, "rounding", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cn.VatMinor != 1 { // 7 × 7.5% = 0.525 → 1 kobo
		t.Errorf("VAT on 7 kobo: %d", cn.VatMinor)
	}
	// What remains: ₦32,250 less 8 kobo, at most 2,999,993 before VAT.
	if _, err := s.CreditNote(ctx, inv.ID, 2_999_994, "too much", nil); err == nil {
		t.Error("credits beyond the invoice's total were accepted")
	}
	if _, err := s.CreditNote(ctx, inv.ID, 2_999_993, "the rest", nil); err != nil {
		t.Fatalf("the rest: %v", err)
	}
	var credited int64
	if err := db.QueryRow(ctx, `SELECT sum(amount_minor + vat_minor) FROM credit_notes WHERE invoice_id = $1`, inv.ID).Scan(&credited); err != nil || credited > inv.TotalMinor {
		t.Fatalf("credited %d of %d (%v)", credited, inv.TotalMinor, err)
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != inv.TotalMinor-credited {
		t.Errorf("receivable %d, want %d", b, inv.TotalMinor-credited)
	}
	mustCheck(t, db)
}

// TestWHTAfterAPartialPayment: a customer that deducts WHT pays part of an
// invoice, then the rest short by exactly the expected WHT: the shortfall
// is WHT, not a debt.
func TestWHTAfterAPartialPayment(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	q := store.New(db)
	org := newOrg(t, db, "acme")
	if _, err := s.UpdateDetails(ctx, org, billing.Details{DeductsWHT: true}); err != nil {
		t.Fatal(err)
	}
	inv := issuedProInvoice(t, s, clk, org) // ₦32,250, WHT expected ₦1,500
	pay := func(ref string, amount int64) {
		t.Helper()
		if _, err := s.RecordPayment(ctx, billing.PaymentIn{OrgID: org, Provider: billing.ProviderISpend, Channel: billing.ChannelTransfer,
			ProviderRef: ref, AmountMinor: amount}); err != nil {
			t.Fatal(err)
		}
	}
	pay("isp-1", 1_000_000)
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPartiallyPaid {
		t.Fatalf("after the first part: %s", got.Status)
	}
	pay("isp-2", 2_225_000-150_000)
	got := invoiceNow(t, q, inv.ID)
	if got.Status != billing.StatusPaidWHTPending || got.WhtDeductedMinor != 150_000 {
		t.Fatalf("after the rest: %s, WHT %d", got.Status, got.WhtDeductedMinor)
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 0 {
		t.Errorf("receivable %d", b)
	}
	if b := balance(t, db, &org, billing.AccWHTReceivable); b != 150_000 {
		t.Errorf("WHT receivable %d", b)
	}
	mustCheck(t, db)
}
