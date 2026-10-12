package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Prepaid billing (V3 §3.5): the org keeps a credit balance; usage is rated
// daily and deducted from it. Revenue and VAT are recognised as they are
// deducted, and the month's invoice, issued on the 1st, trues up the rest
// (including next month's plan fee) instead of raising a receivable.

// GraceDays is how long a prepaid org runs at zero balance before dunning.
const GraceDays = 3

// targets are what an org's month comes to so far, per account and VAT
// (lines with Advance left out unless all), as deductions.
func targets(r Rated, vatRate Dec, all bool) map[string]int64 {
	out := map[string]int64{}
	var sub int64
	for _, l := range r.Lines {
		if l.Advance && !all {
			continue
		}
		out[l.Revenue] += l.Amount
		sub += l.Amount
	}
	out[AccVATPayable] = DecInt(sub).Mul(vatRate).Round()
	return out
}

// deductTo moves an org's prepaid deductions for month to want: each
// account's difference posts against the credit balance.
func (s *Service) deductTo(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, month time.Time, want map[string]int64, memo string) (int64, error) {
	q := store.New(tx)
	have := map[string]int64{}
	rows, err := q.PrepaidDeducted(ctx, store.PrepaidDeductedParams{OrgID: orgID, Month: pgDate(month)})
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		have[r.Account] = r.AmountMinor
	}
	for a := range have {
		if _, ok := want[a]; !ok {
			want[a] = 0
		}
	}
	org := orgID
	t := Txn{Key: "prepaid:" + orgID.String() + ":" + uuid.NewString(), SourceType: SourceUsage, SourceID: month.Format("2006-01"), Memo: memo}
	var net int64
	for _, acct := range sortedKeys(want) {
		d := want[acct] - have[acct]
		if d == 0 {
			continue
		}
		if err := q.AddPrepaidDeduction(ctx, store.AddPrepaidDeductionParams{OrgID: orgID, Month: pgDate(month), Account: acct, AmountMinor: d}); err != nil {
			return 0, err
		}
		if d > 0 {
			t.Entries = append(t.Entries, Cr(acct, &org, d))
		} else {
			t.Entries = append(t.Entries, Dr(acct, &org, -d))
		}
		net += d
	}
	switch {
	case net > 0:
		t.Entries = append(t.Entries, Dr(AccCreditBalance, &org, net))
	case net < 0:
		t.Entries = append(t.Entries, Cr(AccCreditBalance, &org, -net))
	}
	if len(t.Entries) < 2 {
		return 0, nil
	}
	_, err = Post(ctx, tx, t)
	return net, err
}

// DeductPrepaid deducts a prepaid org's usage so far this month from its
// balance (a no-op for postpaid orgs).
func (s *Service) DeductPrepaid(ctx context.Context, orgID uuid.UUID) (int64, error) {
	a, err := s.Account(ctx, orgID)
	if err != nil || a.Mode != ModePrepaid {
		return 0, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	month := MonthStart(s.Now())
	r, err := s.Rate(ctx, orgID, month)
	if err != nil {
		return 0, err
	}
	var net int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := store.New(tx).LockBillingAccount(ctx, orgID); err != nil {
			return err
		}
		net, err = s.deductTo(ctx, tx, orgID, month, targets(r, set.VATRate, false), "Prepaid usage, "+month.Format("January 2006"))
		return err
	})
	return net, err
}

// settlePrepaidInvoice trues up a prepaid org's deductions to its issued
// invoice (in Issue's transaction) and marks it paid from the balance.
func (s *Service) settlePrepaidInvoice(ctx context.Context, tx pgx.Tx, inv store.Invoice, lines []store.InvoiceLine) (store.Invoice, error) {
	q := store.New(tx)
	want := map[string]int64{}
	for _, l := range lines {
		want[l.RevenueAccount] += l.AmountMinor
	}
	want[AccVATPayable] = inv.VatMinor
	if _, err := s.deductTo(ctx, tx, inv.OrgID, inv.PeriodStart.Time, want, "Prepaid invoice "+deref(inv.Number)); err != nil {
		return inv, err
	}
	if inv.TotalMinor <= 0 {
		return inv, nil
	}
	return q.SettleInvoice(ctx, store.SettleInvoiceParams{ID: inv.ID, PaidMinor: inv.TotalMinor, Status: StatusPaid})
}

