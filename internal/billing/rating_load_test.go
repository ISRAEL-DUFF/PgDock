package billing_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// TestRatingLoad is M27's load test of rating (V3 §14): 1,000
// organisations on a mix of plans, a month of hourly usage for 2,000+
// projects, then the month end: every invoice drafted and issued, and the
// ledger audit run over the result. PGDOCK_LOAD_RATING_ORGS changes its
// size; it writes tmp/load-report-rating.md.
func TestRatingLoad(t *testing.T) {
	if os.Getenv("PGDOCK_TEST_LOAD") == "" {
		t.Skip("set PGDOCK_TEST_LOAD=1 (make test-load)")
	}
	orgs := 1000
	if v, err := strconv.Atoi(os.Getenv("PGDOCK_LOAD_RATING_ORGS")); err == nil && v > 0 {
		orgs = v
	}
	s, db, clk, _ := newService(t)
	ctx := context.Background()
	oct, nov := day(1), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	// The mix: 60% Free, 30% Pro, 8% Team, 2% Team with a dedicated
	// project under HA. One to three shared projects each.
	type org struct {
		id       uuid.UUID
		plan     string
		projects []uuid.UUID
		ded      uuid.UUID
	}
	all := make([]org, orgs)
	setup := time.Now()
	for i := range all {
		o := org{id: newOrg(t, db, fmt.Sprintf("load-%04d", i)), plan: billing.PlanFree}
		switch {
		case i%50 == 0:
			o.plan, o.ded = billing.PlanTeam, uuid.New()
		case i%100 < 10:
			o.plan = billing.PlanTeam
		case i%100 < 40:
			o.plan = billing.PlanPro
		}
		for range 1 + i%3 {
			o.projects = append(o.projects, uuid.New())
		}
		if _, err := s.AddContact(ctx, o.id, fmt.Sprintf("ap@load-%04d.example", i), nil); err != nil {
			t.Fatal(err)
		}
		if o.plan != billing.PlanFree {
			clk.t = oct
			if _, err := s.ChangePlan(ctx, o.id, billing.PlanRequest{Plan: o.plan}); err != nil {
				t.Fatalf("org %d: %v", i, err)
			}
		}
		all[i] = o
	}
	// A month of usage as the recorder writes it: hourly storage and
	// transfer per project, daily backup storage, and the dedicated
	// project's compute, HA and synchronous replication hours.
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		slots    = make(chan struct{}, 8)
	)
	gctx := ctx
	for _, o := range all {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer func() { <-slots; wg.Done() }()
			if err := func() error {
				hourly := []struct {
					metric string
					qty    string
				}{{tenancy.MetricSharedStorage, "0.75"}, {tenancy.MetricPoolerTraffic, "0.02"}}
				for _, p := range o.projects {
					for _, m := range hourly {
						if _, err := db.Exec(gctx, `
						INSERT INTO usage_records (org_id, project_id, metric, granularity, period_start, quantity, plan_id)
						SELECT $1, $2, $3, 'hour', h, $4::numeric, (SELECT plan_id FROM organizations WHERE id = $1)
						FROM generate_series($5::timestamptz, $6::timestamptz - interval '1 hour', interval '1 hour') AS h`,
							o.id, p, m.metric, m.qty, oct, nov); err != nil {
							return err
						}
					}
					if _, err := db.Exec(gctx, `
					INSERT INTO usage_records (org_id, project_id, metric, granularity, period_start, quantity, plan_id)
					SELECT $1, $2, $3, 'day', d, 18, (SELECT plan_id FROM organizations WHERE id = $1)
					FROM generate_series($4::timestamptz, $5::timestamptz - interval '1 day', interval '1 day') AS d`,
						o.id, p, tenancy.MetricBackupStorage, oct, nov); err != nil {
						return err
					}
				}
				if o.ded != uuid.Nil {
					for _, m := range []struct {
						metric string
						qty    string
					}{{tenancy.MetricDedicatedCPU, "2"}, {tenancy.MetricDedicatedRAM, "4"}, {tenancy.MetricDedicatedDisk, "50"},
						{tenancy.MetricHACPU, "2"}, {tenancy.MetricHARAM, "4"}, {tenancy.MetricHADisk, "50"}, {tenancy.MetricSyncReplication, "1"}} {
						if _, err := db.Exec(gctx, `
						INSERT INTO usage_records (org_id, project_id, metric, granularity, period_start, quantity, plan_id)
						SELECT $1, $2, $3, 'hour', h, $4::numeric, (SELECT plan_id FROM organizations WHERE id = $1)
						FROM generate_series($5::timestamptz, $6::timestamptz - interval '1 hour', interval '1 hour') AS h`,
							o.id, o.ded, m.metric, m.qty, oct, nov); err != nil {
							return err
						}
					}
				}
				return nil
			}(); err != nil {
				mu.Lock()
				firstErr = err
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	var usageRows int64
	if err := db.QueryRow(ctx, `SELECT count(*) FROM usage_records`).Scan(&usageRows); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	setupTook := time.Since(setup)

	set, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.AutoIssue = true
	if err := s.SetSettings(ctx, set); err != nil {
		t.Fatal(err)
	}

	// Rating each org on its own (what the forecast and previews do).
	clk.t = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	rateTimes := make([]time.Duration, 0, orgs)
	rateStart := time.Now()
	for _, o := range all {
		st := time.Now()
		if _, err := s.Rate(ctx, o.id, oct); err != nil {
			t.Fatalf("rate %s: %v", o.id, err)
		}
		rateTimes = append(rateTimes, time.Since(st))
	}
	rateTook := time.Since(rateStart)

	// The month end.
	st := time.Now()
	drafted, err := s.DraftAll(ctx, oct)
	if err != nil {
		t.Fatalf("draft all: %v", err)
	}
	draftTook := time.Since(st)
	st = time.Now()
	issued, err := s.IssueDue(ctx, oct)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	issueTook := time.Since(st)
	st = time.Now()
	probs, err := billing.Check(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	checkTook := time.Since(st)
	var invoices, ledgerLines int64
	_ = db.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&invoices)
	_ = db.QueryRow(ctx, `SELECT count(*) FROM ledger_entries`).Scan(&ledgerLines)

	pct := func(ds []time.Duration, p float64) time.Duration {
		s := slices.Clone(ds)
		slices.Sort(s)
		return s[int(float64(len(s)-1)*p)]
	}
	ms := func(d time.Duration) string { return fmt.Sprintf("%d ms", d.Milliseconds()) }
	var r strings.Builder
	fmt.Fprintf(&r, "# Rating load test\n\n")
	fmt.Fprintf(&r, "| Measure | Result |\n| --- | --- |\n")
	fmt.Fprintf(&r, "| Organisations | %d (60%% Free, 30%% Pro, 8%% Team, 2%% Team with a dedicated HA project) |\n", orgs)
	fmt.Fprintf(&r, "| Usage rows for the month | %d (setup %s) |\n", usageRows, setupTook.Round(time.Second))
	fmt.Fprintf(&r, "| Rate each org | %s in all; p50 %s, p95 %s, max %s |\n", rateTook.Round(time.Millisecond), ms(pct(rateTimes, 0.5)), ms(pct(rateTimes, 0.95)), ms(pct(rateTimes, 1)))
	fmt.Fprintf(&r, "| Draft every invoice | %d drafts in %s |\n", drafted, draftTook.Round(time.Millisecond))
	fmt.Fprintf(&r, "| Issue them | %d in %s |\n", issued, issueTook.Round(time.Millisecond))
	fmt.Fprintf(&r, "| Ledger audit | %s over %d invoices and %d ledger entries; %d problems |\n", checkTook.Round(time.Millisecond), invoices, ledgerLines, len(probs))
	t.Log("\n" + r.String())
	_ = os.MkdirAll(filepath.Join("..", "..", "tmp"), 0o755)
	_ = os.WriteFile(filepath.Join("..", "..", "tmp", "load-report-rating.md"), []byte(r.String()), 0o644)

	paying := 0
	for _, o := range all {
		if o.plan != billing.PlanFree {
			paying++
		}
	}
	if drafted < paying || issued != drafted {
		t.Errorf("%d drafted and %d issued, want at least %d (every paying org)", drafted, issued, paying)
	}
	if len(probs) != 0 {
		t.Errorf("ledger problems: %+v", probs[:min(len(probs), 5)])
	}
	if p95 := pct(rateTimes, 0.95); p95 > 500*time.Millisecond {
		t.Errorf("rating p95 %s, want under 500 ms", p95)
	}
	if draftTook+issueTook > 10*time.Minute {
		t.Errorf("month end took %s, want under 10 minutes", draftTook+issueTook)
	}
	if checkTook > time.Minute {
		t.Errorf("ledger audit took %s, want under a minute", checkTook)
	}
}
