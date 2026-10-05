package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
)

// Forecast is an org's projected spend for the current month (V3 §3.10),
// before VAT: its plan fee, plan changes, and usage so far extrapolated to
// the whole month.
type Forecast struct {
	Month time.Time
	// Spend is the month's projected cost.
	Spend int64
	// Usage is the projected usage charges: overage, dedicated, add-ons.
	// The spend cap applies to these.
	Usage int64
	// Elapsed is the share of the month whose usage is recorded.
	Elapsed Dec
}

// budgetThresholds are the budget alerts' percentages (V3 §3.10).
var budgetThresholds = []int32{50, 80, 100}

// Forecast projects orgID's current month.
func (s *Service) Forecast(ctx context.Context, orgID uuid.UUID) (Forecast, error) {
	now := s.Now().UTC()
	mStart := MonthStart(now)
	mEnd := mStart.AddDate(0, 1, 0)
	through, err := s.usageRecordedThrough(ctx)
	if err != nil {
		return Forecast{}, err
	}
	if through.IsZero() || through.After(now) {
		through = now
	}
	elapsed := through.Sub(mStart)
	total := mEnd.Sub(mStart)
	f := Forecast{Month: mStart, Elapsed: DecInt(int64(elapsed / time.Hour)).Quo(DecInt(int64(total / time.Hour)))}
	scale := Dec{}
	if elapsed >= time.Hour {
		scale = DecInt(int64(total / time.Hour)).Quo(DecInt(int64(elapsed / time.Hour)))
	}
	r, err := s.rate(ctx, orgID, mStart, nil, scale)
	if err != nil {
		return Forecast{}, err
	}
	f.Spend = r.Subtotal - r.NextFee + r.MonthFee
	for _, l := range r.Lines {
		switch l.Kind {
		case KindOverage, KindDedicated, KindAddon:
			f.Usage += l.Amount
		}
	}
	return f, nil
}

// RefreshForecast projects orgID's month, stores it, sends the budget alerts
// it crosses, and sets or lifts the spend cap.
func (s *Service) RefreshForecast(ctx context.Context, orgID uuid.UUID) (Forecast, error) {
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return Forecast{}, err
	}
	f, err := s.Forecast(ctx, orgID)
	if err != nil {
		return f, err
	}
	alerted := a.BudgetAlerted
	if !a.BudgetMonth.Valid || !a.BudgetMonth.Time.Equal(f.Month) {
		alerted = 0 // a new month
	}
	var crossed int32
	if a.BudgetMinor != nil && *a.BudgetMinor > 0 {
		for _, t := range budgetThresholds {
			if t > alerted && f.Spend*100 >= *a.BudgetMinor*int64(t) {
				crossed = t
			}
		}
	}
	capped := a.SpendCapMinor != nil && f.Usage >= *a.SpendCapMinor
	now := s.Now()
	next := alerted
	if crossed > 0 {
		next = crossed
	}
	if err := store.New(s.db).UpdateForecast(ctx, store.UpdateForecastParams{
		OrgID: orgID, ForecastMinor: &f.Spend, ForecastAt: &now, Capped: capped, BudgetAlerted: next, BudgetMonth: pgDate(f.Month),
	}); err != nil {
		return f, err
	}
	month := f.Month.Format("January 2006")
	if crossed > 0 {
		s.notifyOrg(ctx, orgID, "billing.budget",
			fmt.Sprintf("PGDock: %s's spend is forecast at %d%% of its budget", s.orgName(ctx, orgID), crossed),
			fmt.Sprintf("The forecast for %s is %s before VAT, %d%% of the monthly budget of %s.\n\nSee the forecast and what drives it: %s/orgs/%s/billing\n",
				month, Naira(f.Spend), crossed, Naira(*a.BudgetMinor), s.publicURL, orgID))
	}
	switch {
	case capped && !a.Capped:
		s.notifyOrg(ctx, orgID, "billing.spend_cap",
			fmt.Sprintf("PGDock: %s has reached its spend cap", s.orgName(ctx, orgID)),
			fmt.Sprintf("Usage charges for %s are forecast at %s, at or above the spend cap of %s.\n\n"+
				"Until the cap is raised or next month starts, new branches, dedicated instances and HA are refused, webhook deliveries queue, and scheduled jobs are skipped. "+
				"Databases stay connected and no data is deleted.\n\nChange the cap: %s/orgs/%s/billing\n",
				month, Naira(f.Usage), Naira(*a.SpendCapMinor), s.publicURL, orgID))
	case !capped && a.Capped:
		s.log.Info("spend cap lifted", "org", orgID)
	}
	return f, nil
}

