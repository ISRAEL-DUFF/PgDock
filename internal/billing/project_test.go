package billing_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// TestProjectMonth splits an allowance's overage between the projects by
// their usage, and gives each its own lines in full (V4.1 §9.3).
func TestProjectMonth(t *testing.T) {
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	clk.t = day(1)
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	a, b := uuid.New(), uuid.New()
	// 8M requests against Pro's 5M: 3M over at ₦3 per 1,000, ₦9,000.
	usage(t, db, org, a, tenancy.MetricAPIRequests, day(2), day(2).Add(10*time.Hour), "600000")
	usage(t, db, org, b, tenancy.MetricAPIRequests, day(2), day(2).Add(10*time.Hour), "200000")
	usage(t, db, org, a, tenancy.MetricDedicatedCPU, day(2), day(2).Add(10*time.Hour), "1")
	clk.t = day(3)

	pa, err := s.ProjectMonth(ctx, org, a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := s.ProjectMonth(ctx, org, b)
	if err != nil {
		t.Fatal(err)
	}
	if pa.ByService["data_api"] != 675_000 || pb.ByService["data_api"] != 225_000 {
		t.Fatalf("data API overage: a %d, b %d", pa.ByService["data_api"], pb.ByService["data_api"])
	}
	if pa.ByService["database"] != 27_400 || pb.ByService["database"] != 0 {
		t.Fatalf("dedicated: a %d, b %d", pa.ByService["database"], pb.ByService["database"])
	}
	if pa.ByService["plan"] != 0 {
		t.Fatalf("the plan fee went to a project: %d", pa.ByService["plan"])
	}
	if inc := pa.Included[tenancy.MetricAPIRequests]; inc.String() != "5000000" {
		t.Fatalf("included requests %s", inc)
	}
}
