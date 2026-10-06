package billing_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/billing"
)

// MRR and its movements from daily snapshots: a new subscription, an
// upgrade, a conversion from Free, and a cancellation (V3 §7.2).
func TestRevenueMovements(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	stay, grow, convert, leave := newOrg(t, db, "stay"), newOrg(t, db, "grow"), newOrg(t, db, "convert"), newOrg(t, db, "leave")
	plan := func(org [16]byte, p string) {
		t.Helper()
		if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: p, Immediately: true}); err != nil {
			t.Fatal(err)
		}
	}
	// September: three on Pro, one on Free.
	clk.t = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for _, o := range [][16]byte{stay, grow, leave} {
		plan(o, billing.PlanPro)
	}
	for _, o := range [][16]byte{stay, grow, leave, convert} {
		if _, err := s.Account(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SnapshotMRR(ctx); err != nil {
		t.Fatal(err)
	}
	// October: one upgrades, one converts, one leaves.
	clk.t = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	plan(grow, billing.PlanTeam)
	plan(convert, billing.PlanPro)
	plan(leave, billing.PlanFree)
	if err := s.SnapshotMRR(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := s.Revenue(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Months) != 2 {
		t.Fatalf("months: %+v", r.Months)
	}
	pro, team := int64(1_500_000), int64(6_000_000)
	sep, oct := r.Months[0], r.Months[1]
	if sep.Month != "2026-09" || sep.MRR != 3*pro || sep.Paying != 3 || sep.New != 3*pro {
		t.Errorf("September: %+v", sep)
	}
	want := billing.RevenueMonth{Month: "2026-10", MRR: 2*pro + team, ARR: 12 * (2*pro + team), New: pro, Expansion: team - pro, Churned: pro,
		Paying: 3, ARPA: (2*pro + team) / 3, Conversions: 1, FreeOrgs: 1}
	oct.UsageRevenue, oct.Invoiced, oct.InvoicesCount, oct.Collected = 0, 0, 0, 0
	if oct != want {
		t.Errorf("October:\n got %+v\nwant %+v", oct, want)
	}
	var b bytes.Buffer
	if err := billing.WriteRevenueCSV(&b, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "2026-10,90000.00,1080000.00,15000.00,45000.00,0.00,15000.00,3,30000.00,1,1") {
		t.Errorf("CSV:\n%s", b.String())
	}
}
