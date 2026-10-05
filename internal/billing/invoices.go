package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
)

// Invoice statuses.
const (
	StatusDraft          = "draft"
	StatusIssued         = "issued"
	StatusPaid           = "paid"
	StatusPaidWHTPending = "paid_wht_pending"
	StatusPartiallyPaid  = "partially_paid"
	StatusVoid           = "void"
)

func pgDate(t time.Time) pgtype.Date { return pgtype.Date{Time: t.UTC(), Valid: true} }

// Draft rates orgID's month and saves it as a draft invoice, replacing an
// earlier draft. It returns the invoice, or nil when there is nothing to
// bill (and no draft is left). An issued invoice isn't touched.
func (s *Service) Draft(ctx context.Context, orgID uuid.UUID, month time.Time) (*store.Invoice, error) {
	r, err := s.Rate(ctx, orgID, month)
	if err != nil {
		return nil, err
	}
	var out *store.Invoice
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.GetOrgInvoiceForPeriod(ctx, store.GetOrgInvoiceForPeriodParams{OrgID: orgID, PeriodStart: pgDate(r.Period)})
		switch {
		case err == nil && cur.Status != StatusDraft:
			out = &cur
			return nil // issued: never edited (credit notes correct it)
		case err == nil && len(r.Lines) == 0:
			_, err := q.DeleteDraftInvoice(ctx, cur.ID)
			return err
		case errors.Is(err, pgx.ErrNoRows) && len(r.Lines) == 0:
			return nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var inv store.Invoice
		if err == nil {
			inv, err = q.UpdateDraftInvoice(ctx, store.UpdateDraftInvoiceParams{
				ID: cur.ID, SubtotalMinor: r.Subtotal, VatMinor: r.VAT, TotalMinor: r.Total, WhtExpectedMinor: r.WHTExpected,
				VatRate: r.VATRate.Numeric(), PriceBookVersion: r.PriceBookVersion,
			})
		} else {
			inv, err = q.InsertDraftInvoice(ctx, store.InsertDraftInvoiceParams{
				OrgID: orgID, PeriodStart: pgDate(r.Period), PeriodEnd: pgDate(r.Period.AddDate(0, 1, -1)),
				SubtotalMinor: r.Subtotal, VatMinor: r.VAT, TotalMinor: r.Total, WhtExpectedMinor: r.WHTExpected,
				VatRate: r.VATRate.Numeric(), PriceBookVersion: r.PriceBookVersion,
			})
		}
		if err != nil {
			return err
		}
		if err := q.DeleteInvoiceLines(ctx, inv.ID); err != nil {
			return err
		}
		for _, l := range r.Lines {
			var metric *string
			if l.Metric != "" {
				metric = &l.Metric
			}
			if err := q.InsertInvoiceLine(ctx, store.InsertInvoiceLineParams{
				InvoiceID: inv.ID, Kind: l.Kind, Description: l.Description, ProjectID: l.ProjectID, Metric: metric,
				Quantity: l.Quantity.Numeric(), UnitPriceMinor: l.UnitPrice.Numeric(), AmountMinor: l.Amount, RevenueAccount: l.Revenue,
			}); err != nil {
				return err
			}
		}
		out = &inv
		return nil
	})
	return out, err
}

// DraftAll drafts month's invoice for every organisation.
func (s *Service) DraftAll(ctx context.Context, month time.Time) (int, error) {
	if err := s.Init(ctx); err != nil {
		return 0, err
	}
	accts, err := store.New(s.db).ListBillingAccounts(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, a := range accts {
		inv, err := s.Draft(ctx, a.OrgID, month)
		if err != nil {
			errs = append(errs, fmt.Errorf("org %s: %w", a.OrgID, err))
			continue
		}
		if inv != nil && inv.Status == StatusDraft {
			n++
		}
	}
	return n, errors.Join(errs...)
}

// Hold keeps a draft from being issued (e.g. disputed usage), or releases it.
func (s *Service) Hold(ctx context.Context, invoiceID uuid.UUID, held bool, reason *string) (store.Invoice, error) {
	if !held {
		reason = nil
	}
	inv, err := store.New(s.db).SetInvoiceHold(ctx, store.SetInvoiceHoldParams{ID: invoiceID, Held: held, HoldReason: reason})
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, s.notDraft(ctx, invoiceID)
	}
	return inv, err
}

func (s *Service) notDraft(ctx context.Context, id uuid.UUID) error {
	if inv, err := store.New(s.db).GetInvoice(ctx, id); err == nil {
		return fmt.Errorf("%w: invoice %s is %s", ErrConflict, deref(inv.Number), inv.Status)
	}
	return ErrNotFound
}

