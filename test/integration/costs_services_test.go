package integration

import (
	"context"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/cloud"
	"github.com/israel-duff/pgdock/internal/costs"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestCostAttributionBackendServices is V4.1-M10's done-when (V4.1 §11):
// each organisation's edge, files and messages cost matches a calculation
// by hand, the margins show them per service, and a region's edges busy for
// an hour raise an edge-tier proposal that provisions an edge node.
func TestCostAttributionBackendServices(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	if _, err := e.Billing.ChangePlan(ctx, e.OrgID, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	a := e.CreateProject("A")
	freeOrg := e.CreateOrg("Hobby")
	b := e.CreateProjectIn("B", freeOrg)
	var region string
	if err := e.DB.QueryRow(ctx, `SELECT region FROM projects WHERE id = $1`, a.Project.Id).Scan(&region); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	if code := e.Do("POST", "/api/v1/admin/fx-rates", gen.FXRateInput{Currency: "EUR", NgnPerUnit: 1700, EffectiveAt: ptr(day.AddDate(0, 0, -10))}, nil); code != http.StatusCreated {
		t.Fatalf("fx rate: %d", code)
	}

	// A dedicated edge node at €30 a month, and the shared node at €40 of
	// which a quarter is moved to the edge category.
	var en gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "edge-1", PrivateAddr: "10.0.0.50",
		Role: gen.CreateNodeRequestRoleEdge, Region: &region}, &en); code != http.StatusCreated {
		t.Fatalf("edge node: %d", code)
	}
	sharedID := mustNodeID(t, e, "test")
	for id, cost := range map[uuid.UUID]int64{en.Node.Id: 3000, sharedID: 4000} {
		if code := e.Do("PUT", "/api/v1/nodes/"+id.String()+"/cost", gen.NodeCost{MonthlyCostMinor: ptr(cost), Currency: "EUR"}, nil); code != http.StatusOK {
			t.Fatalf("node cost: %d", code)
		}
		if _, err := e.DB.Exec(ctx, `UPDATE nodes SET capacity = '{"cpus": 4}', created_at = now() - interval '30 days' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	var cs gen.CostSettings
	if code := e.Do("GET", "/api/v1/admin/costs/settings", nil, &cs); code != http.StatusOK {
		t.Fatalf("cost settings: %d", code)
	}
	cs.EdgeSharePercent, cs.FloatingIps = ptr(float32(25)), 0
	if code := e.Do("PUT", "/api/v1/admin/costs/settings", cs, nil); code != http.StatusOK {
		t.Fatalf("cost settings: %d", code)
	}

	// A day's backend services: A makes 3,000 requests and B 1,000, both
	// 200 realtime minutes; A stores and serves files and sends codes.
	use := func(org, project uuid.UUID, metric string, qty float64) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, `INSERT INTO usage_records (org_id, project_id, metric, granularity, period_start, quantity, plan_id)
			VALUES ($1, $2, $3, 'day', $4, $5, (SELECT plan_id FROM organizations WHERE id = $1))`, org, project, metric, day.Add(time.Hour), qty); err != nil {
			t.Fatal(err)
		}
	}
	use(e.OrgID, a.Project.Id, "api_requests", 3000)
	use(freeOrg, b.Project.Id, "api_requests", 1000)
	use(e.OrgID, a.Project.Id, "realtime_connection_minutes", 200)
	use(freeOrg, b.Project.Id, "realtime_connection_minutes", 200)
	use(e.OrgID, a.Project.Id, "storage_gb_hours", 720)
	use(e.OrgID, a.Project.Id, "storage_egress_gb", 5)
	use(e.OrgID, a.Project.Id, "messages_sms_cost_kobo", 85000) // ₦850 at the provider

	if code := e.Do("POST", "/api/v1/admin/costs/attribute", gen.AttributeRequest{From: date(day), To: date(day)}, nil); code != http.StatusOK {
		t.Fatalf("attribute: %d", code)
	}

	// By hand, in euro cents.
	dm := float64(time.Date(day.Year(), day.Month()+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Day())
	edgePool := 3000/dm + 4000/dm*0.25
	shareA, shareB := 0.5*0.75+0.5*0.5, 0.5*0.25+0.5*0.5 // requests 3:1, minutes 1:1
	edgeA, edgeB := edgePool*shareA, edgePool*shareB
	filesA := costs.DefaultSettings().ObjectStorageGBMonth*720/(24*dm) + costs.DefaultSettings().EgressGB*5
	messagesA := 85000.0 / 1700 // 50 cents
	allocated := func(org uuid.UUID) map[string]float64 {
		t.Helper()
		rows, err := e.DB.Query(ctx, `SELECT category, sum(amount_minor)::float8 FROM cost_allocations WHERE org_id = $1 AND day = $2 AND currency = 'EUR' GROUP BY category`,
			org, day)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]float64{}
		for rows.Next() {
			var c string
			var v float64
			if err := rows.Scan(&c, &v); err != nil {
				t.Fatal(err)
			}
			out[c] = v
		}
		return out
	}
	near := func(what string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 0.01 {
			t.Errorf("%s: %.4f cents, by hand %.4f", what, got, want)
		}
	}
	ca, cb := allocated(e.OrgID), allocated(freeOrg)
	near("A's edge", ca[costs.CatEdge], edgeA)
	near("B's edge", cb[costs.CatEdge], edgeB)
	near("A's files", ca[costs.CatFiles], filesA)
	near("A's messages", ca[costs.CatMessages], messagesA)
	if cb[costs.CatFiles] != 0 || cb[costs.CatMessages] != 0 {
		t.Errorf("B's files and messages: %+v", cb)
	}
	// What the edge didn't take of the shared node is idle (no database use).
	var idle float64
	if err := e.DB.QueryRow(ctx, `SELECT coalesce(sum(amount_minor), 0)::float8 FROM cost_allocations WHERE day = $1 AND category = 'idle'`, day).Scan(&idle); err != nil {
		t.Fatal(err)
	}
	near("idle", idle, 4000/dm*0.75)

	// The margins, per service, in naira at ₦1,700.
	var m gen.Margins
	if code := e.Do("GET", "/api/v1/admin/costs?month="+day.Format("2006-01"), nil, &m); code != http.StatusOK || m.Services == nil {
		t.Fatalf("costs: %d %+v", code, m.Services)
	}
	kobo := func(cents float64) int64 { return int64(math.Round(cents * 1700)) }
	services := map[gen.ServiceMarginService]gen.ServiceMargin{}
	for _, s := range *m.Services {
		services[s.Service] = s
	}
	for svc, want := range map[gen.ServiceMarginService]int64{
		gen.ServiceMarginServiceApi: kobo(edgeA + edgeB), gen.ServiceMarginServiceFiles: kobo(filesA), gen.ServiceMarginServiceMessages: kobo(messagesA),
	} {
		if d := services[svc].CostMinor - want; d < -2 || d > 2 {
			t.Errorf("%s cost %d, by hand %d", svc, services[svc].CostMinor, want)
		}
	}
	if db := services[gen.ServiceMarginServiceDatabase]; db.RevenueMinor <= 0 || db.MarginMinor != db.RevenueMinor-db.CostMinor {
		t.Errorf("database service: %+v", db)
	}
	if code, csv := e.GetText("/api/v1/admin/costs?format=csv&month="+day.Format("2006-01"), nil, true); code != http.StatusOK ||
		!strings.Contains(csv, "service,revenue_ngn") || !strings.Contains(csv, "messages,") {
		t.Errorf("CSV: %d %.400s", code, csv)
	}

	// ---- Edge capacity: an hour above 70% proposes an edge node ------------
	sample := func(region string, cpu float32, ago time.Duration) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, `INSERT INTO edge_cpu_samples (edge, region, at, cpu_percent) VALUES ('edge-a', $1, now() - make_interval(secs => $2), $3)`,
			region, ago.Seconds(), cpu); err != nil {
			t.Fatal(err)
		}
	}
	for m := 0; m < 60; m++ {
		sample(region, 85, time.Duration(m)*time.Minute)
		cpu := float32(85)
		if m == 30 {
			cpu = 40 // one quiet stretch: no proposal there
		}
		sample("ng-quiet", cpu, time.Duration(m)*time.Minute)
	}
	made, err := e.Capacity.Evaluate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var p store.CapacityProposal
	for _, x := range made {
		if x.Tier == "edge" {
			if x.Region != region || p.ID != uuid.Nil {
				t.Fatalf("edge proposals: %+v", made)
			}
			p = x
		}
	}
	if p.ID == uuid.Nil || !strings.Contains(p.Reason, "70%") || p.Status != "pending" {
		t.Fatalf("no edge proposal for %s: %+v", region, made)
	}
	if again, _ := e.Capacity.Evaluate(ctx); len(again) != 0 {
		t.Fatalf("a second evaluation proposed again: %+v", again)
	}

	// Approved, it creates a server whose cloud-init runs pgdock-edge, and
	// its node joins as an edge node.
	var started gen.CapacityProposal
	if code := e.Do("POST", "/api/v1/admin/capacity/proposals/"+p.ID.String()+"/approve", nil, &started); code != http.StatusOK || started.OperationId == nil {
		t.Fatalf("approve: %d %+v", code, started)
	}
	var srv cloud.FakeServer
	waitFor(t, 30*time.Second, "the edge server", func() bool {
		if live := e.Hetzner.Live(); len(live) == 1 {
			srv = live[0]
			return true
		}
		return false
	})
	if !strings.Contains(srv.UserData, "--name pgdock-edge") || !strings.Contains(srv.UserData, "PGDOCK_EDGE_SECRET="+testenv.EdgeSecret) ||
		!strings.Contains(srv.UserData, "PGDOCK_EDGE_REGION="+region) {
		t.Fatalf("the edge server's cloud-init:\n%s", srv.UserData)
	}
	waitFor(t, 10*time.Second, "the node's server", func() bool {
		n, err := store.New(e.DB).GetNodeByName(ctx, srv.Name)
		if err != nil || n.ProviderServerID == nil {
			return false
		}
		_, err = e.DB.Exec(ctx, `UPDATE nodes SET private_addr = 'agent-test-2' WHERE id = $1`, n.ID)
		return err == nil
	})
	e.StartSecondAgent(srv.Name, cloud.TokenFromCloudInit(srv.UserData))
	if op := e.WaitOperation(*started.OperationId); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("provision: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	n, err := store.New(e.DB).GetNodeByName(ctx, srv.Name)
	if err != nil || n.Role != "edge" || n.Lifecycle != "active" {
		t.Fatalf("the edge node: %+v %v", n, err)
	}
	var instances int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM instances WHERE node_id = $1 AND deleted_at IS NULL`, n.ID).Scan(&instances); err != nil || instances != 0 {
		t.Fatalf("an edge node got %d instances (%v)", instances, err)
	}
}
