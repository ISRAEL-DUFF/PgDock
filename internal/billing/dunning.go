package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Dunning states (V3 §3.8), on billing_accounts.dunning_state.
const (
	DunningOK         = "ok"
	DunningRetrying   = "retrying" // automatic card charges failing
	DunningOverdue    = "overdue"
	DunningRestricted = "restricted" // no new projects, branches or dedicated instances
	DunningSuspended  = "suspended"
)

// SuspendPrefix starts the suspension reason of an org suspended for
// non-payment, so paying reinstates only those.
const SuspendPrefix = "billing: "

// Suspender suspends and reinstates organisations (the tenancy service).
type Suspender interface {
	Suspend(ctx context.Context, orgID uuid.UUID, reason string) error
	Reinstate(ctx context.Context, orgID uuid.UUID) error
}

// Remover deletes an org's paid resources beyond Free (its dedicated
// projects), keeping final backups.
type Remover func(ctx context.Context, orgID uuid.UUID) (int, error)

// SetDunning configures how dunning suspends orgs and removes resources.
func (s *Service) SetDunning(sus Suspender, remove Remover) {
	s.suspender, s.remover = sus, remove
	s.onPaid = s.reevaluate
	s.onChargeFailed = s.cardFailed
}

// Card retries (V3 §3.8): the days after the first failure.
var cardRetryDays = []int{3, 5, 7}

// The overdue ladder: days after the due date (or after a prepaid org's
// grace ends).
type ladderStep struct {
	day   int
	step  string
	state string
}

var ladder = []ladderStep{
	{0, "overdue", DunningOverdue},
	{3, "restricted", DunningRestricted},
	{7, "final_notice", DunningRestricted},
	{10, "suspended", DunningSuspended},
	{40, "deletion_scheduled", DunningSuspended},
}

// DeletionNotice is the notice before resources are deleted (day 40 + 7).
const DeletionNotice = 7 * 24 * time.Hour

func daysSince(t, now time.Time) int { return int(now.Sub(t).Hours() / 24) }

func (s *Service) cardFailed(ctx context.Context, orgID uuid.UUID) {
	a, err := s.Account(ctx, orgID)
	if err != nil || a.CardFailingSince != nil {
		return
	}
	now := s.Now()
	q := store.New(s.db)
	_ = q.SetCardFailing(ctx, store.SetCardFailingParams{OrgID: orgID, CardFailingSince: &now})
	if a.DunningState == DunningOK {
		_ = q.SetDunning(ctx, store.SetDunningParams{OrgID: orgID, DunningState: DunningRetrying, DunningSince: &now})
	}
	s.notifyOrg(ctx, orgID, "billing.card_failed", fmt.Sprintf("PGDock: a payment for %s failed", s.orgName(ctx, orgID)),
		fmt.Sprintf("Charging the saved card failed. PGDock will try again in 3, 5 and 7 days. Update the card or pay another way: %s/org/billing?org=%s\n", s.publicURL, orgID))
}

// owing is when orgID's dunning cycle started, or zero if it owes nothing:
// a postpaid org's oldest unpaid invoice's due date, or the end of a
// prepaid org's zero-balance grace.
func (s *Service) owing(ctx context.Context, a store.BillingAccount, now time.Time) (time.Time, error) {
	if a.Mode == ModePrepaid {
		if a.ZeroBalanceAt == nil {
			return time.Time{}, nil
		}
		end := a.ZeroBalanceAt.Add(GraceDays * 24 * time.Hour)
		if end.After(now) {
			return time.Time{}, nil
		}
		return end, nil
	}
	since, err := store.New(s.db).OrgOverdueSince(ctx, store.OrgOverdueSinceParams{OrgID: a.OrgID, At: now})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && since == nil) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return *since, nil
}

