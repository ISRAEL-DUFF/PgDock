package costs

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/store"
)

// Amounts are minor units: kobo for naira, cents for euros. Margins are in
// naira at the current rate unless they say otherwise.

// CategoryCost is one category of cost in one region.
type CategoryCost struct {
	Category string `json:"category"`
	Region   string `json:"region"`
	// Native is the cost in each billing currency.
	Native map[string]int64 `json:"native_minor"`
	// NGN at the current rate, and at each day's rate when it was incurred.
	NGN       int64 `json:"ngn_minor"`
	NGNBooked int64 `json:"ngn_booked_minor"`
}

// OrgMargin is an organisation's month.
type OrgMargin struct {
	OrgID   uuid.UUID        `json:"org_id"`
	Name    string           `json:"name"`
	Plan    string           `json:"plan"`
	Revenue int64            `json:"revenue_minor"`
	Cost    int64            `json:"cost_minor"`
	Native  map[string]int64 `json:"cost_native_minor"`
	Margin  int64            `json:"margin_minor"`
	// MarginPct is the margin over revenue (nil without revenue).
	MarginPct *float64 `json:"margin_pct,omitempty"`
}

// PlanMargin is a plan's month across its organisations.
type PlanMargin struct {
	Plan      string   `json:"plan"`
	Orgs      int      `json:"orgs"`
	Revenue   int64    `json:"revenue_minor"`
	Cost      int64    `json:"cost_minor"`
	Margin    int64    `json:"margin_minor"`
	MarginPct *float64 `json:"margin_pct,omitempty"`
}

// UnitCost is the cost of a unit, the input to pricing (V3 §3.1).
type UnitCost struct {
	Unit     string  `json:"unit"`
	Quantity float64 `json:"quantity"`
	// Native per unit in the currency most of it was billed in, and NGN.
	Currency  string  `json:"currency"`
	PerUnit   float64 `json:"per_unit_minor"`
	PerUnitNG float64 `json:"per_unit_ngn_minor"`
}

// Margins is the cost side of the dashboard for a month.
type Margins struct {
	Month string `json:"month"`
	// Days attributed so far.
	Days  int   `json:"days"`
	Rates Rates `json:"rates"`
	// MissingRates are currencies costs were billed in with no rate: their
	// costs are left out of the naira totals.
	MissingRates []string       `json:"missing_rates,omitempty"`
	Categories   []CategoryCost `json:"categories"`
	Plans        []PlanMargin   `json:"plans"`
	Orgs         []OrgMargin    `json:"orgs"`
	Units        []UnitCost     `json:"units"`
	Revenue      int64          `json:"revenue_minor"`
	Cost         int64          `json:"cost_minor"`
	CostBooked   int64          `json:"cost_booked_minor"`
	// Unallocated is idle capacity, floating IPs and overheads: charged to
	// no organisation, so in the total margin only.
	Unallocated int64 `json:"unallocated_minor"`
	FreeTier    int64 `json:"free_tier_cost_minor"`
	Margin      int64 `json:"margin_minor"`
	// FXErosion is how much more the month's costs are at today's rate than
	// at the rates when they were incurred.
	FXErosion int64    `json:"fx_erosion_minor"`
	MarginPct *float64 `json:"margin_pct,omitempty"`
}

func pct(margin, revenue int64) *float64 {
	if revenue <= 0 {
		return nil
	}
	v := math.Round(float64(margin)/float64(revenue)*1000) / 10
	return &v
}

// earned is what an organisation's month earns: its usage and the
// month's own plan fee (an invoice bills the next month's fee in advance).
func earned(subtotal, nextFee, monthFee int64) int64 { return subtotal - nextFee + monthFee }

