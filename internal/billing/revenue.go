package billing

import (
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/store"
)

// The revenue dashboard (V3 §7.2), first version: recurring revenue and
// its movements, paying organisations, conversion from Free, metered
// revenue, collections and receivables. Costs and margin come with cost
// attribution (§5.4, M24).
//
// MRR is plan subscriptions: each paying org's plan fee at its price book,
// an annual fee spread over 12 months. Metered charges (overage,
// dedicated, add-ons) are reported apart, from invoices. A month's figure
// is its snapshot as of its last day (or today, for this month).

// SnapshotMRR records every organisation's recurring revenue for this
// month (run daily).
func (s *Service) SnapshotMRR(ctx context.Context) error {
	q := store.New(s.db)
	accts, err := q.ListBillingAccounts(ctx)
	if err != nil {
		return err
	}
	month := MonthStart(s.Now())
	books := map[int32]Book{}
	for _, a := range accts {
		if a.OrgStatus == "deleted" {
			continue
		}
		b, ok := books[a.PriceBookVersion]
		if !ok {
			if b, err = s.GetBook(ctx, a.PriceBookVersion); err != nil {
				return err
			}
			books[a.PriceBookVersion] = b
		}
		var mrr int64
		if pl, ok := b.Prices.Plans[a.Plan]; ok && a.Plan != PlanFree && a.OrgStatus != "suspended" {
			mrr = pl.Monthly(a.Term).Round()
		}
		if err := q.UpsertMRRSnapshot(ctx, store.UpsertMRRSnapshotParams{Month: pgDate(month), OrgID: a.OrgID, Plan: a.Plan, Term: a.Term, MrrMinor: mrr}); err != nil {
			return err
		}
	}
	return nil
}

// RevenueMonth is one month on the dashboard.
type RevenueMonth struct {
	Month         string `json:"month"`
	MRR           int64  `json:"mrr_minor"`
	ARR           int64  `json:"arr_minor"`
	New           int64  `json:"new_minor"`
	Expansion     int64  `json:"expansion_minor"`
	Contraction   int64  `json:"contraction_minor"`
	Churned       int64  `json:"churned_minor"`
	Paying        int    `json:"paying_orgs"`
	ARPA          int64  `json:"arpa_minor"`
	Conversions   int    `json:"conversions"`
	FreeOrgs      int    `json:"free_orgs"`
	UsageRevenue  int64  `json:"usage_revenue_minor"`
	Invoiced      int64  `json:"invoiced_minor"`
	InvoicesCount int64  `json:"invoices"`
	Collected     int64  `json:"collected_minor"`
}

// AgeingBucket is accounts receivable by days past due.
type AgeingBucket struct {
	Label    string `json:"label"`
	Amount   int64  `json:"amount_minor"`
	Invoices int    `json:"invoices"`
}

// Revenue is the dashboard.
type Revenue struct {
	Months         []RevenueMonth `json:"months"`
	Ageing         []AgeingBucket `json:"ageing"`
	OutstandingWHT int64          `json:"outstanding_wht_minor"`
	AsOf           time.Time      `json:"as_of"`
}

