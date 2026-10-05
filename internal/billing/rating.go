package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// Revenue accounts for what isn't a plan.
const (
	RevenueDedicated = AccRevenuePrefix + "dedicated"
	RevenueHA        = AccRevenuePrefix + "ha"
)

// metricLabels name the rated metrics on invoices.
var metricLabels = map[string]string{
	tenancy.MetricSharedStorage:   "Shared storage (GB-hours)",
	tenancy.MetricBackupStorage:   "Backup storage (GB-hours)",
	tenancy.MetricBranchHours:     "Branch hours",
	tenancy.MetricPoolerTraffic:   "Data transfer (GB)",
	tenancy.MetricWebhookSent:     "Webhook deliveries",
	tenancy.MetricJobRunsSQL:      "SQL job runs",
	tenancy.MetricJobRunsHTTP:     "HTTP job runs",
	tenancy.MetricDedicatedCPU:    "vCPU-hours",
	tenancy.MetricDedicatedRAM:    "RAM GB-hours",
	tenancy.MetricDedicatedDisk:   "Disk GB-hours",
	tenancy.MetricHACPU:           "vCPU-hours",
	tenancy.MetricHARAM:           "RAM GB-hours",
	tenancy.MetricHADisk:          "Disk GB-hours",
	tenancy.MetricSyncReplication: "hours",
}

// Rated is a month's invoice before it is saved: usage of the month in
// arrears, the next month's plan fee in advance, and the month's plan
// changes (V3 §3.5, §3.6).
type Rated struct {
	OrgID            uuid.UUID
	Period           time.Time // the first of the usage month (UTC)
	PriceBookVersion int32
	Lines            []Line
	Subtotal         int64
	VAT              int64
	Total            int64
	WHTExpected      int64
	VATRate          Dec
	// NextFee is the next month's plan fee among the lines (in advance);
	// MonthFee is this month's, billed on the previous invoice.
	NextFee  int64
	MonthFee int64
}

type planTerm struct{ plan, term string }

type segment struct {
	from, to time.Time
	planTerm
}

func renewal(c store.BillingPlanChange) bool {
	return c.FromPlan == c.ToPlan && c.FromTerm == c.ToTerm
}

// Rate rates orgID's month (the UTC calendar month containing month).
func (s *Service) Rate(ctx context.Context, orgID uuid.UUID, month time.Time) (Rated, error) {
	return s.rate(ctx, orgID, month, nil, Dec{})
}