// Margins computes month's margins (the month containing month).
func (s *Service) Margins(ctx context.Context, month time.Time) (Margins, error) {
	from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	q := store.New(s.db)
	rows, err := q.CostAllocationsBetween(ctx, store.CostAllocationsBetweenParams{FromDay: pgDate(from), ToDay: pgDate(to)})
	if err != nil {
		return Margins{}, err
	}
	now, err := s.RatesAt(ctx, s.now())
	if err != nil {
		return Margins{}, err
	}
	out := Margins{Month: from.Format("2006-01"), Rates: now}
	// Each day's rates, for the booked view.
	dayRates := map[string]Rates{}
	rateOn := func(d time.Time) (Rates, error) {
		k := d.Format(time.DateOnly)
		if r, ok := dayRates[k]; ok {
			return r, nil
		}
		r, err := s.RatesAt(ctx, d.AddDate(0, 0, 1))
		if err == nil {
			dayRates[k] = r
		}
		return r, err
	}
	type catKey struct{ cat, region string }
	cats := map[catKey]*CategoryCost{}
	catNative := map[catKey]map[string]float64{}
	catNGN, catBooked := map[catKey]float64{}, map[catKey]float64{}
	orgNative := map[uuid.UUID]map[string]float64{}
	unitQty := map[string]float64{}
	unitNative := map[string]map[string]float64{}
	missing := map[string]bool{}
	days := map[string]bool{}
	var unalloc float64
	for _, r := range rows {
		days[r.Day.Time.Format(time.DateOnly)] = true
		amt := flt(r.AmountMinor)
		k := catKey{r.Category, r.Region}
		c := cats[k]
		if c == nil {
			c = &CategoryCost{Category: r.Category, Region: r.Region, Native: map[string]int64{}}
			cats[k] = c
		}
		if catNative[k] == nil {
			catNative[k] = map[string]float64{}
		}
		catNative[k][r.Currency] += amt
		ngn, ok := now.ToNGN(amt, r.Currency)
		if !ok {
			missing[r.Currency] = true
		}
		catNGN[k] += ngn
		// At the rate in effect that day; before the first rate recorded,
		// at the current one.
		booked := ngn
		if dr, err := rateOn(r.Day.Time); err == nil {
			if b, ok := dr.ToNGN(amt, r.Currency); ok {
				booked = b
			}
		}
		catBooked[k] += booked
		if r.OrgID == uuid.Nil {
			unalloc += ngn
		} else {
			if orgNative[r.OrgID] == nil {
				orgNative[r.OrgID] = map[string]float64{}
			}
			orgNative[r.OrgID][r.Currency] += amt
		}
		unit := map[string]string{CatShared: "shared GB-month", CatDedicated: "dedicated vCPU-month", CatHA: "dedicated vCPU-month", CatBackup: "backup GB-month"}[r.Category]
		if unit != "" {
			unitQty[unit] += flt(r.Quantity)
			if unitNative[unit] == nil {
				unitNative[unit] = map[string]float64{}
			}
			unitNative[unit][r.Currency] += amt
		}
	}
	out.Days = len(days)
	for c := range missing {
		out.MissingRates = append(out.MissingRates, c)
	}
	sort.Strings(out.MissingRates)
	for k, c := range cats {
		for cur, v := range catNative[k] {
			c.Native[cur] = int64(math.Round(v))
		}
		c.NGN, c.NGNBooked = int64(math.Round(catNGN[k])), int64(math.Round(catBooked[k]))
		out.Categories = append(out.Categories, *c)
		out.Cost += c.NGN
		out.CostBooked += c.NGNBooked
	}
	sort.Slice(out.Categories, func(i, j int) bool {
		if out.Categories[i].Region != out.Categories[j].Region {
			return out.Categories[i].Region < out.Categories[j].Region
		}
		return out.Categories[i].Category < out.Categories[j].Category
	})
	out.Unallocated = int64(math.Round(unalloc))
	out.FXErosion = out.Cost - out.CostBooked

	// Unit costs: GB-hours and vCPU-hours over the month's hours.
	hours := 24 * daysIn(from)
	for _, unit := range []string{"shared GB-month", "dedicated vCPU-month", "backup GB-month"} {
		qty := unitQty[unit] / hours
		if qty <= 0 {
			continue
		}
		cur, best := "", 0.0
		var ngn float64
		for c, v := range unitNative[unit] {
			if v > best {
				cur, best = c, v
			}
			if n, ok := now.ToNGN(v, c); ok {
				ngn += n
			}
		}
		out.Units = append(out.Units, UnitCost{Unit: unit, Quantity: math.Round(qty*100) / 100, Currency: cur,
			PerUnit: math.Round(best/qty*100) / 100, PerUnitNG: math.Round(ngn/qty*100) / 100})
	}

	// Revenue and margin per organisation.
	orgs, err := q.CostOrgs(ctx, pgDate(from))
	if err != nil {
		return out, err
	}
	plans := map[string]*PlanMargin{}
	for _, o := range orgs {
		nat := orgNative[o.ID]
		var cost float64
		native := map[string]int64{}
		for c, v := range nat {
			native[c] = int64(math.Round(v))
			if n, ok := now.ToNGN(v, c); ok {
				cost += n
			}
		}
		var revenue int64
		if s.rater != nil && (o.Plan != "free" || len(nat) > 0) {
			r, err := s.rater.Rate(ctx, o.ID, from)
			if err == nil {
				revenue = earned(r.Subtotal, r.NextFee, r.MonthFee)
			} else {
				s.log.Warn("margin: rate", "org", o.ID, "err", err)
			}
		}
		if revenue == 0 && len(nat) == 0 {
			continue
		}
		om := OrgMargin{OrgID: o.ID, Name: o.Name, Plan: o.Plan, Revenue: revenue, Cost: int64(math.Round(cost)), Native: native}
		om.Margin = om.Revenue - om.Cost
		om.MarginPct = pct(om.Margin, om.Revenue)
		out.Orgs = append(out.Orgs, om)
		out.Revenue += om.Revenue
		if o.Plan == "free" {
			out.FreeTier += om.Cost
		}
		p := plans[o.Plan]
		if p == nil {
			p = &PlanMargin{Plan: o.Plan}
			plans[o.Plan] = p
		}
		p.Orgs++
		p.Revenue += om.Revenue
		p.Cost += om.Cost
	}
	sort.Slice(out.Orgs, func(i, j int) bool { return out.Orgs[i].Margin < out.Orgs[j].Margin })
	for _, name := range []string{"free", "pro", "team"} {
		if p := plans[name]; p != nil {
			p.Margin = p.Revenue - p.Cost
			p.MarginPct = pct(p.Margin, p.Revenue)
			out.Plans = append(out.Plans, *p)
			delete(plans, name)
		}
	}
	for _, p := range plans {
		p.Margin = p.Revenue - p.Cost
		p.MarginPct = pct(p.Margin, p.Revenue)
		out.Plans = append(out.Plans, *p)
	}
	out.Margin = out.Revenue - out.Cost
	out.MarginPct = pct(out.Margin, out.Revenue)
	return out, nil
}