func deref(s *string) string {
	if s == nil {
		return "(draft)"
	}
	return *s
}

// BillTo is the customer's details frozen on an invoice.
type BillTo struct {
	OrgName       string  `json:"org_name"`
	LegalName     *string `json:"legal_name,omitempty"`
	Address       *string `json:"address,omitempty"`
	TIN           *string `json:"tin,omitempty"`
	VATRegistered bool    `json:"vat_registered"`
}

// FormatNumber is an invoice number: PGD-2027-000123 (V3 §3.6).
func FormatNumber(prefix string, year, n int) string {
	return fmt.Sprintf("%s-%d-%06d", prefix, year, n)
}

// Issue issues a draft: it gets the next number of its year, freezes both
// parties' details, posts to the ledger in the same transaction, and is
// emailed to the billing contacts. An admin issuing a held draft releases
// it.
func (s *Service) Issue(ctx context.Context, invoiceID uuid.UUID, by *uuid.UUID) (store.Invoice, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return store.Invoice{}, err
	}
	now := s.Now().UTC()
	var inv store.Invoice
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.LockInvoice(ctx, invoiceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Status != StatusDraft {
			return fmt.Errorf("%w: invoice %s is already %s", ErrConflict, deref(cur.Number), cur.Status)
		}
		a, err := q.GetBillingAccount(ctx, cur.OrgID)
		if err != nil {
			return err
		}
		o, err := q.GetOrg(ctx, cur.OrgID)
		if err != nil {
			return err
		}
		n, err := q.NextBillingNumber(ctx, store.NextBillingNumberParams{Kind: "invoice", Year: int32(now.Year())})
		if err != nil {
			return err
		}
		number := FormatNumber("PGD", now.Year(), int(n))
		billTo, _ := json.Marshal(BillTo{OrgName: o.Name, LegalName: a.LegalName, Address: a.Address, TIN: a.Tin, VATRegistered: a.VatRegistered})
		seller, _ := json.Marshal(set.Seller)
		due := now.AddDate(0, 0, int(a.PaymentTermsDays))
		status, paidAt := StatusIssued, (*time.Time)(nil)
		if cur.TotalMinor <= 0 {
			status, paidAt = StatusPaid, &now // nothing to pay
		}
		inv, err = q.IssueInvoice(ctx, store.IssueInvoiceParams{
			ID: cur.ID, Status: status, Number: &number, IssuedAt: &now, DueAt: &due, BillTo: billTo, Seller: seller, PaidAt: paidAt,
		})
		if err != nil {
			return err
		}
		lines, err := q.InvoiceLines(ctx, inv.ID)
		if err != nil {
			return err
		}
		txn, ok := invoiceTxn(inv, lines, by)
		if !ok {
			return nil
		}
		_, err = Post(ctx, tx, txn)
		return err
	})
	if err != nil {
		return inv, err
	}
	s.mailInvoice(ctx, inv)
	return inv, nil
}

// invoiceTxn is an issued invoice's ledger transaction (V3 §3.3): debit
// receivable, credit revenue by product line and VAT. Credits on the
// invoice net against revenue; a negative total is credit the org holds.
func invoiceTxn(inv store.Invoice, lines []store.InvoiceLine, by *uuid.UUID) (Txn, bool) {
	org := inv.OrgID
	net := map[string]int64{}
	for _, l := range lines {
		net[l.RevenueAccount] += l.AmountMinor
	}
	net[AccVATPayable] += inv.VatMinor
	t := Txn{Key: "invoice:" + inv.ID.String(), SourceType: SourceInvoice, SourceID: deref(inv.Number), By: by,
		Memo: "Invoice " + deref(inv.Number)}
	for _, acct := range sortedKeys(net) {
		switch v := net[acct]; {
		case v > 0:
			t.Entries = append(t.Entries, Cr(acct, &org, v))
		case v < 0:
			t.Entries = append(t.Entries, Dr(acct, &org, -v))
		}
	}
	switch {
	case inv.TotalMinor > 0:
		t.Entries = append(t.Entries, Dr(AccReceivable, &org, inv.TotalMinor))
	case inv.TotalMinor < 0:
		t.Entries = append(t.Entries, Cr(AccCreditBalance, &org, -inv.TotalMinor))
	}
	return t, len(t.Entries) >= 2
}

