package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/bloom"
	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// Per-plan limits for backend services (V4 §10, V4.1 §3). The quota plan
// sets monthly hard limits (Free's data API requests and monthly active
// users) and ceilings on a project's own timeout and rate limits. This
// sweep works them out per organisation and writes what the edge applies
// onto each project's services row, which moves the feed.

// mauFilterRate is the counted-users filter's false-positive rate: a false
// positive lets a user who wasn't counted sign in.
const mauFilterRate = 0.01

// planLimitsFromKey is the setting holding when the plan limits start
// (the migration's first full month for installs upgrading).
const planLimitsFromKey = "plan_limits_from"

// PlanLimitsFrom is when the plan limits start applying.
func (s *Service) PlanLimitsFrom(ctx context.Context) (time.Time, error) {
	raw, err := store.New(s.db).GetSetting(ctx, planLimitsFromKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	var at time.Time
	if err := json.Unmarshal(raw, &at); err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", planLimitsFromKey, err)
	}
	return at, nil
}

// PlanCeilings are the most a project's settings may ask for (nil: no
// ceiling).
type PlanCeilings struct {
	TimeoutMs, RatePerIP, RatePerKey *int32
}

// planLimitState is what the sweep decided for an organisation.
type planLimitState struct {
	ceilings                 PlanCeilings
	requestsBlocked          bool
	mauBlocked               bool
	requestsUsed, mauUsed    float64
	requestsCap, mauCap      int64
	hasRequestCap, hasMAUCap bool
}

func ceiling(l store.Limits, key string) *int32 {
	if v, ok := l.Get(key); ok && v > 0 {
		n := int32(min(v, 1<<30))
		return &n
	}
	return nil
}