func kobo(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// WriteMarginsCSV writes the month for the accountant: costs by category
// and region, then each organisation's revenue, cost and margin, in naira.
func WriteMarginsCSV(w io.Writer, m Margins) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"month", m.Month, "days attributed", strconv.Itoa(m.Days)})
	for c, r := range m.Rates {
		if c != NGN {
			_ = cw.Write([]string{"rate", c, strconv.FormatFloat(r, 'f', 4, 64), "NGN per unit"})
		}
	}
	_ = cw.Write(nil)
	_ = cw.Write([]string{"category", "region", "native", "ngn_at_current_rate", "ngn_at_rate_incurred"})
	for _, c := range m.Categories {
		native := ""
		for cur, v := range c.Native {
			native += fmt.Sprintf("%s %s ", cur, kobo(v))
		}
		_ = cw.Write([]string{c.Category, c.Region, native, kobo(c.NGN), kobo(c.NGNBooked)})
	}
	_ = cw.Write(nil)
	_ = cw.Write([]string{"organisation", "org_id", "plan", "revenue_ngn", "cost_ngn", "margin_ngn", "margin_pct"})
	for _, o := range m.Orgs {
		p := ""
		if o.MarginPct != nil {
			p = strconv.FormatFloat(*o.MarginPct, 'f', 1, 64)
		}
		_ = cw.Write([]string{o.Name, o.OrgID.String(), o.Plan, kobo(o.Revenue), kobo(o.Cost), kobo(o.Margin), p})
	}
	_ = cw.Write(nil)
	_ = cw.Write([]string{"total revenue", kobo(m.Revenue)})
	_ = cw.Write([]string{"total cost", kobo(m.Cost)})
	_ = cw.Write([]string{"of which unallocated", kobo(m.Unallocated)})
	_ = cw.Write([]string{"of which free tier", kobo(m.FreeTier)})
	_ = cw.Write([]string{"gross margin", kobo(m.Margin)})
	_ = cw.Write([]string{"fx erosion", kobo(m.FXErosion)})
	cw.Flush()
	return cw.Error()
}
