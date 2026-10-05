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