// rate rates with prices in place of the org's price book when given (a
// draft book's preview), and usage multiplied by scale when it isn't zero
// (the forecast).
func (s *Service) rate(ctx context.Context, orgID uuid.UUID, month time.Time, prices *Prices, scale Dec) (Rated, error) {
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return Rated{}, err
	}
	b, err := s.GetBook(ctx, a.PriceBookVersion)
	if err != nil {
		return Rated{}, err
	}
	p := b.Prices
	if prices != nil {
		p = *prices
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return Rated{}, err
	}
	mStart := MonthStart(month)
	mEnd := mStart.AddDate(0, 1, 0)
	out := Rated{OrgID: orgID, Period: mStart, PriceBookVersion: b.Version, VATRate: set.VATRate}
	q := store.New(s.db)

	// The plan over the month, from its applied changes.
	changes, err := q.PlanChangesFrom(ctx, store.PlanChangesFromParams{OrgID: orgID, FromTs: mStart})
	if err != nil {
		return Rated{}, err
	}
	start := planTerm{a.Plan, a.Term}
	for _, c := range changes {
		if !renewal(c) {
			start = planTerm{c.FromPlan, c.FromTerm}
			break
		}
	}
	var segs []segment
	cur, from := start, mStart
	for _, c := range changes {
		if !c.EffectiveAt.Before(mEnd) {
			break
		}
		if renewal(c) {
			continue
		}
		if at := dayStart(c.EffectiveAt); at.After(from) {
			segs = append(segs, segment{from, at, cur})
			from = at
		}
		cur = planTerm{c.ToPlan, c.ToTerm}
	}
	segs = append(segs, segment{from, mEnd, cur})
	monthDays := days(mStart, mEnd)

	// Usage, per segment for plan metrics and per project for dedicated.
	rows, err := q.OrgUsageByDay(ctx, store.OrgUsageByDayParams{OrgID: orgID, FromTs: mStart, ToTs: mEnd})
	if err != nil {
		return Rated{}, err
	}
	planUse := make([]map[string]Dec, len(segs))
	for i := range planUse {
		planUse[i] = map[string]Dec{}
	}
	projUse := map[uuid.UUID]map[string]Dec{}
	for _, r := range rows {
		qty := DecFromNumeric(r.Quantity)
		if !scale.IsZero() {
			qty = qty.Mul(scale)
		}
		day := r.Day.Time.UTC()
		if _, ok := metricLabels[r.Metric]; !ok {
			continue
		}
		switch r.Metric {
		case tenancy.MetricDedicatedCPU, tenancy.MetricDedicatedRAM, tenancy.MetricDedicatedDisk,
			tenancy.MetricHACPU, tenancy.MetricHARAM, tenancy.MetricHADisk, tenancy.MetricSyncReplication:
			if projUse[r.ProjectID] == nil {
				projUse[r.ProjectID] = map[string]Dec{}
			}
			projUse[r.ProjectID][r.Metric] = projUse[r.ProjectID][r.Metric].Add(qty)
		default:
			for i, sg := range segs {
				if !day.Before(sg.from) && day.Before(sg.to) {
					planUse[i][r.Metric] = planUse[i][r.Metric].Add(qty)
					break
				}
			}
		}
	}

	// Overage above each segment's share of the allowance.
	for i, sg := range segs {
		pl, ok := p.Plans[sg.plan]
		if !ok {
			continue
		}
		segDays := days(sg.from, sg.to)
		when := ""
		if len(segs) > 1 {
			when = fmt.Sprintf(", %s", dateRange(sg.from, sg.to))
		}
		for _, m := range PlanMetrics {
			unit, priced := pl.Unit[m]
			if !priced || unit.Sign() <= 0 {
				continue
			}
			used := planUse[i][m]
			included := pl.Included[m].Frac(segDays, monthDays)
			over := used.Sub(included)
			if over.Sign() <= 0 {
				continue
			}
			amount := over.Mul(unit).Round()
			if amount == 0 {
				continue
			}
			out.Lines = append(out.Lines, Line{
				Kind: KindOverage, Metric: m, Quantity: over, UnitPrice: unit, Amount: amount, Revenue: AccRevenuePrefix + sg.plan,
				Description: fmt.Sprintf("%s above the %s allowance of %s%s", metricLabels[m], pl.Name, included.String(), when),
			})
		}
	}

	// Dedicated instances, HA standbys and synchronous replication.
	ids := make([]uuid.UUID, 0, len(projUse))
	for id := range projUse {
		ids = append(ids, id)
	}
	names := map[uuid.UUID]string{}
	if len(ids) > 0 {
		pn, err := q.ProjectNamesByID(ctx, ids)
		if err != nil {
			return Rated{}, err
		}
		for _, n := range pn {
			names[n.ID] = n.Name
		}
	}
	sort.Slice(ids, func(i, j int) bool { return names[ids[i]]+ids[i].String() < names[ids[j]]+ids[j].String() })
	for _, id := range ids {
		use, pid := projUse[id], id
		name := names[id]
		if name == "" {
			name = id.String()
		}
		add := func(kind, metric, what string, unit Dec, revenue string) int64 {
			qty := use[metric]
			amount := qty.Mul(unit).Round()
			if qty.Sign() <= 0 || amount == 0 {
				return 0
			}
			out.Lines = append(out.Lines, Line{
				Kind: kind, ProjectID: &pid, Metric: metric, Quantity: qty, UnitPrice: unit, Amount: amount, Revenue: revenue,
				Description: fmt.Sprintf("%s %s: %s", what, name, metricLabels[metric]),
			})
			return amount
		}
		add(KindDedicated, tenancy.MetricDedicatedCPU, "Dedicated", p.Dedicated.VCPUHour, RevenueDedicated)
		add(KindDedicated, tenancy.MetricDedicatedRAM, "Dedicated", p.Dedicated.RAMGBHour, RevenueDedicated)
		add(KindDedicated, tenancy.MetricDedicatedDisk, "Dedicated", p.Dedicated.DiskGBHour, RevenueDedicated)
		standby := add(KindAddon, tenancy.MetricHACPU, "HA standby", p.Dedicated.VCPUHour, RevenueHA) +
			add(KindAddon, tenancy.MetricHARAM, "HA standby", p.Dedicated.RAMGBHour, RevenueHA) +
			add(KindAddon, tenancy.MetricHADisk, "HA standby", p.Dedicated.DiskGBHour, RevenueHA)
		if pct := p.AddOns.HAPremiumPercent; standby > 0 && pct.Sign() > 0 {
			if amount := DecInt(standby).Mul(pct).Frac(1, 100).Round(); amount > 0 {
				out.Lines = append(out.Lines, Line{
					Kind: KindAddon, ProjectID: &pid, Quantity: pct, UnitPrice: DecInt(standby).Frac(1, 100), Amount: amount, Revenue: RevenueHA,
					Description: fmt.Sprintf("HA premium %s: %s%% of the standby", name, pct),
				})
			}
		}
		add(KindAddon, tenancy.MetricSyncReplication, "Synchronous replication", p.AddOns.SyncReplicationHour, RevenueHA)
	}

	// The month's plan changes: proration, annual terms and renewals.
	for _, c := range changes {
		if !c.EffectiveAt.Before(mEnd) {
			break
		}
		var lines []Line
		if err := json.Unmarshal(c.Lines, &lines); err != nil {
			return Rated{}, fmt.Errorf("plan change %s: %w", c.ID, err)
		}
		out.Lines = append(out.Lines, lines...)
	}

	// Next month's plan fee, in advance (annual terms are paid with their
	// change or renewal).
	next := start
	for _, c := range changes {
		if c.EffectiveAt.After(mEnd) {
			break
		}
		next = planTerm{c.ToPlan, c.ToTerm}
	}
	if sc, err := q.ScheduledChangeBy(ctx, store.ScheduledChangeByParams{OrgID: orgID, At: mEnd}); err == nil {
		next = planTerm{sc.ToPlan, sc.ToTerm}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Rated{}, err
	}
	if pl, ok := p.Plans[start.plan]; ok && start.term == TermMonthly {
		out.MonthFee = pl.MonthlyMinor
	}
	if pl, ok := p.Plans[next.plan]; ok && next.term == TermMonthly && pl.MonthlyMinor > 0 {
		out.NextFee = pl.MonthlyMinor
		out.Lines = append(out.Lines, Line{
			Kind: KindPlan, Quantity: DecInt(1), UnitPrice: DecInt(pl.MonthlyMinor), Amount: pl.MonthlyMinor, Revenue: AccRevenuePrefix + next.plan,
			Description: fmt.Sprintf("%s plan, %s", pl.Name, mEnd.Format("January 2006")),
		})
	}

	order := map[string]int{KindPlan: 0, KindProration: 1, KindOverage: 2, KindDedicated: 3, KindAddon: 4, KindCredit: 5}
	sort.SliceStable(out.Lines, func(i, j int) bool { return order[out.Lines[i].Kind] < order[out.Lines[j].Kind] })
	for _, l := range out.Lines {
		out.Subtotal += l.Amount
	}
	out.VAT = DecInt(out.Subtotal).Mul(set.VATRate).Round()
	out.Total = out.Subtotal + out.VAT
	if a.DeductsWht && out.Subtotal > 0 {
		out.WHTExpected = DecInt(out.Subtotal).Mul(set.WHTRate).Round()
	}
	return out, nil
}

// Preview estimates each organisation's invoice for month under prices
// (V3 §3.9: the revenue effect of a draft book before publishing it).
type Preview struct {
	OrgID     uuid.UUID
	OrgName   string
	Plan      string
	Current   int64 // subtotal at the org's price book
	Projected int64 // subtotal at the draft
}

// PreviewPrices rates every org's month under its book and under prices.
func (s *Service) PreviewPrices(ctx context.Context, prices Prices, month time.Time) ([]Preview, error) {
	if err := prices.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	accts, err := store.New(s.db).ListBillingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []Preview
	for _, a := range accts {
		if a.OrgStatus == "deleted" {
			continue
		}
		if _, ok := prices.Plans[a.Plan]; !ok {
			continue
		}
		cur, err := s.Rate(ctx, a.OrgID, month)
		if err != nil {
			return nil, err
		}
		proj, err := s.rate(ctx, a.OrgID, month, &prices, Dec{})
		if err != nil {
			return nil, err
		}
		if cur.Subtotal == 0 && proj.Subtotal == 0 {
			continue
		}
		out = append(out, Preview{OrgID: a.OrgID, OrgName: a.OrgName, Plan: a.Plan, Current: cur.Subtotal, Projected: proj.Subtotal})
	}
	return out, nil
}
