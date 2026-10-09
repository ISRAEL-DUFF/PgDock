package billing

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestDec(t *testing.T) {
	for in, want := range map[string]string{"34.25": "34.25", "0.0750": "0.075", "-3": "-3", "1000000": "1000000"} {
		if got := D(in).String(); got != want {
			t.Errorf("D(%q) = %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"", "1/3", "1e3", "abc"} {
		if _, err := ParseDec(bad); err == nil {
			t.Errorf("ParseDec(%q) accepted it", bad)
		}
	}
	for in, want := range map[string]int64{"0.5": 1, "1.49": 1, "-0.5": -1, "-1.4": -1, "919354.838": 919355, "2.5": 3} {
		if got := D(in).Round(); got != want {
			t.Errorf("Round(%s) = %d, want %d", in, got, want)
		}
	}
	var n pgtype.Numeric
	_ = n.Scan("7300.123456789")
	if got := DecFromNumeric(n).String(); got != "7300.123457" {
		t.Errorf("numeric = %s", got)
	}
	var d struct{ A, B Dec }
	if err := json.Unmarshal([]byte(`{"A": "0.1", "B": 0.2}`), &d); err != nil || d.A.Add(d.B).String() != "0.3" {
		t.Errorf("JSON decimals: %v %s", err, d.A.Add(d.B))
	}
	raw, _ := json.Marshal(d)
	if string(raw) != `{"A":"0.1","B":"0.2"}` {
		t.Errorf("marshal = %s", raw)
	}
}

func TestDefaultPricesValidate(t *testing.T) {
	p := DefaultPrices()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	// Round-trips through JSON unchanged.
	raw, _ := json.Marshal(p)
	var back Prices
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	raw2, _ := json.Marshal(back)
	if string(raw) != string(raw2) {
		t.Error("prices don't round-trip")
	}
	bad := []func(*Prices){
		func(p *Prices) { p.Currency = "USD" },
		func(p *Prices) { delete(p.Plans, PlanTeam) },
		func(p *Prices) { pl := p.Plans[PlanFree]; pl.MonthlyMinor = 100; p.Plans[PlanFree] = pl },
		func(p *Prices) { p.Plans[PlanPro].Unit["made_up_metric"] = D("1") },
		func(p *Prices) { p.Plans[PlanPro].Unit["job_runs_sql"] = D("-1") },
		func(p *Prices) { pl := p.Plans[PlanPro]; pl.QuotaPlan = ""; p.Plans[PlanPro] = pl },
		func(p *Prices) { p.Dedicated.VCPUHour = D("-1") },
		func(p *Prices) { p.Plans["Big Plan"] = p.Plans[PlanPro] },
		func(p *Prices) { p.AddOns.PITR14Hour = D("-1") },
		func(p *Prices) { p.AddOns.RegionPremiumPercent = map[string]Dec{"ng-lagos": D("501")} },
		func(p *Prices) { p.AddOns.RegionPremiumPercent = map[string]Dec{"NG Lagos": D("25")} },
	}
	for i, f := range bad {
		p := DefaultPrices()
		f(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("case %d: validated", i)
		}
	}
}

func sum(lines []Line) int64 {
	var n int64
	for _, l := range lines {
		n += l.Amount
	}
	return n
}

func TestProrateMidMonthUpgrade(t *testing.T) {
	p := DefaultPrices()
	at := time.Date(2026, 10, 13, 15, 4, 0, 0, time.UTC) // 19 of October's 31 days left, the 13th included
	lines, ends := Prorate(p, PlanPro, TermMonthly, nil, PlanTeam, TermMonthly, at)
	if ends != nil || len(lines) != 2 {
		t.Fatalf("lines %+v ends %v", lines, ends)
	}
	// Pro ₦15,000 × 19/31 = 919,354.84 kobo credited; Team ₦60,000 × 19/31 = 3,677,419.35 charged.
	if lines[0].Amount != -919355 || lines[1].Amount != 3677419 {
		t.Errorf("amounts %d, %d", lines[0].Amount, lines[1].Amount)
	}
	if lines[0].Revenue != "revenue:pro" || lines[1].Revenue != "revenue:team" {
		t.Errorf("revenue accounts %s, %s", lines[0].Revenue, lines[1].Revenue)
	}
	if lines[0].Description != "Unused Pro (monthly), 19 of 31 days" {
		t.Errorf("description %q", lines[0].Description)
	}

	// From Free on the 1st: the whole month of Pro.
	lines, _ = Prorate(p, PlanFree, TermMonthly, nil, PlanPro, TermMonthly, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if len(lines) != 1 || lines[0].Amount != 1_500_000 {
		t.Errorf("free→pro on the 1st: %+v", lines)
	}
	// On the last day: one day of 28.
	lines, _ = Prorate(p, PlanFree, TermMonthly, nil, PlanPro, TermMonthly, time.Date(2026, 2, 28, 23, 0, 0, 0, time.UTC))
	if sum(lines) != DecInt(1_500_000).Frac(1, 28).Round() {
		t.Errorf("one day: %+v", lines)
	}
}

func TestProrateAnnual(t *testing.T) {
	p := DefaultPrices()
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	// Monthly Pro to annual Pro: the month's unused part back, a year paid.
	lines, ends := Prorate(p, PlanPro, TermMonthly, nil, PlanPro, TermAnnual, at)
	if ends == nil || !ends.Equal(time.Date(2027, 10, 13, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("term ends %v", ends)
	}
	if len(lines) != 2 || lines[1].Kind != KindPlan || lines[1].Amount != 15_000_000 || lines[0].Amount != -919355 {
		t.Errorf("lines %+v", lines)
	}
	// Annual Pro to annual Team half way through: the difference for the
	// rest of the term.
	termEnds := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) // 182 of 365 days left
	lines, ends = Prorate(p, PlanPro, TermAnnual, &termEnds, PlanTeam, TermAnnual, mid)
	if ends == nil || !ends.Equal(termEnds) {
		t.Fatalf("term end moved: %v", ends)
	}
	want := DecInt(60_000_000).Frac(182, 365).Round() - DecInt(15_000_000).Frac(182, 365).Round()
	if sum(lines) != want {
		t.Errorf("annual upgrade = %d, want %d (%+v)", sum(lines), want, lines)
	}
}
