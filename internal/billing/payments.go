package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// PaymentIn is money received, from a provider event, a re-query, or the
// platform admin.
type PaymentIn struct {
	OrgID       uuid.UUID
	Provider    string
	Channel     string
	ProviderRef string
	Reference   string // ours, when the payment started here
	AmountMinor int64
	FeeMinor    int64
	// InvoiceID is paid first when set (a checkout for that invoice).
	InvoiceID *uuid.UUID
	// Topup puts the whole amount on the credit balance.
	Topup       bool
	FXQuote     json.RawMessage
	Note        string
	ProofKey    string
	RecordedBy  *uuid.UUID
	ReceivedAt  time.Time
	CashAccount string // default cash:<provider>
}

// Settlement is what a payment did.
type Settlement struct {
	Payment   store.Payment
	Duplicate bool
	// Allocations are the invoices it paid; Credit is what went to the
	// credit balance.
	Allocations []Allocation
	Credit      int64
}

// Allocation is one invoice a payment settled.
type Allocation struct {
	InvoiceID uuid.UUID
	Number    string
	Amount    int64
	WHT       int64
	Status    string
}

// outstanding is what is left to pay on an invoice: its total less what
// was paid, the WHT deducted, and credit notes.
func outstanding(ctx context.Context, q *store.Queries, inv store.Invoice) (int64, error) {
	credited, err := q.InvoiceCredited(ctx, inv.ID)
	if err != nil {
		return 0, err
	}
	return inv.TotalMinor - inv.PaidMinor - inv.WhtDeductedMinor - credited, nil
}

// RecordPayment records p and settles it, in one transaction: a postpaid
// org's oldest open invoices first (the invoice it was for, if any), WHT
// when a transfer is short by exactly the expected WHT (V3 §3.7), the
// rest as credit. A payment already recorded (same provider and
// reference) changes nothing.
func (s *Service) RecordPayment(ctx context.Context, p PaymentIn) (Settlement, error) {
	if p.AmountMinor <= 0 {
		return Settlement{}, invalid("a payment's amount must be positive")
	}
	if p.FeeMinor < 0 || p.FeeMinor > p.AmountMinor {
		return Settlement{}, invalid("a fee must be between 0 and the amount")
	}
	if p.ReceivedAt.IsZero() {
		p.ReceivedAt = s.Now()
	}
	var out Settlement
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		out, err = s.recordPayment(ctx, tx, p)
		return err
	})
	if err == nil && !out.Duplicate {
		s.afterPayment(ctx, p.OrgID)
		s.notifyOrg(ctx, p.OrgID, "billing.receipt", fmt.Sprintf("PGDock: payment of %s received", Naira(p.AmountMinor)),
			fmt.Sprintf("Thank you: %s received from %s on %s (reference %s).\n\nThe receipt: %s/org/billing?org=%s\n",
				Naira(p.AmountMinor), s.orgName(ctx, p.OrgID), p.ReceivedAt.UTC().Format("2 January 2006"), p.ProviderRef, s.publicURL, p.OrgID))
	}
	return out, err
}