// Revenue reports the months ending with this one (months of them).
func (s *Service) Revenue(ctx context.Context, months int) (Revenue, error) {
	if months < 1 || months > 36 {
		months = 12
	}
	now := s.Now()
	to := MonthStart(now)
	from := to.AddDate(0, -(months - 1), 0)
	q := store.New(s.db)
	snaps, err := q.MRRSnapshots(ctx, store.MRRSnapshotsParams{FromMonth: pgDate(from.AddDate(0, -1, 0)), ToMonth: pgDate(to)})
	if err != nil {
		return Revenue{}, err
	}
	type orgMonth struct {
		plan string
		mrr  int64
	}
	byMonth := map[string]map[uuid.UUID]orgMonth{}
	for _, r := range snaps {
		k := r.Month.Time.Format("2006-01")
		if byMonth[k] == nil {
			byMonth[k] = map[uuid.UUID]orgMonth{}
		}
		byMonth[k][r.OrgID] = orgMonth{plan: r.Plan, mrr: r.MrrMinor}
	}
	invoices, err := q.InvoiceTotalsByPeriod(ctx, store.InvoiceTotalsByPeriodParams{FromMonth: pgDate(from), ToMonth: pgDate(to)})
	if err != nil {
		return Revenue{}, err
	}
	inv := map[string]store.InvoiceTotalsByPeriodRow{}
	for _, r := range invoices {
		inv[r.PeriodStart.Time.Format("2006-01")] = r
	}
	usage, err := q.UsageRevenueByPeriod(ctx, store.UsageRevenueByPeriodParams{FromMonth: pgDate(from), ToMonth: pgDate(to)})
	if err != nil {
		return Revenue{}, err
	}
	use := map[string]int64{}
	for _, r := range usage {
		use[r.PeriodStart.Time.Format("2006-01")] = r.AmountMinor
	}
	out := Revenue{AsOf: now}
	for m := from; !m.After(to); m = m.AddDate(0, 1, 0) {
		k, pk := m.Format("2006-01"), m.AddDate(0, -1, 0).Format("2006-01")
		cur, prev := byMonth[k], byMonth[pk]
		rm := RevenueMonth{Month: k, UsageRevenue: use[k]}
		if r, ok := inv[k]; ok {
			rm.Invoiced, rm.InvoicesCount, rm.Collected = r.TotalMinor, r.Issued, r.PaidMinor
		}
		for org, c := range cur {
			p := prev[org]
			rm.MRR += c.mrr
			switch {
			case c.mrr > 0 && p.mrr == 0:
				rm.New += c.mrr
				if p.plan == PlanFree {
					rm.Conversions++
				}
			case c.mrr > p.mrr:
				rm.Expansion += c.mrr - p.mrr
			case c.mrr < p.mrr && c.mrr > 0:
				rm.Contraction += p.mrr - c.mrr
			}
			if c.mrr > 0 {
				rm.Paying++
			}
		}
		for org, p := range prev {
			if p.mrr > 0 && cur[org].mrr == 0 && cur != nil {
				rm.Churned += p.mrr
			}
			if p.plan == PlanFree {
				rm.FreeOrgs++
			}
		}
		rm.ARR = rm.MRR * 12
		if rm.Paying > 0 {
			rm.ARPA = rm.MRR / int64(rm.Paying)
		}
		out.Months = append(out.Months, rm)
	}
	if out.Ageing, err = s.ageing(ctx, now); err != nil {
		return out, err
	}
	wht, err := q.OutstandingWHT(ctx)
	if err != nil {
		return out, err
	}
	for _, w := range wht {
		out.OutstandingWHT += w.WhtDeductedMinor
	}
	return out, nil
}

// ageing buckets open receivables by days past due.
func (s *Service) ageing(ctx context.Context, now time.Time) ([]AgeingBucket, error) {
	rows, err := store.New(s.db).OpenReceivables(ctx)
	if err != nil {
		return nil, err
	}
	buckets := []AgeingBucket{{Label: "Not yet due"}, {Label: "1–30 days"}, {Label: "31–60 days"}, {Label: "61–90 days"}, {Label: "Over 90 days"}}
	for _, r := range rows {
		if r.OutstandingMinor <= 0 {
			continue
		}
		i := 0
		if r.DueAt != nil && now.After(*r.DueAt) {
			switch d := int(now.Sub(*r.DueAt).Hours() / 24); {
			case d <= 30:
				i = 1
			case d <= 60:
				i = 2
			case d <= 90:
				i = 3
			default:
				i = 4
			}
		}
		buckets[i].Amount += r.OutstandingMinor
		buckets[i].Invoices++
	}
	return buckets, nil
}

// WriteRevenueCSV writes the dashboard for the accountant: one row a
// month, then receivables by age and the outstanding WHT. Amounts are in
// naira with kobo.
func WriteRevenueCSV(w io.Writer, r Revenue) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"month", "mrr_ngn", "arr_ngn", "new_ngn", "expansion_ngn", "contraction_ngn", "churned_ngn", "paying_orgs", "arpa_ngn",
		"conversions_from_free", "free_orgs_at_start", "usage_revenue_ngn", "invoices", "invoiced_ngn", "collected_ngn"})
	for _, m := range r.Months {
		_ = cw.Write([]string{m.Month, money(m.MRR), money(m.ARR), money(m.New), money(m.Expansion), money(m.Contraction), money(m.Churned),
			strconv.Itoa(m.Paying), money(m.ARPA), strconv.Itoa(m.Conversions), strconv.Itoa(m.FreeOrgs), money(m.UsageRevenue),
			strconv.FormatInt(m.InvoicesCount, 10), money(m.Invoiced), money(m.Collected)})
	}
	_ = cw.Write([]string{})
	_ = cw.Write([]string{"receivables", "amount_ngn", "invoices"})
	for _, b := range r.Ageing {
		_ = cw.Write([]string{b.Label, money(b.Amount), strconv.Itoa(b.Invoices)})
	}
	_ = cw.Write([]string{"outstanding WHT", money(r.OutstandingWHT), ""})
	cw.Flush()
	return cw.Error()
}
