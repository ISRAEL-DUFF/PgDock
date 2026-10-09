package api

import (
	"net/http"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/authz"
	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// servicesUsageMetrics are the API page's usage rows (V4.1 §9.3), with the
// plan limit each is held to and the factor from the limit's unit to the
// metric's.
var servicesUsageMetrics = []struct {
	metric, service, limit string
	perLimit               float64
}{
	{tenancy.MetricAPIRequests, "data_api", store.LimitAPIRequestsMo, 1},
	{tenancy.MetricAPIEgress, "data_api", "", 0},
	{tenancy.MetricAuthMAU, "auth", store.LimitAuthMAUMo, 1},
	{tenancy.MetricMessagesSMS, "messages", "", 0},
	{tenancy.MetricMessagesWhatsApp, "messages", "", 0},
	{tenancy.MetricStorageGBHours, "storage", "", 0},
	{tenancy.MetricStorageEgress, "storage", store.LimitStorageEgressMBMo, 1.0 / 1024},
	{tenancy.MetricImageTransforms, "storage", store.LimitImageTransformsMo, 1},
	{tenancy.MetricRealtimeConnMinutes, "realtime", "", 0},
	{tenancy.MetricRealtimeMessages, "realtime", store.LimitRealtimeMessagesMo, 1},
}

// GetServicesUsage implements GET /api/v1/projects/{id}/services/usage.
func (s *Server) GetServicesUsage(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ctx := r.Context()
	org := accessFrom(ctx).OrgID
	q := store.New(s.db)
	month := billing.MonthStart(time.Now().UTC())
	rows, err := q.OrgUsageByDay(ctx, store.OrgUsageByDayParams{OrgID: org, FromTs: month, ToTs: month.AddDate(0, 1, 0)})
	if err != nil {
		s.internalError(w, "services usage", err)
		return
	}
	mine, all := map[string]float64{}, map[string]float64{}
	for _, u := range rows {
		f := billing.DecFromNumeric(u.Quantity).Float()
		all[u.Metric] += f
		if u.ProjectID == id {
			mine[u.Metric] += f
		}
	}
	o, err := q.OrgWithPlan(ctx, org)
	if err != nil {
		s.internalError(w, "services usage", err)
		return
	}
	limits, err := store.EffectiveLimits(o.PlanLimits, o.LimitOverrides)
	if err != nil {
		s.internalError(w, "services usage", err)
		return
	}
	var pm *billing.ProjectMonth
	if s.billing != nil {
		if ok, err := s.can(ctx, authz.OrgBillingManage); err != nil {
			s.internalError(w, "services usage", err)
			return
		} else if ok {
			m, err := s.billing.ProjectMonth(ctx, org, id)
			if err != nil {
				s.internalError(w, "services usage", err)
				return
			}
			pm = &m
		}
	}
	out := gen.ServicesUsage{Month: month.Format("2006-01"), Metrics: []gen.ServicesUsageMetric{}}
	for _, m := range servicesUsageMetrics {
		row := gen.ServicesUsageMetric{Metric: m.metric, Service: gen.ServicesUsageMetricService(m.service),
			Quantity: float32(mine[m.metric]), OrgQuantity: float32(all[m.metric])}
		if n, ok := limits.Get(m.limit); m.limit != "" && ok {
			v := float32(float64(n) * m.perLimit)
			row.Limit = &v
		}
		if pm != nil {
			if inc, ok := pm.Included[m.metric]; ok && inc.Sign() > 0 {
				v := float32(inc.Float())
				row.Included = &v
			}
		}
		out.Metrics = append(out.Metrics, row)
	}
	if pm != nil {
		charges := []gen.ServiceCharge{}
		for _, svc := range []string{"database", "data_api", "auth", "messages", "storage", "realtime", "read_replicas"} {
			if a := pm.ByService[svc]; a != 0 {
				charges = append(charges, gen.ServiceCharge{Service: gen.ServiceChargeService(svc), AmountMinor: a})
			}
		}
		out.Charges = &charges
	}
	writeJSON(w, http.StatusOK, out)
}
