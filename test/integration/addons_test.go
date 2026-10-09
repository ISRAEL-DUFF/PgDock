package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/tenancy"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestBillingAddOns is V4.1-M3's done-when (V4.1 §4.5): a dedicated
// project on 14-day point-in-time recovery keeps 14 base backups after 15
// daily ones and restores to a point older than the default window; a
// shared project on extended retention keeps 30 dailies; a Lagos dedicated
// project's invoice carries a 25% premium on exactly its dedicated and HA
// lines and the ledger balances; Personal is refused the add-ons.
func TestBillingAddOns(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "both")
	ctx := context.Background()
	plan := func(name string) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, `UPDATE organizations SET plan_id = (SELECT id FROM quota_plans WHERE name = $2) WHERE id = $1`, e.OrgID, name); err != nil {
			t.Fatal(err)
		}
	}

	tier := gen.ProjectTierDedicated
	profile, vol := "small", 5
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Ledger", Tier: &tier, Profile: &profile, VolumeGb: &vol}, &c); code != http.StatusAccepted {
		t.Fatalf("create dedicated: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create dedicated: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	pid := c.Project.Id
	var original string
	if err := e.DB.QueryRow(ctx, `SELECT q.name FROM organizations o JOIN quota_plans q ON q.id = o.plan_id WHERE o.id = $1`, e.OrgID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	instance := "/api/v1/projects/" + pid.String() + "/instance"

	// ---- The plan gate ---------------------------------------------------------
	plan("Personal")
	days := gen.InstanceUpdatePitrDaysN14
	var apiErr gen.Error
	if code := e.Do("PATCH", instance, gen.InstanceUpdate{PitrDays: &days}, &apiErr); code != http.StatusForbidden || apiErr.Code != "plan_required" {
		t.Fatalf("14-day PITR on Personal: %d %+v", code, apiErr)
	}
	shared := e.CreateProject("Shop")
	long := gen.Extended
	if code := e.Do("PATCH", "/api/v1/projects/"+shared.Project.Id.String()+"/settings",
		gen.UpdateProjectRequest{Settings: &gen.ProjectSettingsPatch{BackupRetention: &long}}, &apiErr); code != http.StatusForbidden || apiErr.Code != "plan_required" {
		t.Fatalf("extended retention on Personal: %d %+v", code, apiErr)
	}
	plan("Pro")
	var upd gen.InstanceUpdated
	if code := e.Do("PATCH", instance, gen.InstanceUpdate{PitrDays: &days}, &upd); code != http.StatusOK || upd.Instance.PitrDays == nil || *upd.Instance.PitrDays != 14 {
		t.Fatalf("14-day PITR on Pro: %d %+v", code, upd)
	}

	// ---- 14-day PITR: 14 base backups kept, a point past 7 restorable ---------
	// Each base backup stands for a day; WAL-G keeps the newest 14 (and the
	// WAL from the oldest of them on).
	app := e.MustConnect(c.Connection.PooledUrl)
	if _, err := app.Exec(ctx, `CREATE TABLE days (n int PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	var target time.Time
	backup := func(n int) {
		t.Helper()
		if _, err := app.Exec(ctx, `INSERT INTO days VALUES ($1)`, n); err != nil {
			t.Fatal(err)
		}
		var op gen.Operation
		if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/backups", nil, &op); code != http.StatusAccepted {
			t.Fatalf("base backup %d: %d", n, code)
		}
		if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
			t.Fatalf("base backup %d: %s\n%s", n, op.Status, testenv.FormatLog(op))
		}
	}
	for n := 1; n <= 15; n++ {
		backup(n)
		if n == 3 {
			// Day 3's moment: 13 backups later it's outside a 7-backup window.
			time.Sleep(1100 * time.Millisecond)
			if err := app.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&target); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1100 * time.Millisecond)
		}
	}
	var kept int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FROM backups WHERE project_id = $1 AND kind = 'base' AND status = 'succeeded'`, pid).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 14 {
		t.Fatalf("base backups kept with a 14-day window: %d, want 14", kept)
	}
	// The restore is a second dedicated instance: past Pro's allowance, so
	// back to the test org's own plan for it.
	plan(original)
	var rc gen.ProjectCredentials
	var body json.RawMessage
	if code := e.Do("POST", "/api/v1/projects/"+pid.String()+"/pitr", gen.PitrRequest{Name: "Ledger day 3", TargetTime: &target}, &body); code != http.StatusAccepted {
		t.Fatalf("pitr: %d %s", code, body)
	} else if err := json.Unmarshal(body, &rc); err != nil {
		t.Fatal(err)
	}
	if op := e.WaitOperation(rc.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("pitr to day 3: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	restored := e.MustConnect(rc.Connection.PooledUrl)
	var last int
	if err := restored.QueryRow(ctx, `SELECT max(n) FROM days`).Scan(&last); err != nil || last != 3 {
		t.Fatalf("restored to day 3: max %d %v", last, err)
	}

	// ---- Extended retention: 30 dailies --------------------------------------
	if code := e.Do("PATCH", "/api/v1/projects/"+shared.Project.Id.String()+"/settings",
		gen.UpdateProjectRequest{Settings: &gen.ProjectSettingsPatch{BackupRetention: &long}}, nil); code != http.StatusOK {
		t.Fatalf("extended retention on Pro: %d", code)
	}
	nightly := func() uuid.UUID {
		t.Helper()
		var op gen.Operation
		if code := e.Do("POST", "/api/v1/projects/"+shared.Project.Id.String()+"/backups", nil, &op); code != http.StatusAccepted {
			t.Fatalf("logical backup: %d", code)
		}
		if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
			t.Fatalf("logical backup: %s\n%s", op.Status, testenv.FormatLog(op))
		}
		var id uuid.UUID
		if err := e.DB.QueryRow(ctx, `SELECT id FROM backups WHERE operation_id = $1`, op.Id).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := nightly()
	// Forty earlier nights, one a day, copies of the first.
	if _, err := e.DB.Exec(ctx, `INSERT INTO backups
		SELECT (jsonb_populate_record(b, jsonb_build_object('id', gen_random_uuid(), 'object_key', b.object_key || '.day' || g,
		  'operation_id', NULL, 'started_at', b.started_at - g * interval '1 day', 'finished_at', b.finished_at - g * interval '1 day'))).*
		FROM backups b, generate_series(1, 40) g WHERE b.id = $1`, first); err != nil {
		t.Fatal(err)
	}
	nightly() // applies retention
	var dailies, total int
	if err := e.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE finished_at > now() - interval '29 days 12 hours'), count(*)
		FROM backups WHERE project_id = $1 AND kind = 'logical' AND status = 'succeeded'`, shared.Project.Id).Scan(&dailies, &total); err != nil {
		t.Fatal(err)
	}
	// One a day for the newest 30 days (today's newer backup for today),
	// then one a week from the older ten nights (two to three ISO weeks).
	if dailies != 30 || total < 32 || total > 33 {
		t.Fatalf("extended retention kept %d recent and %d in all", dailies, total)
	}

	// ---- The Lagos premium -----------------------------------------------------
	for _, stmt := range []string{
		`INSERT INTO regions (id, name, country) VALUES ('ng-lagos', 'Lagos', 'NG') ON CONFLICT (id) DO NOTHING`,
		fmt.Sprintf(`UPDATE projects SET region = 'ng-lagos' WHERE id = '%s'`, pid),
	} {
		if _, err := e.DB.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Billing.ChangePlan(ctx, e.OrgID, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	// A published book with a 25% Lagos premium, for this org.
	if _, err := e.DB.Exec(ctx, `WITH b AS (
		  INSERT INTO price_books (version, effective_at, prices, notes, published_at)
		  SELECT (SELECT max(version) + 1 FROM price_books), effective_at, jsonb_set(prices, '{addons,region_premium_percent}', '{"ng-lagos": "25"}'), 'Lagos premium', now()
		  FROM price_books WHERE version = (SELECT price_book_version FROM billing_accounts WHERE org_id = $1)
		  RETURNING version)
		UPDATE billing_accounts SET price_book_version = (SELECT version FROM b) WHERE org_id = $1`, e.OrgID); err != nil {
		t.Fatal(err)
	}
	month := billing.MonthStart(time.Now())
	hour := month.Add(2 * time.Hour)
	for metric, qty := range map[string]float64{
		tenancy.MetricDedicatedCPU: 1, tenancy.MetricDedicatedRAM: 1, tenancy.MetricDedicatedDisk: 5,
		tenancy.MetricHACPU: 1, tenancy.MetricHARAM: 1, tenancy.MetricHADisk: 5,
		tenancy.MetricPITR14Hours: 1,
	} {
		if _, err := e.DB.Exec(ctx, `INSERT INTO usage_records (org_id, project_id, metric, granularity, period_start, quantity, plan_id)
			VALUES ($1, $2, $3, 'hour', $4, $5, (SELECT plan_id FROM organizations WHERE id = $1))
			ON CONFLICT (org_id, metric, granularity, period_start, project_id) DO UPDATE SET quantity = EXCLUDED.quantity`,
			e.OrgID, pid, metric, hour, qty); err != nil {
			t.Fatal(err)
		}
	}
	r, err := e.Billing.Rate(ctx, e.OrgID, month)
	if err != nil {
		t.Fatal(err)
	}
	var hardware, premium int64
	var pitr bool
	for _, l := range r.Lines {
		if l.ProjectID == nil || *l.ProjectID != pid {
			continue
		}
		switch {
		case strings.HasPrefix(l.Description, "Dedicated "), strings.HasPrefix(l.Description, "HA "):
			hardware += l.Amount
		case strings.HasPrefix(l.Description, "Lagos region premium"):
			premium += l.Amount
		case strings.HasPrefix(l.Description, "14-day point-in-time recovery"):
			pitr = true
		}
	}
	if want := billing.DecInt(hardware).Mul(billing.D("25")).Frac(1, 100).Round(); hardware == 0 || premium != want || !pitr {
		for _, l := range r.Lines {
			t.Logf("  %-70s %d", l.Description, l.Amount)
		}
		t.Fatalf("premium %d on %d of dedicated and HA lines, want %d (PITR line: %v)", premium, hardware, want, pitr)
	}
	// Invoiced, and the ledger balances.
	period := month.Format("2006-01")
	var drafted struct{ Drafts int }
	if code := e.Do("POST", "/api/v1/admin/invoices/draft", map[string]any{"period": period, "org_id": e.OrgID}, &drafted); code != http.StatusOK || drafted.Drafts != 1 {
		t.Fatalf("draft: %d %+v", code, drafted)
	}
	var invs gen.InvoiceList
	e.Do("GET", "/api/v1/admin/invoices?status=draft&period="+period, nil, &invs)
	if len(invs.Items) != 1 {
		t.Fatalf("drafts: %+v", invs.Items)
	}
	if code := e.Do("POST", "/api/v1/admin/invoices/"+invs.Items[0].Id.String()+"/issue", nil, nil); code != http.StatusOK {
		t.Fatalf("issue: %d", code)
	}
	if problems, err := billing.Check(ctx, e.DB); err != nil || len(problems) > 0 {
		t.Fatalf("ledger check: %v %+v", err, problems)
	}

	// The estimate shows the premium too.
	var est struct {
		HourlyMinor int64 `json:"hourly_minor"`
		Lines       []gen.InvoiceLine
	}
	region := "ng-lagos"
	cpus := float32(1)
	if code := e.Do("POST", "/api/v1/orgs/"+e.OrgID.String()+"/billing/estimate", map[string]any{"cpus": cpus, "memory_mb": 1024, "disk_gb": 5, "region": region}, &est); code != http.StatusOK {
		t.Fatalf("estimate: %d", code)
	}
	found := false
	for _, l := range est.Lines {
		found = found || l.Description == "Region premium"
	}
	if !found {
		t.Errorf("estimate without the region premium: %+v", est.Lines)
	}

	// A book saved through the API keeps every add-on price (the margin on
	// messages and region premiums were once dropped on the way in).
	var books struct{ Items []gen.PriceBook }
	if code := e.Do("GET", "/api/v1/admin/price-books", nil, &books); code != http.StatusOK || len(books.Items) == 0 {
		t.Fatalf("price books: %d", code)
	}
	src := books.Items[0].Prices
	var draft gen.PriceBook
	if code := e.Do("POST", "/api/v1/admin/price-books", gen.PriceBookInput{EffectiveAt: time.Now().Add(40 * 24 * time.Hour), Prices: src}, &draft); code != http.StatusCreated {
		t.Fatalf("draft: %d", code)
	}
	a := draft.Prices.Addons
	if a.MessageMarginPercent == nil || a.Pitr14Hour == nil || a.RegionPremiumPercent == nil || (*a.RegionPremiumPercent)["ng-lagos"] != "25" {
		t.Fatalf("add-ons after a round trip: %+v", a)
	}
}
