package billing

import (
	"context"
	"encoding/json"
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

// AutoTopup tops up a prepaid balance from the default saved method.
type AutoTopup struct {
	BelowMinor  int64 `json:"below_minor"`
	AmountMinor int64 `json:"amount_minor"`
}

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

// CheckPrepaid sends a prepaid org's balance alerts, starts or clears its
// zero-balance grace, and tops it up automatically when it is set to.
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
	if err := store.New(s.db).SetBalanceAlert(ctx, store.SetBalanceAlertParams{OrgID: orgID, BalanceAlerted: next, BalanceMonth: pgDate(month), ZeroBalanceAt: zero}); err != nil {
		return err
	}
	return s.autoTopup(ctx, a, bal)
}

// autoTopup charges the default saved method when the balance is below the
// org's threshold, at most once a day.
func (s *Service) autoTopup(ctx context.Context, a store.BillingAccount, bal int64) error {
	if len(a.AutoTopup) == 0 {
		return nil
	}
	var at AutoTopup
	if err := json.Unmarshal(a.AutoTopup, &at); err != nil || at.AmountMinor <= 0 || bal >= at.BelowMinor {
		return nil
	}
	q := store.New(s.db)
	recent, err := q.RecentAutoTopup(ctx, store.RecentAutoTopupParams{OrgID: a.OrgID, Since: s.Now().Add(-24 * time.Hour)})
	if err != nil || recent {
		return err
	}
	m, err := q.DefaultPaymentMethod(ctx, a.OrgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	out, err := s.ChargeMethod(ctx, m, nil, at.AmountMinor)
	if err != nil {
		return err
	}
	if !out.Succeeded && !out.Unavailable {
		s.notifyOrg(ctx, a.OrgID, "billing.topup_failed", "PGDock: an automatic top-up failed",
			fmt.Sprintf("Topping up %s from the saved payment method failed: %s.\n\nTop up another way: %s/org/billing?org=%s\n", Naira(at.AmountMinor), out.Message, s.publicURL, a.OrgID))
	}
	return nil
}

// SetAutoTopup sets (or, with nil, clears) an org's auto top-up.
func (s *Service) SetAutoTopup(ctx context.Context, orgID uuid.UUID, at *AutoTopup) error {
	if at == nil {
		return store.New(s.db).SetAutoTopup(ctx, store.SetAutoTopupParams{OrgID: orgID})
	}
	if at.AmountMinor < 1000_00 || at.BelowMinor < 0 {
		return invalid("an automatic top-up is at least ₦1,000, below a threshold of ₦0 or more")
	}
	raw, _ := json.Marshal(at)
	return store.New(s.db).SetAutoTopup(ctx, store.SetAutoTopupParams{OrgID: orgID, AutoTopup: raw})
}

// chargeOnIssue charges an issued postpaid invoice to the org's default
// saved card or mandate (V3 §3.5: "charged automatically on invoice
// issue").
func (s *Service) chargeOnIssue(ctx context.Context, inv store.Invoice) {
	if inv.Status != StatusIssued && inv.Status != StatusPartiallyPaid {
		return
	}
	q := store.New(s.db)
	m, err := q.DefaultPaymentMethod(ctx, inv.OrgID)
	if err != nil {
		return
	}
	owed, err := outstanding(ctx, q, inv)
	if err != nil || owed <= 0 {
		return
	}
	out, err := s.ChargeMethod(ctx, m, &inv.ID, owed)
	switch {
	case err != nil:
		s.log.Warn("charging an invoice", "invoice", deref(inv.Number), "err", err)
	case !out.Succeeded && !out.Unavailable && s.onChargeFailed != nil:
		s.onChargeFailed(ctx, inv.OrgID)
	}
}

// CardReminders emails 30 and 7 days before a saved card expires, and
// retires expired cards.
func (s *Service) CardReminders(ctx context.Context) error {
	q := store.New(s.db)
	cards, err := q.ExpiringCards(ctx)
	if err != nil {
		return err
	}
	now := s.Now()
	for _, c := range cards {
		// A card works through the last day of its expiry month.
		end := time.Date(int(*c.ExpYear), time.Month(*c.ExpMonth)+1, 1, 0, 0, 0, 0, time.UTC)
		left := end.Sub(now)
		switch {
		case left <= 0:
			if err := q.SetPaymentMethodStatus(ctx, store.SetPaymentMethodStatusParams{ID: c.ID, Status: "expired"}); err != nil {
				return err
			}
		case left <= 7*24*time.Hour && c.RemindedDays != 7, left <= 30*24*time.Hour && c.RemindedDays == 0:
			days := int32(30)
			if left <= 7*24*time.Hour {
				days = 7
			}
			s.notifyOrg(ctx, c.OrgID, "billing.card_expiring", "PGDock: a saved card expires soon",
				fmt.Sprintf("The %s card ending %s expires at the end of %02d/%d. Add a new card so invoices keep being paid: %s/org/billing?org=%s\n",
					deref(c.Brand), deref(c.Last4), *c.ExpMonth, *c.ExpYear, s.publicURL, c.OrgID))
			if err := q.SetCardReminded(ctx, store.SetCardRemindedParams{ID: c.ID, Days: days}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Daily runs the once-a-day work: prepaid deductions, balance alerts and
// auto top-ups, card expiry reminders, and dunning.
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
	if err := s.CardReminders(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := s.SnapshotMRR(ctx); err != nil {
		errs = append(errs, fmt.Errorf("MRR snapshot: %w", err))
	}
	if err := s.RunDunning(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