// PlanLimitsSweep works out each organisation's limits and writes them for
// the edge; it also sends the 80% and 100% notices. It runs with the
// storage sweep.
func (s *Service) PlanLimitsSweep(ctx context.Context) error {
	q := store.New(s.db)
	from, err := s.PlanLimitsFrom(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	active := !now.Before(from)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	rows, err := q.PlanLimitProjects(ctx)
	if err != nil {
		return err
	}
	byOrg := map[uuid.UUID][]store.PlanLimitProjectsRow{}
	var orgs []uuid.UUID
	for _, r := range rows {
		if _, seen := byOrg[r.OrgID]; !seen {
			orgs = append(orgs, r.OrgID)
		}
		byOrg[r.OrgID] = append(byOrg[r.OrgID], r)
	}
	var errs []error
	for _, org := range orgs {
		st, err := s.orgPlanLimits(ctx, q, org, month)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		s.planLimitNotices(ctx, q, org, month, from, st)
		if !active {
			st = planLimitState{} // announced, not applied yet
		}
		for _, r := range byOrg[org] {
			var counted []byte
			if st.mauBlocked {
				ids, err := q.ProjectMonthUsers(ctx, store.ProjectMonthUsersParams{ProjectID: r.ProjectID,
					Month: monthOf(month)})
				if err != nil {
					errs = append(errs, err)
					continue
				}
				counted = bloom.New(ids, mauFilterRate)
			}
			if _, err := q.SetPlanLimits(ctx, store.SetPlanLimitsParams{ProjectID: r.ProjectID,
				TimeoutMs: st.ceilings.TimeoutMs, RatePerIp: st.ceilings.RatePerIP, RatePerKey: st.ceilings.RatePerKey,
				ApiRequestsBlocked: st.requestsBlocked, MauBlocked: st.mauBlocked, MauCounted: counted}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// orgPlanLimits is an organisation's ceilings and whether its monthly
// limits are used up.
func (s *Service) orgPlanLimits(ctx context.Context, q *store.Queries, org uuid.UUID, month time.Time) (planLimitState, error) {
	var st planLimitState
	o, err := q.OrgWithPlan(ctx, org)
	if err != nil {
		return st, err
	}
	l, err := store.EffectiveLimits(o.PlanLimits, o.LimitOverrides)
	if err != nil {
		return st, err
	}
	st.ceilings = PlanCeilings{TimeoutMs: ceiling(l, store.LimitAPITimeoutMs), RatePerIP: ceiling(l, store.LimitAPIRatePerIP),
		RatePerKey: ceiling(l, store.LimitAPIRatePerKey)}
	if n, ok := l.Get(store.LimitAPIRequestsMo); ok {
		used, err := q.OrgUsageSince(ctx, store.OrgUsageSinceParams{OrgID: org, Metric: tenancy.MetricAPIRequests, Since: month})
		if err != nil {
			return st, err
		}
		st.hasRequestCap, st.requestsCap, st.requestsUsed = true, n, numericFloat(used)
		st.requestsBlocked = st.requestsUsed >= float64(n)
	}
	if n, ok := l.Get(store.LimitAuthMAUMo); ok {
		used, err := q.OrgUsageSince(ctx, store.OrgUsageSinceParams{OrgID: org, Metric: tenancy.MetricAuthMAU, Since: month})
		if err != nil {
			return st, err
		}
		st.hasMAUCap, st.mauCap, st.mauUsed = true, n, numericFloat(used)
		st.mauBlocked = st.mauUsed >= float64(n)
	}
	return st, nil
}

// planLimitNotices emails the organisation's owners and admins once a
// month at 80% and at 100% of each hard limit, before the limits apply too
// (so the first month's notice says when they start).
func (s *Service) planLimitNotices(ctx context.Context, q *store.Queries, org uuid.UUID, month, from time.Time, st planLimitState) {
	if s.Mail == nil {
		return
	}
	type check struct {
		key, what   string
		used        float64
		cap         int64
		has         bool
		atLimitDoes string
	}
	for _, c := range []check{
		{store.LimitAPIRequestsMo, "data API requests", st.requestsUsed, st.requestsCap, st.hasRequestCap,
			"the data API, storage and realtime answer 429 until the month ends (sign-in keeps working)"},
		{store.LimitAuthMAUMo, "monthly active users", st.mauUsed, st.mauCap, st.hasMAUCap,
			"users who already signed in this month can sign in again, but new ones can't until the month ends"},
	} {
		if !c.has || c.cap <= 0 {
			continue
		}
		level := 0
		switch {
		case c.used >= float64(c.cap):
			level = 100
		case c.used >= 0.8*float64(c.cap):
			level = 80
		default:
			continue
		}
		n, err := q.InsertPlanLimitNotice(ctx, store.InsertPlanLimitNoticeParams{OrgID: org, LimitKey: c.key, Month: monthOf(month), Level: int32(level)})
		if err != nil || n == 0 {
			continue
		}
		o, err := q.GetOrg(ctx, org)
		if err != nil {
			continue
		}
		addrs, err := q.ProjectAdminEmails(ctx, store.ProjectAdminEmailsParams{OrgID: org, ProjectID: uuid.Nil})
		if err != nil || len(addrs) == 0 {
			continue
		}
		when := "At the limit, " + c.atLimitDoes + "."
		if time.Now().Before(from) {
			when = fmt.Sprintf("Plan limits apply from %s; from then, at the limit, %s.", from.Format("2 January 2006"), c.atLimitDoes)
		}
		subject := fmt.Sprintf("[PGDock] %s has used %d%% of its %s this month", o.Name, level, c.what)
		body := fmt.Sprintf("%s has used %.0f of the %d %s its plan includes this month.\n\n%s\n\n"+
			"Upgrade the plan (Organisation → Billing) for more; paid plans are billed past their inclusions instead of stopping.\n",
			o.Name, c.used, c.cap, c.what, when)
		go func(to []string) {
			sctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
			defer cancel()
			if err := s.Mail.Send(sctx, mail.Message{To: to, Subject: subject, Body: body}); err != nil {
				s.log.Warn("plan limit notice", "org", org, "err", err)
			}
		}(addrs)
	}
}

// EffectiveSettings applies the plan's ceilings to a project's settings
// (with the defaults filled in).
func EffectiveSettings(st Settings, c PlanCeilings) (timeoutMs, perIP, perKey int) {
	timeoutMs, perIP, perKey = or(st.StatementTimeoutMs, DefaultStatementTimeoutMs), or(st.RatePerIP, DefaultRatePerIP), or(st.RatePerKey, DefaultRatePerKey)
	if c.TimeoutMs != nil {
		timeoutMs = min(timeoutMs, int(*c.TimeoutMs))
	}
	if c.RatePerIP != nil {
		perIP = min(perIP, int(*c.RatePerIP))
	}
	if c.RatePerKey != nil {
		perKey = min(perKey, int(*c.RatePerKey))
	}
	return
}