func (s *Service) recordPayment(ctx context.Context, tx pgx.Tx, p PaymentIn) (Settlement, error) {
	q := store.New(tx)
	if _, err := s.Account(ctx, p.OrgID); err != nil {
		return Settlement{}, err
	}
	a, err := q.LockBillingAccount(ctx, p.OrgID)
	if err != nil {
		return Settlement{}, err
	}
	var note, ref, proof *string
	if p.Note != "" {
		note = &p.Note
	}
	if p.Reference != "" {
		ref = &p.Reference
	}
	if p.ProofKey != "" {
		proof = &p.ProofKey
	}
	var fx []byte
	if len(p.FXQuote) > 0 {
		fx = p.FXQuote
	}
	pay, err := q.InsertPayment(ctx, store.InsertPaymentParams{
		OrgID: p.OrgID, Provider: p.Provider, Channel: p.Channel, ProviderRef: p.ProviderRef, Reference: ref,
		AmountMinor: p.AmountMinor, FeeMinor: p.FeeMinor, FxQuote: fx, Note: note, ProofObjectKey: proof,
		RecordedBy: p.RecordedBy, ReceivedAt: p.ReceivedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err := q.GetPaymentByProviderRef(ctx, store.GetPaymentByProviderRefParams{Provider: p.Provider, ProviderRef: p.ProviderRef})
		return Settlement{Payment: existing, Duplicate: true}, err
	}
	if err != nil {
		return Settlement{}, err
	}
	out := Settlement{Payment: pay}
	left := p.AmountMinor
	var whtTotal int64
	if !p.Topup && a.Mode == ModePostpaid {
		invs, err := q.OpenInvoices(ctx, p.OrgID)
		if err != nil {
			return out, err
		}
		if p.InvoiceID != nil {
			for i, inv := range invs {
				if inv.ID == *p.InvoiceID {
					invs = append([]store.Invoice{inv}, append(invs[:i:i], invs[i+1:]...)...)
					break
				}
			}
		}
		for _, inv := range invs {
			if left == 0 {
				break
			}
			owed, err := outstanding(ctx, q, inv)
			if err != nil {
				return out, err
			}
			if owed <= 0 {
				continue
			}
			var paid, wht int64
			status := StatusPartiallyPaid
			switch {
			case inv.WhtExpectedMinor > 0 && inv.WhtDeductedMinor == 0 && left == owed-inv.WhtExpectedMinor:
				// Short by exactly the expected WHT: the customer deducted it.
				paid, wht, status = left, inv.WhtExpectedMinor, StatusPaidWHTPending
			case left >= owed:
				paid, status = owed, StatusPaid
			default:
				paid = left
			}
			if wht > 0 && inv.WhtEvidencedAt != nil {
				status = StatusPaid
			}
			if _, err := q.SettleInvoice(ctx, store.SettleInvoiceParams{ID: inv.ID, PaidMinor: paid, WhtMinor: wht, Status: status}); err != nil {
				return out, err
			}
			if err := q.InsertAllocation(ctx, store.InsertAllocationParams{PaymentID: pay.ID, InvoiceID: inv.ID, AmountMinor: paid, WhtMinor: wht}); err != nil {
				return out, err
			}
			out.Allocations = append(out.Allocations, Allocation{InvoiceID: inv.ID, Number: deref(inv.Number), Amount: paid, WHT: wht, Status: status})
			left -= paid
			whtTotal += wht
		}
	}
	out.Credit = left

	org := p.OrgID
	cash := p.CashAccount
	if cash == "" {
		cash = AccCashPrefix + p.Provider
	}
	t := Txn{Key: "payment:" + pay.ID.String(), SourceType: SourcePayment, SourceID: p.Provider + ":" + p.ProviderRef, By: p.RecordedBy,
		Memo: fmt.Sprintf("%s payment %s", p.Channel, p.ProviderRef)}
	if net := p.AmountMinor - p.FeeMinor; net > 0 {
		t.Entries = append(t.Entries, Dr(cash, &org, net))
	}
	if p.FeeMinor > 0 {
		t.Entries = append(t.Entries, Dr(AccFeesPrefix+p.Provider, &org, p.FeeMinor))
	}
	if settled := p.AmountMinor - left; settled > 0 {
		t.Entries = append(t.Entries, Cr(AccReceivable, &org, settled))
	}
	if left > 0 {
		t.Entries = append(t.Entries, Cr(AccCreditBalance, &org, left))
	}
	if _, err := Post(ctx, tx, t); err != nil {
		return out, err
	}
	if whtTotal > 0 {
		if _, err := Post(ctx, tx, Txn{
			Key: "wht:" + pay.ID.String(), SourceType: SourceWHT, SourceID: pay.ID.String(), By: p.RecordedBy,
			Memo:    "WHT deducted by the customer",
			Entries: []Entry{Dr(AccWHTReceivable, &org, whtTotal), Cr(AccReceivable, &org, whtTotal)},
		}); err != nil {
			return out, err
		}
	}
	if left > 0 && a.Mode == ModePostpaid && p.Topup {
		// A top-up on a postpaid account pays what is open.
		if _, err := s.applyCredit(ctx, tx, p.OrgID); err != nil {
			return out, err
		}
	}
	return out, nil
}

// CreditAvailable is what an org holds on its credit balance.
func (s *Service) CreditAvailable(ctx context.Context, db store.DBTX, orgID uuid.UUID) (int64, error) {
	b, err := Balance(ctx, db, &orgID, AccCreditBalance)
	return -b, err
}

// applyCredit pays an org's open invoices from its credit balance (an
// overpayment, a credit note on a paid invoice, a top-up).
func (s *Service) applyCredit(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (int64, error) {
	q := store.New(tx)
	avail, err := s.CreditAvailable(ctx, tx, orgID)
	if err != nil || avail <= 0 {
		return 0, err
	}
	invs, err := q.OpenInvoices(ctx, orgID)
	if err != nil {
		return 0, err
	}
	var used int64
	org := orgID
	for _, inv := range invs {
		if avail == 0 {
			break
		}
		owed, err := outstanding(ctx, q, inv)
		if err != nil {
			return used, err
		}
		if owed <= 0 {
			continue
		}
		pay := min(avail, owed)
		status := StatusPartiallyPaid
		if pay == owed {
			status = StatusPaid
		}
		if _, err := q.SettleInvoice(ctx, store.SettleInvoiceParams{ID: inv.ID, PaidMinor: pay, Status: status}); err != nil {
			return used, err
		}
		if _, err := Post(ctx, tx, Txn{
			Key: fmt.Sprintf("credit:%s:%d", inv.ID, inv.PaidMinor+pay), SourceType: SourceCredit, SourceID: deref(inv.Number),
			Memo:    "Credit balance applied to " + deref(inv.Number),
			Entries: []Entry{Dr(AccCreditBalance, &org, pay), Cr(AccReceivable, &org, pay)},
		}); err != nil {
			return used, err
		}
		avail -= pay
		used += pay
	}
	return used, nil
}

// ApplyCredit pays orgID's open invoices from its credit balance.
func (s *Service) ApplyCredit(ctx context.Context, orgID uuid.UUID) (int64, error) {
	var used int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := store.New(tx).LockBillingAccount(ctx, orgID); err != nil {
			return err
		}
		var err error
		used, err = s.applyCredit(ctx, tx, orgID)
		return err
	})
	return used, err
}