// RunDunning walks every org that owes money, or is in a dunning state,
// along the ladder (V3 §3.8).
func (s *Service) RunDunning(ctx context.Context) error {
	q := store.New(s.db)
	accts, err := q.DunningAccounts(ctx, s.Now())
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range accts {
		if err := s.dunOrg(ctx, a.OrgID); err != nil {
			errs = append(errs, fmt.Errorf("org %s: %w", a.OrgID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) dunOrg(ctx context.Context, orgID uuid.UUID) error {
	now := s.Now()
	q := store.New(s.db)
	a, err := q.GetBillingAccount(ctx, orgID)
	if err != nil {
		return err
	}
	// Card retries on days 3, 5 and 7 after the first failure.
	if a.CardFailingSince != nil {
		for _, d := range cardRetryDays {
			if daysSince(*a.CardFailingSince, now) < d {
				break
			}
			step := fmt.Sprintf("card_retry_%d", d)
			if _, err := q.InsertDunningStep(ctx, store.InsertDunningStepParams{OrgID: orgID, Cycle: *a.CardFailingSince, Step: step}); errors.Is(err, pgx.ErrNoRows) {
				continue
			} else if err != nil {
				return err
			}
			if ok, err := s.retryCard(ctx, orgID); err != nil {
				return err
			} else if ok {
				return s.Reevaluate(ctx, orgID)
			}
			s.notifyOrg(ctx, orgID, "billing.card_failed", fmt.Sprintf("PGDock: a payment for %s failed again", s.orgName(ctx, orgID)),
				fmt.Sprintf("Retrying the saved card on day %d failed. Update the card or pay another way: %s/org/billing?org=%s\n", d, s.publicURL, orgID))
		}
	}
	since, err := s.owing(ctx, a, now)
	if err != nil {
		return err
	}
	if since.IsZero() {
		return s.Reevaluate(ctx, orgID)
	}
	if a.GraceUntil != nil && a.GraceUntil.After(now) {
		return nil // the admin extended its grace
	}
	o, err := q.GetOrg(ctx, orgID)
	if err != nil {
		return err
	}
	free := a.Plan == PlanFree
	link := fmt.Sprintf("%s/org/billing?org=%s", s.publicURL, orgID)
	for _, l := range ladder {
		if daysSince(since, now) < l.day {
			break
		}
		if free && (l.step == "suspended" || l.step == "deletion_scheduled") {
			break // Free-plan orgs are never suspended for billing
		}
		if _, err := q.InsertDunningStep(ctx, store.InsertDunningStepParams{OrgID: orgID, Cycle: since, Step: l.step}); errors.Is(err, pgx.ErrNoRows) {
			continue
		} else if err != nil {
			return err
		}
		deletion := a.DeletionScheduledAt
		subject, body := "", ""
		switch l.step {
		case "overdue":
			subject = fmt.Sprintf("PGDock: %s has an overdue balance", o.Name)
			body = "A payment is overdue. Please pay now: " + link
		case "restricted":
			subject = fmt.Sprintf("PGDock: %s can't create new resources until it pays", o.Name)
			body = "Payment is 3 days overdue: creating projects, branches and dedicated instances is blocked until it arrives. Existing databases keep running. Pay: " + link
		case "final_notice":
			subject = fmt.Sprintf("PGDock: final notice for %s", o.Name)
			body = "Payment is 7 days overdue. If it hasn't arrived in 3 days, the organisation will be suspended: its databases go offline (data is kept). Pay: " + link
		case "suspended":
			subject = fmt.Sprintf("PGDock: %s is suspended for non-payment", o.Name)
			body = "Payment is 10 days overdue, so the organisation is suspended: its databases are offline and its data is kept, with a final backup. Paying reinstates it at once: " + link
			if s.suspender != nil && o.Status == "active" {
				if err := s.suspender.Suspend(ctx, orgID, SuspendPrefix+"payment 10 days overdue"); err != nil {
					return err
				}
			}
		case "deletion_scheduled":
			at := now.Add(DeletionNotice)
			deletion = &at
			subject = fmt.Sprintf("PGDock: %s's paid resources will be deleted on %s", o.Name, at.Format("2 January 2006"))
			body = fmt.Sprintf("Payment is 40 days overdue. On %s, the organisation's resources beyond the Free plan (its dedicated projects) will be deleted; final backups are kept for 30 days. Paying before then cancels this: %s",
				at.Format("2 January 2006"), link)
		}
		if err := q.SetDunning(ctx, store.SetDunningParams{OrgID: orgID, DunningState: l.state, DunningSince: &since, DeletionScheduledAt: deletion}); err != nil {
			return err
		}
		a.DeletionScheduledAt = deletion
		s.notifyOrg(ctx, orgID, "billing.dunning."+l.step, subject, body+"\n")
	}
	// The deletion itself, after its notice.
	if a.DeletionScheduledAt != nil && !a.DeletionScheduledAt.After(now) {
		set, err := s.Settings(ctx)
		if err != nil {
			return err
		}
		if _, err := q.InsertDunningStep(ctx, store.InsertDunningStepParams{OrgID: orgID, Cycle: since, Step: "deleted"}); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if !set.DeleteForNonPayment || s.remover == nil {
			s.log.Warn("org due for deletion for non-payment; deletion is off, so it is left for the platform admin", "org", orgID)
			return nil
		}
		n, err := s.remover(ctx, orgID)
		if err != nil {
			return err
		}
		s.log.Info("deleted resources for non-payment", "org", orgID, "projects", n)
	}
	return nil
}

// retryCard charges each open invoice to the default method; true when
// everything owed is paid.
func (s *Service) retryCard(ctx context.Context, orgID uuid.UUID) (bool, error) {
	q := store.New(s.db)
	m, err := q.DefaultPaymentMethod(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var invs []store.Invoice
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		invs, err = store.New(tx).OpenInvoices(ctx, orgID)
		return err
	})
	if err != nil {
		return false, err
	}
	for _, inv := range invs {
		owed, err := outstanding(ctx, q, inv)
		if err != nil || owed <= 0 {
			continue
		}
		out, err := s.ChargeMethod(ctx, m, &inv.ID, owed)
		if err != nil {
			return false, err
		}
		if !out.Succeeded {
			return false, nil
		}
	}
	return true, nil
}

// reevaluate is Reevaluate as a hook after each payment.
func (s *Service) reevaluate(ctx context.Context, orgID uuid.UUID) {
	if err := s.Reevaluate(ctx, orgID); err != nil {
		s.log.Warn("billing: re-evaluating dunning", "org", orgID, "err", err)
	}
}

// Reevaluate ends an org's dunning once it owes nothing: restrictions
// lift and a suspension for non-payment is reversed.
func (s *Service) Reevaluate(ctx context.Context, orgID uuid.UUID) error {
	q := store.New(s.db)
	a, err := q.GetBillingAccount(ctx, orgID)
	if err != nil {
		return err
	}
	now := s.Now()
	if a.Mode == ModePrepaid && a.ZeroBalanceAt != nil {
		if bal, err := s.CreditAvailable(ctx, s.db, orgID); err == nil && bal > 0 {
			if err := q.SetBalanceAlert(ctx, store.SetBalanceAlertParams{OrgID: orgID, BalanceAlerted: a.BalanceAlerted, BalanceMonth: a.BalanceMonth}); err != nil {
				return err
			}
			a.ZeroBalanceAt = nil
		}
	}
	since, err := s.owing(ctx, a, now)
	if err != nil || !since.IsZero() {
		return err
	}
	open := false
	if a.CardFailingSince != nil {
		var invs []store.Invoice
		_ = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var err error
			invs, err = store.New(tx).OpenInvoices(ctx, orgID)
			return err
		})
		open = len(invs) > 0
		if !open {
			if err := q.SetCardFailing(ctx, store.SetCardFailingParams{OrgID: orgID}); err != nil {
				return err
			}
		}
	}
	if a.DunningState == DunningOK || (a.DunningState == DunningRetrying && open) {
		return nil
	}
	if err := q.SetDunning(ctx, store.SetDunningParams{OrgID: orgID, DunningState: DunningOK}); err != nil {
		return err
	}
	o, err := q.GetOrg(ctx, orgID)
	if err != nil {
		return err
	}
	if o.Status == "suspended" && o.SuspendedReason != nil && strings.HasPrefix(*o.SuspendedReason, SuspendPrefix) && s.suspender != nil {
		if err := s.suspender.Reinstate(ctx, orgID); err != nil {
			return err
		}
	}
	if a.DunningState != DunningRetrying {
		msg := "Payment received: restrictions are lifted."
		if a.DunningState == DunningSuspended {
			msg = "Payment received: restrictions are lifted and the organisation is reinstated."
		}
		s.notifyOrg(ctx, orgID, "billing.dunning.cleared", fmt.Sprintf("PGDock: thank you, %s is paid up", o.Name), msg+"\n")
	}
	return nil
}

// ExtendGrace holds dunning for orgID until until (the platform admin).
func (s *Service) ExtendGrace(ctx context.Context, orgID uuid.UUID, until *time.Time) error {
	if until != nil && until.Before(s.Now()) {
		return invalid("grace must end in the future")
	}
	if _, err := s.Account(ctx, orgID); err != nil {
		return err
	}
	return store.New(s.db).SetGrace(ctx, store.SetGraceParams{OrgID: orgID, GraceUntil: until})
}