// prepaidAlerts are the shares of the month's projected spend left at which
// the balance is reported (V3 §3.5), then "fewer than 3 days left".
var prepaidAlerts = []int32{50, 25, 10}

// CheckPrepaid sends a prepaid org's balance alerts and starts or clears its
// zero-balance grace.
func (s *Service) CheckPrepaid(ctx context.Context, orgID uuid.UUID) error {
	a, err := s.Account(ctx, orgID)
	if err != nil || a.Mode != ModePrepaid {
		return err
	}
	bal, err := s.CreditAvailable(ctx, s.db, orgID)
	if err != nil {
		return err
	}
	f, err := s.Forecast(ctx, orgID)
	if err != nil {
		return err
	}
	now := s.Now()
	month := MonthStart(now)
	alerted := a.BalanceAlerted
	if !a.BalanceMonth.Valid || !a.BalanceMonth.Time.Equal(month) {
		alerted = 0
	}
	daysInMonth := int64(month.AddDate(0, 1, 0).Sub(month).Hours() / 24)
	var crossed int32
	var why string
	if f.Spend > 0 {
		left := bal * 100 / f.Spend // percent of the month's projected spend
		if left > 50 {
			alerted = 0 // topped up: alerts can fire again
		}
		for _, t := range prepaidAlerts {
			if left <= int64(t) && (alerted == 0 || t < alerted) {
				crossed, why = t, fmt.Sprintf("%d%% of this month's projected spend", t)
			}
		}
		if daily := f.Spend / daysInMonth; daily > 0 && bal < 3*daily && (alerted == 0 || alerted > 3) {
			crossed, why = 3, "fewer than 3 days of usage"
		}
	}
	zero := a.ZeroBalanceAt
	switch {
	case bal <= 0 && zero == nil:
		zero = &now
		s.notifyOrg(ctx, orgID, "billing.prepaid_empty", fmt.Sprintf("PGDock: %s's prepaid balance has run out", s.orgName(ctx, orgID)),
			fmt.Sprintf("The prepaid balance is %s. Top up within %d days to avoid restrictions; projects keep running meanwhile.\n\nTop up: %s/org/billing?org=%s\n",
				Naira(bal), GraceDays, s.publicURL, orgID))
	case bal > 0:
		zero = nil
	}
	next := alerted
	if crossed > 0 {
		next = crossed
		s.notifyOrg(ctx, orgID, "billing.prepaid_low", fmt.Sprintf("PGDock: %s's prepaid balance is low", s.orgName(ctx, orgID)),
			fmt.Sprintf("The prepaid balance of %s covers %s.\n\nTop up: %s/org/billing?org=%s\n", Naira(bal), why, s.publicURL, orgID))
	}
	return store.New(s.db).SetBalanceAlert(ctx, store.SetBalanceAlertParams{OrgID: orgID, BalanceAlerted: next, BalanceMonth: pgDate(month), ZeroBalanceAt: zero})
}

// chargeOnIssue debits the org's wallet for an issued postpaid invoice, if
// the org agreed to that (V3 §3.5: "charged automatically on invoice issue").
func (s *Service) chargeOnIssue(ctx context.Context, inv store.Invoice) {
	if s.wallets == nil || (inv.Status != StatusIssued && inv.Status != StatusPartiallyPaid) {
		return
	}
	if _, err := s.chargeInvoiceFromWallet(ctx, inv, false); err != nil {
		s.log.Warn("debiting a wallet for an invoice", "invoice", deref(inv.Number), "err", err)
	}
}

// Daily runs the once-a-day work: prepaid deductions and balance alerts,
// and dunning.
func (s *Service) Daily(ctx context.Context) error {
	accts, err := store.New(s.db).ListBillingAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range accts {
		if a.Mode != ModePrepaid || a.OrgStatus == "deleted" {
			continue
		}
		if _, err := s.DeductPrepaid(ctx, a.OrgID); err != nil {
			errs = append(errs, fmt.Errorf("prepaid %s: %w", a.OrgID, err))
			continue
		}
		if err := s.CheckPrepaid(ctx, a.OrgID); err != nil {
			errs = append(errs, fmt.Errorf("prepaid alerts %s: %w", a.OrgID, err))
		}
	}
	if err := s.SnapshotMRR(ctx); err != nil {
		errs = append(errs, fmt.Errorf("MRR snapshot: %w", err))
	}
	if err := s.RunDunning(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