// IssueDue issues month's drafts that aren't held, once its usage is
// recorded through the end of the month. It is a no-op when the billing
// settings turn automatic issue off.
func (s *Service) IssueDue(ctx context.Context, month time.Time) (int, error) {
	set, err := s.Settings(ctx)
	if err != nil || !set.AutoIssue {
		return 0, err
	}
	drafts, err := store.New(s.db).DraftsForPeriod(ctx, pgDate(MonthStart(month)))
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, d := range drafts {
		if d.Held {
			continue
		}
		if _, err := s.Issue(ctx, d.ID, nil); err != nil {
			errs = append(errs, fmt.Errorf("invoice for org %s: %w", d.OrgID, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

func (s *Service) mailInvoice(ctx context.Context, inv store.Invoice) {
	if s.mail == nil || inv.Number == nil {
		return
	}
	to, err := store.New(s.db).BillingRecipients(ctx, inv.OrgID)
	if err != nil || len(to) == 0 {
		return
	}
	month := inv.PeriodStart.Time.Format("January 2006")
	var b strings.Builder
	fmt.Fprintf(&b, "Invoice %s for %s's usage in %s.\n\n", *inv.Number, s.orgName(ctx, inv.OrgID), month)
	fmt.Fprintf(&b, "Subtotal: %s\nVAT: %s\nTotal: %s\n", Naira(inv.SubtotalMinor), Naira(inv.VatMinor), Naira(inv.TotalMinor))
	if inv.WhtExpectedMinor > 0 {
		fmt.Fprintf(&b, "Withholding tax you may deduct: %s (please send the WHT credit note)\n", Naira(inv.WhtExpectedMinor))
	}
	if inv.TotalMinor > 0 && inv.DueAt != nil {
		fmt.Fprintf(&b, "Due: %s\n", inv.DueAt.UTC().Format("2 January 2006"))
	}
	fmt.Fprintf(&b, "\nView and download it: %s/org/billing?org=%s&invoice=%s\n", s.publicURL, inv.OrgID, inv.ID)
	if err := s.mail.Send(ctx, mail.Message{
		To: to, Subject: fmt.Sprintf("PGDock invoice %s for %s", *inv.Number, month), Body: b.String(),
		Headers: map[string]string{"X-PGDock-Event": "billing.invoice"},
	}); err != nil {
		s.log.Warn("invoice email", "invoice", *inv.Number, "err", err)
	}
}

func (s *Service) orgName(ctx context.Context, orgID uuid.UUID) string {
	if o, err := store.New(s.db).GetOrg(ctx, orgID); err == nil {
		return o.Name
	}
	return orgID.String()
}

// ---- Credit notes (V3 §3.6) ---------------------------------------------------

// CreditNote issues a credit note against an issued invoice: amount is
// before VAT, which is credited at the invoice's rate. The credits on an
// invoice can't exceed its total.
func (s *Service) CreditNote(ctx context.Context, invoiceID uuid.UUID, amount int64, reason string, by *uuid.UUID) (store.CreditNote, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case amount <= 0:
		return store.CreditNote{}, invalid("a credit note's amount must be positive")
	case reason == "" || len(reason) > 500:
		return store.CreditNote{}, invalid("a credit note needs a reason (at most 500 characters)")
	}
	now := s.Now().UTC()
	var cn store.CreditNote
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		inv, err := q.LockInvoice(ctx, invoiceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if inv.Status == StatusDraft || inv.Status == StatusVoid {
			return fmt.Errorf("%w: credit notes are for issued invoices (this one is %s)", ErrConflict, inv.Status)
		}
		vat := DecInt(amount).Mul(DecFromNumeric(inv.VatRate)).Round()
		prior, err := q.InvoiceCreditNotes(ctx, inv.ID)
		if err != nil {
			return err
		}
		var credited int64
		for _, c := range prior {
			credited += c.AmountMinor + c.VatMinor
		}
		if credited+amount+vat > inv.TotalMinor {
			return invalid("the invoice's total is %s and %s is already credited; at most %s more (before VAT) can be",
				Naira(inv.TotalMinor), Naira(credited), Naira(DecInt(inv.TotalMinor-credited).Quo(DecInt(1).Add(DecFromNumeric(inv.VatRate))).Round()))
		}
		n, err := q.NextBillingNumber(ctx, store.NextBillingNumberParams{Kind: "credit_note", Year: int32(now.Year())})
		if err != nil {
			return err
		}
		cn, err = q.InsertCreditNote(ctx, store.InsertCreditNoteParams{
			InvoiceID: inv.ID, OrgID: inv.OrgID, Number: FormatNumber("PGD-CN", now.Year(), int(n)),
			AmountMinor: amount, VatMinor: vat, Reason: reason, IssuedBy: by,
		})
		if err != nil {
			return err
		}
		lines, err := q.InvoiceLines(ctx, inv.ID)
		if err != nil {
			return err
		}
		_, err = Post(ctx, tx, creditNoteTxn(inv, lines, cn, by))
		return err
	})
	return cn, err
}

// creditNoteTxn reverses part of an invoice: debit revenue (spread over the
// invoice's product lines in proportion) and VAT; credit what the org owes,
// or, once the invoice is paid, its credit balance.
func creditNoteTxn(inv store.Invoice, lines []store.InvoiceLine, cn store.CreditNote, by *uuid.UUID) Txn {
	org := inv.OrgID
	byAcct := map[string]int64{}
	var pos int64
	for _, l := range lines {
		if l.AmountMinor > 0 {
			byAcct[l.RevenueAccount] += l.AmountMinor
			pos += l.AmountMinor
		}
	}
	t := Txn{Key: "credit_note:" + cn.ID.String(), SourceType: SourceCredit, SourceID: cn.Number, By: by,
		Memo: fmt.Sprintf("Credit note %s on %s: %s", cn.Number, deref(inv.Number), cn.Reason)}
	accts := sortedKeys(byAcct)
	sort.SliceStable(accts, func(i, j int) bool { return byAcct[accts[i]] > byAcct[accts[j]] })
	left := cn.AmountMinor
	for i, acct := range accts {
		share := left
		if i < len(accts)-1 {
			share = DecInt(cn.AmountMinor).Frac(byAcct[acct], pos).Round()
			share = min(share, left)
		}
		if share > 0 {
			t.Entries = append(t.Entries, Dr(acct, &org, share))
			left -= share
		}
	}
	if left > 0 { // no positive lines: a general adjustment
		t.Entries = append(t.Entries, Dr(AccRevenuePrefix+"adjustments", &org, left))
	}
	if cn.VatMinor > 0 {
		t.Entries = append(t.Entries, Dr(AccVATPayable, &org, cn.VatMinor))
	}
	credit := AccReceivable
	if inv.Status == StatusPaid || inv.Status == StatusPaidWHTPending {
		credit = AccCreditBalance
	}
	t.Entries = append(t.Entries, Cr(credit, &org, cn.AmountMinor+cn.VatMinor))
	return t
}

// ---- The monthly run --------------------------------------------------------------

// usageRecordedThrough is where usage recording has reached (the tenancy
// watermark): the hour and the day it records next.
func (s *Service) usageRecordedThrough(ctx context.Context) (time.Time, error) {
	raw, err := store.New(s.db).GetSetting(ctx, "usage.watermark")
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	var wm struct{ Hour, Day time.Time }
	if err := json.Unmarshal(raw, &wm); err != nil {
		return time.Time{}, err
	}
	if wm.Day.Before(wm.Hour) {
		return wm.Day, nil
	}
	return wm.Hour, nil
}

// Invoicing is the monthly cycle (V3 §3.6): on the last day of a month its
// drafts are generated; once the next month starts and usage is recorded
// through the end of the month, drafts are re-rated with the full month and
// issued (unless held). Months that were missed are caught up.
func (s *Service) Invoicing(ctx context.Context) error {
	now := s.Now().UTC()
	q := store.New(s.db)
	var errs []error
	// The previous month, once its usage is in.
	prev := MonthStart(now).AddDate(0, -1, 0)
	through, err := s.usageRecordedThrough(ctx)
	if err != nil {
		return err
	}
	if !through.Before(MonthStart(now)) {
		done, err := s.monthDone(ctx, prev)
		if err != nil {
			return err
		}
		if !done {
			if _, err := s.DraftAll(ctx, prev); err != nil {
				errs = append(errs, err)
			}
			if _, err := s.IssueDue(ctx, prev); err != nil {
				errs = append(errs, err)
			}
			if err := q.PutSetting(ctx, store.PutSettingParams{Key: invoicedKey, Value: mustJSON(prev)}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// The current month's drafts on its last day, refreshed each run.
	if MonthStart(now.Add(24*time.Hour)) != MonthStart(now) {
		if _, err := s.DraftAll(ctx, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

const invoicedKey = "billing.invoiced_through"

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// monthDone reports whether month was already drafted and issued.
func (s *Service) monthDone(ctx context.Context, month time.Time) (bool, error) {
	raw, err := store.New(s.db).GetSetting(ctx, invoicedKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var last time.Time
	if err := json.Unmarshal(raw, &last); err != nil {
		return false, nil
	}
	return !last.Before(month), nil
}
