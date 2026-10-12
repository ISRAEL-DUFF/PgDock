package billing

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/store"
)

// ProjectMonth is one project's part of its organisation's month so far
// (V4.1 §9.3), for the API page.
type ProjectMonth struct {
	Month time.Time
	// Included is the organisation's plan allowance per metric this month,
	// shared by its projects.
	Included map[string]Dec
	// ByService is the month's charges so far (before VAT) that fall to the
	// project, per service (LineService): its own lines in full, and of
	// each allowance's overage the share the project's usage of that metric
	// is of the organisation's. The plan fee is the organisation's alone.
	ByService map[string]int64
}

// ProjectMonth rates orgID's current month and attributes it to projectID.
func (s *Service) ProjectMonth(ctx context.Context, orgID, projectID uuid.UUID) (ProjectMonth, error) {
	mStart := MonthStart(s.Now().UTC())
	out := ProjectMonth{Month: mStart, Included: map[string]Dec{}, ByService: map[string]int64{}}
	r, err := s.rate(ctx, orgID, mStart, nil, Dec{})
	if err != nil {
		return out, err
	}
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return out, err
	}
	if b, err := s.GetBook(ctx, a.PriceBookVersion); err == nil {
		for m, v := range b.Prices.Plans[a.Plan].Included {
			out.Included[m] = v
		}
	}
	rows, err := store.New(s.db).OrgUsageByDay(ctx, store.OrgUsageByDayParams{OrgID: orgID, FromTs: mStart, ToTs: mStart.AddDate(0, 1, 0)})
	if err != nil {
		return out, err
	}
	org, proj := map[string]Dec{}, map[string]Dec{}
	for _, u := range rows {
		q := DecFromNumeric(u.Quantity)
		org[u.Metric] = org[u.Metric].Add(q)
		if u.ProjectID == projectID {
			proj[u.Metric] = proj[u.Metric].Add(q)
		}
	}
	for _, l := range r.Lines {
		switch {
		case l.Advance:
		case l.ProjectID != nil:
			if *l.ProjectID == projectID {
				out.ByService[LineService(l)] += l.Amount
			}
		case l.Metric != "" && org[l.Metric].Sign() > 0 && proj[l.Metric].Sign() > 0:
			out.ByService[LineService(l)] += DecInt(l.Amount).Mul(proj[l.Metric]).Quo(org[l.Metric]).Round()
		}
	}
	return out, nil
}