// afterPayment runs once money has arrived: dunning re-evaluates (paying
// what is owed lifts restrictions), and a receipt is emailed.
func (s *Service) afterPayment(ctx context.Context, orgID uuid.UUID) {
	if s.onPaid != nil {
		s.onPaid(ctx, orgID)
	}
}

// ManualPayment is a payment the platform admin records (V3 §3.4.7): a
// transfer to the company's bank account, a cheque, an arrangement.
type ManualPayment struct {
	OrgID       uuid.UUID
	AmountMinor int64
	Reference   string
	Note        string
	InvoiceID   *uuid.UUID
	Topup       bool
	ProofKey    string
	ReceivedAt  time.Time
	By          *uuid.UUID
}

// RecordManual records a manual payment into cash:bank.
func (s *Service) RecordManual(ctx context.Context, m ManualPayment) (Settlement, error) {
	ref := strings.TrimSpace(m.Reference)
	if ref == "" || len(ref) > 200 {
		return Settlement{}, invalid("a manual payment needs its bank reference (at most 200 characters)")
	}
	if m.ReceivedAt.After(s.Now().Add(time.Hour)) {
		return Settlement{}, invalid("a payment can't be received in the future")
	}
	return s.RecordPayment(ctx, PaymentIn{
		OrgID: m.OrgID, Provider: ProviderBank, Channel: ChannelManual, ProviderRef: ref, AmountMinor: m.AmountMinor,
		InvoiceID: m.InvoiceID, Topup: m.Topup, Note: m.Note, ProofKey: m.ProofKey, RecordedBy: m.By, ReceivedAt: m.ReceivedAt,
	})
}

// ---- Refunds ---------------------------------------------------------------------

