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

func TestForecastBudgetAndSpendCap(t *testing.T) {
	s, db, clk, sent := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	if _, err := s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	clk.t = day(1)
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	// Half of October recorded: 2 vCPUs for 15 days.
	usage(t, db, org, uuid.New(), tenancy.MetricDedicatedCPU, day(1), day(16), "2")
	clk.t = day(16).Add(10 * time.Minute)
	putWatermark(t, db, day(16))

	f, err := s.Forecast(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	// 720 vCPU-hours in 360 of 744 hours project to 1,488, at 2,740 kobo;
	// plus October's Pro fee (prorated from the 1st).
	if f.Usage != 4_077_120 || f.Spend != 1_500_000+4_077_120 || f.Elapsed.String() != billing.D("360").Quo(billing.D("744")).String() {
		t.Fatalf("forecast %+v", f)
	}

	// A budget of ₦50,000: the forecast is past 100%; one email.
	budget, limit := int64(5_000_000), int64(4_000_000)
	if _, err := s.UpdateDetails(ctx, org, billing.Details{BudgetMinor: &budget, SpendCapMinor: &limit}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshForecast(ctx, org); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshForecast(ctx, org); err != nil {
		t.Fatal(err)
	}
	var budgetMails, capMails int
	for _, m := range sent.msgs {
		switch {
		case strings.Contains(m.Subject, "100% of its budget"):
			budgetMails++
		case strings.Contains(m.Subject, "spend cap"):
			capMails++
		}
	}
	if budgetMails != 1 || capMails != 1 {
		t.Fatalf("budget emails %d, cap emails %d: %+v", budgetMails, capMails, sent.msgs)
	}
	q := store.New(db)
	if capped, _ := q.OrgSpendCapped(ctx, org); !capped {
		t.Fatal("usage of ₦40,771 is over a ₦40,000 cap but the org isn't capped")
	}
	a, _ := s.Account(ctx, org)
	if a.ForecastMinor == nil || *a.ForecastMinor != f.Spend || a.BudgetAlerted != 100 {
		t.Errorf("stored forecast %v, alerted %d", a.ForecastMinor, a.BudgetAlerted)
	}

	// Raising the cap lifts it.
	limit = 5_000_000
	if _, err := s.UpdateDetails(ctx, org, billing.Details{BudgetMinor: &budget, SpendCapMinor: &limit}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshForecast(ctx, org); err != nil {
		t.Fatal(err)
	}
	if capped, _ := q.OrgSpendCapped(ctx, org); capped {
		t.Error("still capped under a higher cap")
	}

	// A new month starts over: alerts can fire again.
	clk.t = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	putWatermark(t, db, clk.t)
	if _, err := s.RefreshForecast(ctx, org); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, org); a.BudgetAlerted != 0 || a.BudgetMonth.Time.Month() != time.November {
		t.Errorf("November: alerted %d, month %v", a.BudgetAlerted, a.BudgetMonth.Time)
	}
}

func TestEstimateDedicated(t *testing.T) {
	s, db, _, _ := newService(t)
	org := newOrg(t, db, "acme")
	e, err := s.EstimateDedicated(context.Background(), org, billing.EstimateRequest{
		CPUs: billing.D("2"), MemoryMB: 4096, DiskGB: 40, HA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 2 × 2,740 + 4.096 × 685 + 40 × 34.25 = 9,655.76 an hour, twice with
	// the standby, plus 20% of the standby: 21,242.672 an hour.
	if e.HourlyMinor != 21_243 || e.MonthlyMinor != 15_507_151 {
		t.Errorf("estimate %d an hour, %d a month", e.HourlyMinor, e.MonthlyMinor)
	}
	only, _ := s.EstimateDedicated(context.Background(), org, billing.EstimateRequest{CPUs: billing.D("2"), MemoryMB: 4096, DiskGB: 40, StandbyOnly: true, Sync: true})
	// What enabling HA adds: the standby, its premium, and sync replication.
	if want := billing.D("9655.76").Mul(billing.D("1.2")).Add(billing.D("1370")).Frac(730, 1).Round(); only.MonthlyMinor != want {
		t.Errorf("HA on a running instance: %d a month, want %d", only.MonthlyMinor, want)
	}
}

func TestEstimateAddOns(t *testing.T) {
	s, db, _, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	// Add-ons alone: 14-day PITR and long retention, an hour each.
	e, err := s.EstimateDedicated(ctx, org, billing.EstimateRequest{PITRDays: 14, Retention: "long"})
	if err != nil {
		t.Fatal(err)
	}
	if e.HourlyMinor != 685+411 || len(e.Lines) != 2 {
		t.Errorf("add-ons: %d an hour, lines %+v", e.HourlyMinor, e.Lines)
	}
	for _, bad := range []billing.EstimateRequest{{PITRDays: 10}, {Retention: "forever"}} {
		if _, err := s.EstimateDedicated(ctx, org, bad); err == nil {
			t.Errorf("%+v: estimated", bad)
		}
	}
	// A region without a premium in the default book adds nothing.
	plain, _ := s.EstimateDedicated(ctx, org, billing.EstimateRequest{CPUs: billing.D("2"), MemoryMB: 4096, DiskGB: 40})
	lagos, _ := s.EstimateDedicated(ctx, org, billing.EstimateRequest{CPUs: billing.D("2"), MemoryMB: 4096, DiskGB: 40, Region: "ng-lagos"})
	if plain.HourlyMinor != lagos.HourlyMinor {
		t.Errorf("no premium set, yet Lagos is %d against %d", lagos.HourlyMinor, plain.HourlyMinor)
	}
}

func TestAddOnRating(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	clk.t = day(1)
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	p := uuid.New()
	// Ten days of 30-day PITR, the whole month of extended retention.
	usage(t, db, org, p, tenancy.MetricPITR30Hours, day(1), day(11), "1")
	usage(t, db, org, p, tenancy.MetricRetentionExtendedHours, day(1), day(1).AddDate(0, 1, 0), "1")
	r, err := s.Rate(ctx, org, day(31))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"30-day point-in-time recovery": 240 * 1644,
		"Extended backup retention":     billing.D("744").Mul(billing.D("205.5")).Round(),
	}
	for _, l := range r.Lines {
		for prefix, amount := range want {
			if strings.HasPrefix(l.Description, prefix) {
				if l.Amount != amount || l.Revenue != billing.RevenueAddons {
					t.Errorf("%s: %d to %s, want %d", l.Description, l.Amount, l.Revenue, amount)
				}
				delete(want, prefix)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("missing lines %v in %+v", want, r.Lines)
	}
}