// RefreshForecasts refreshes every org's forecast.
func (s *Service) RefreshForecasts(ctx context.Context) error {
	accts, err := store.New(s.db).ListBillingAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range accts {
		if a.OrgStatus == "deleted" {
			continue
		}
		if _, err := s.RefreshForecast(ctx, a.OrgID); err != nil {
			errs = append(errs, fmt.Errorf("org %s: %w", a.OrgID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) notifyOrg(ctx context.Context, orgID uuid.UUID, event, subject, body string) {
	if s.mail == nil {
		return
	}
	to, err := store.New(s.db).BillingRecipients(ctx, orgID)
	if err != nil || len(to) == 0 {
		return
	}
	if err := s.mail.Send(ctx, mail.Message{To: to, Subject: subject, Body: body, Headers: map[string]string{"X-PGDock-Event": event}}); err != nil {
		s.log.Warn("billing email", "org", orgID, "event", event, "err", err)
	}
}

// ErrSpendCap refuses a new billable resource while the org is capped.
var ErrSpendCap = errors.New("the organisation has reached its spend cap: new billable resources are paused until the cap is raised on the Billing page or next month starts")

// Estimate is the cost of running a dedicated resource (V3 §3.10: shown
// before every billable action).
type Estimate struct {
	HourlyMinor  int64
	MonthlyMinor int64 // 730 hours
	Lines        []Line
}

// EstimateRequest describes what would run.
type EstimateRequest struct {
	CPUs     Dec
	MemoryMB int64
	DiskGB   int64
	// HA adds a standby of the same size and the premium; Sync adds
	// synchronous replication.
	HA   bool
	Sync bool
	// StandbyOnly prices only what HA adds (enabling HA on a running
	// instance).
	StandbyOnly bool
}

// HoursPerMonth is what monthly estimates assume.
const HoursPerMonth = 730

// EstimateDedicated prices r at orgID's price book.
func (s *Service) EstimateDedicated(ctx context.Context, orgID uuid.UUID, r EstimateRequest) (Estimate, error) {
	if r.CPUs.Sign() < 0 || r.MemoryMB < 0 || r.DiskGB < 0 {
		return Estimate{}, invalid("sizes can't be negative")
	}
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return Estimate{}, err
	}
	b, err := s.GetBook(ctx, a.PriceBookVersion)
	if err != nil {
		return Estimate{}, err
	}
	p := b.Prices
	ram := DecInt(r.MemoryMB).Quo(DecInt(1000))
	disk := DecInt(r.DiskGB)
	var e Estimate
	hourly := Dec{}
	add := func(desc string, qty, unit Dec) Dec {
		v := qty.Mul(unit)
		if v.Sign() > 0 {
			e.Lines = append(e.Lines, Line{Kind: KindDedicated, Description: desc, Quantity: qty.Frac(HoursPerMonth, 1), UnitPrice: unit, Amount: v.Frac(HoursPerMonth, 1).Round()})
		}
		hourly = hourly.Add(v)
		return v
	}
	if !r.StandbyOnly {
		add("vCPU-hours", r.CPUs, p.Dedicated.VCPUHour)
		add("RAM GB-hours", ram, p.Dedicated.RAMGBHour)
		add("Disk GB-hours", disk, p.Dedicated.DiskGBHour)
	}
	if r.HA || r.StandbyOnly {
		standby := add("HA standby vCPU-hours", r.CPUs, p.Dedicated.VCPUHour).
			Add(add("HA standby RAM GB-hours", ram, p.Dedicated.RAMGBHour)).
			Add(add("HA standby disk GB-hours", disk, p.Dedicated.DiskGBHour))
		add("HA premium", standby, p.AddOns.HAPremiumPercent.Frac(1, 100))
	}
	if r.Sync {
		add("Synchronous replication hours", DecInt(1), p.AddOns.SyncReplicationHour)
	}
	e.HourlyMinor = hourly.Round()
	e.MonthlyMinor = hourly.Frac(HoursPerMonth, 1).Round()
	return e, nil
}