// Refund refunds part of a payment from the org's credit balance: the
// money must first be credit (a credit note on a paid invoice, an
// overpayment, unused prepaid funds). The ledger moves when the provider
// confirms (V3 §3.3: a refund reverses the payment's cash; nothing is
// edited).
func (s *Service) Refund(ctx context.Context, paymentID uuid.UUID, amount int64, reason string, by *uuid.UUID) (store.Refund, error) {
	reason = strings.TrimSpace(reason)
	if amount <= 0 || reason == "" {
		return store.Refund{}, invalid("a refund needs a positive amount and a reason")
	}
	var r store.Refund
	var pay store.Payment
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		pay, err = q.LockPayment(ctx, paymentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := q.LockBillingAccount(ctx, pay.OrgID); err != nil {
			return err
		}
		pending, err := q.PaymentRefunds(ctx, pay.ID)
		if err != nil {
			return err
		}
		var asked int64
		for _, x := range pending {
			if x.Status != "failed" {
				asked += x.AmountMinor
			}
		}
		if asked+amount > pay.AmountMinor {
			return invalid("at most %s of this payment is left to refund", Naira(pay.AmountMinor-asked))
		}
		avail, err := s.CreditAvailable(ctx, tx, pay.OrgID)
		if err != nil {
			return err
		}
		if amount > avail {
			return invalid("the organisation holds %s of credit; issue a credit note first so the refund comes from credit", Naira(avail))
		}
		r, err = q.InsertRefund(ctx, store.InsertRefundParams{PaymentID: pay.ID, AmountMinor: amount, Reason: reason, CreatedBy: by})
		return err
	})
	if err != nil {
		return r, err
	}
	if pay.Provider == ProviderBank {
		// A manual payment is refunded by the admin outside PGDock.
		return s.completeRefund(ctx, r.ID, "manual:"+r.ID.String())
	}
	p, ok := s.providers[pay.Provider]
	if !ok {
		return s.failRefund(ctx, r.ID, "provider "+pay.Provider+" isn't configured")
	}
	res, err := p.Refund(ctx, RefundRequest{ProviderRef: pay.ProviderRef, AmountMinor: amount, Reference: "refund-" + r.ID.String()})
	if err != nil {
		return s.failRefund(ctx, r.ID, err.Error())
	}
	switch res.Status {
	case TxSucceeded:
		return s.completeRefund(ctx, r.ID, res.ProviderRef)
	case TxFailed:
		return s.failRefund(ctx, r.ID, "the provider refused the refund")
	}
	ref := res.ProviderRef
	return store.New(s.db).FinishRefund(ctx, store.FinishRefundParams{ID: r.ID, Status: "pending", ProviderRef: &ref})
}

func (s *Service) failRefund(ctx context.Context, id uuid.UUID, msg string) (store.Refund, error) {
	r, err := store.New(s.db).FinishRefund(ctx, store.FinishRefundParams{ID: id, Status: "failed", Error: &msg})
	if err != nil {
		return r, err
	}
	return r, fmt.Errorf("refund failed: %s", msg)
}

// completeRefund posts a completed refund: credit balance down, cash out.
func (s *Service) completeRefund(ctx context.Context, id uuid.UUID, providerRef string) (store.Refund, error) {
	var r store.Refund
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		r, err = q.FinishRefund(ctx, store.FinishRefundParams{ID: id, Status: "completed", ProviderRef: &providerRef})
		if err != nil {
			return err
		}
		pay, err := q.GetPayment(ctx, r.PaymentID)
		if err != nil {
			return err
		}
		if err := q.AddPaymentRefunded(ctx, store.AddPaymentRefundedParams{ID: pay.ID, AmountMinor: r.AmountMinor}); err != nil {
			return err
		}
		org := pay.OrgID
		cash := AccCashPrefix + pay.Provider
		_, err = Post(ctx, tx, Txn{
			Key: "refund:" + r.ID.String(), SourceType: SourceRefund, SourceID: providerRef, By: r.CreatedBy,
			Memo:    "Refund: " + r.Reason,
			Entries: []Entry{Dr(AccCreditBalance, &org, r.AmountMinor), Cr(cash, &org, r.AmountMinor)},
		})
		return err
	})
	return r, err
}
