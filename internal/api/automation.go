package api

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/outbound"
	"github.com/israel-duff/pgdock/internal/schedjobs"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
	"github.com/israel-duff/pgdock/internal/webhooks"
)

// ---- Webhooks and scheduled jobs (V2 §9) ------------------------------------

func (s *Server) requireAutomation(w http.ResponseWriter) bool {
	if s.webhooks == nil || s.jobs == nil || s.outbound == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "webhooks and jobs are not available on this server")
		return false
	}
	return true
}

func (s *Server) automationError(w http.ResponseWriter, what string, err error) {
	var qe *tenancy.QuotaError
	switch {
	case errors.As(err, &qe):
		writeQuotaError(w, qe)
	case errors.Is(err, webhooks.ErrInvalid), errors.Is(err, schedjobs.ErrInvalid), errors.Is(err, outbound.ErrRefused):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, webhooks.ErrConflict), errors.Is(err, schedjobs.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, outbound.ErrDisabled):
		writeError(w, http.StatusConflict, "outbound_disabled", err.Error())
	case errors.Is(err, webhooks.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	default:
		s.provisionError(w, what, err)
	}
}

// automationProject is the request's live project.
func (s *Server) automationProject(w http.ResponseWriter, r *http.Request, what string) (store.Project, bool) {
	if !s.requireAutomation(w) {
		return store.Project{}, false
	}
	p, err := s.tenantProjectLive(r.Context())
	if err != nil {
		s.provisionError(w, what, err)
		return p, false
	}
	return p, true
}

func (s *Server) toAPIWebhook(wh store.Webhook, backlog int64) gen.Webhook {
	out := gen.Webhook{
		Id: wh.ID, ProjectId: wh.ProjectID, Name: wh.Name, Tables: wh.Tables, Events: wh.Events, Url: wh.Url,
		HeaderNames: []string{}, Enabled: wh.Enabled, Status: gen.WebhookStatus(wh.Status), StatusReason: wh.StatusReason,
		ConsecutiveFailures: int(wh.ConsecutiveFailures), Backlog: backlog, CreatedAt: wh.CreatedAt,
	}
	if len(wh.Columns) > 0 {
		cols := wh.Columns
		out.Columns = &cols
	}
	if h, err := s.webhooks.Headers(wh); err == nil {
		for k := range h {
			out.HeaderNames = append(out.HeaderNames, k)
		}
		slices.Sort(out.HeaderNames)
	}
	return out
}

func events[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, e := range in {
		out[i] = string(e)
	}
	return out
}

// ListWebhooks implements GET /api/v1/projects/{id}/webhooks.
func (s *Server) ListWebhooks(w http.ResponseWriter, r *http.Request, _ gen.ProjectID) {
	p, ok := s.automationProject(w, r, "list webhooks")
	if !ok {
		return
	}
	rows, err := store.New(s.db).ListWebhooks(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, "list webhooks", err)
		return
	}
	backlog := map[[16]byte]int64{}
	if len(rows) > 0 {
		if b, err := s.webhooks.Backlog(r.Context(), p); err == nil {
			for k, v := range b {
				backlog[k] = v
			}
		}
	}
	out := gen.WebhookList{Items: make([]gen.Webhook, 0, len(rows))}
	for _, wh := range rows {
		out.Items = append(out.Items, s.toAPIWebhook(wh, backlog[wh.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateWebhook implements POST /api/v1/projects/{id}/webhooks.
func (s *Server) CreateWebhook(w http.ResponseWriter, r *http.Request, _ gen.ProjectID) {
	p, ok := s.automationProject(w, r, "create webhook")
	if !ok {
		return
	}
	var req gen.WebhookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", p.ID.String())
	a.set("name", req.Name)
	a.set("url", req.Url)
	in := webhooks.Params{Name: req.Name, Tables: req.Tables, Events: events(req.Events), URL: req.Url, Enabled: req.Enabled == nil || *req.Enabled}
	if req.Columns != nil {
		in.Columns = *req.Columns
	}
	if req.Headers != nil {
		in.Headers = *req.Headers
	}
	c, err := s.webhooks.Create(r.Context(), p, in, userID(r.Context()))
	if err != nil {
		s.automationError(w, "create webhook", err)
		return
	}
	a.set("webhook_id", c.Webhook.ID.String())
	writeJSON(w, http.StatusCreated, gen.WebhookCreated{Webhook: s.toAPIWebhook(c.Webhook, 0), Secret: c.Secret})
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request, id gen.WebhookID, what string) (store.Project, store.Webhook, bool) {
	p, ok := s.automationProject(w, r, what)
	if !ok {
		return p, store.Webhook{}, false
	}
	wh, err := store.New(s.db).GetWebhook(r.Context(), store.GetWebhookParams{ID: id, ProjectID: p.ID})
	if err != nil {
		s.automationError(w, what, err)
		return p, wh, false
	}
	a := auditFrom(r.Context())
	a.target("project", p.ID.String())
	a.set("webhook_id", wh.ID.String())
	return p, wh, true
}

// GetWebhook implements GET /api/v1/projects/{id}/webhooks/{webhook_id}.
func (s *Server) GetWebhook(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.WebhookID) {
	p, wh, ok := s.webhook(w, r, id, "get webhook")
	if !ok {
		return
	}
	b, _ := s.webhooks.Backlog(r.Context(), p)
	writeJSON(w, http.StatusOK, s.toAPIWebhook(wh, b[wh.ID]))
}

// UpdateWebhook implements PATCH /api/v1/projects/{id}/webhooks/{webhook_id}.
func (s *Server) UpdateWebhook(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.WebhookID) {
	p, wh, ok := s.webhook(w, r, id, "update webhook")
	if !ok {
		return
	}
	var req gen.WebhookUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	in := webhooks.Params{Name: wh.Name, Tables: wh.Tables, Events: wh.Events, Columns: wh.Columns, URL: wh.Url, Enabled: wh.Enabled}
	if req.Name != nil {
		in.Name = *req.Name
	}
	if req.Tables != nil {
		in.Tables = *req.Tables
	}
	if req.Events != nil {
		in.Events = events(*req.Events)
	}
	if req.Columns != nil {
		in.Columns = *req.Columns
	}
	if req.Url != nil {
		in.URL = *req.Url
	}
	if req.Headers != nil {
		in.Headers = *req.Headers
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
		auditFrom(r.Context()).set("enabled", in.Enabled)
	}
	out, err := s.webhooks.Update(r.Context(), p, wh, in)
	if err != nil {
		s.automationError(w, "update webhook", err)
		return
	}
	b, _ := s.webhooks.Backlog(r.Context(), p)
	writeJSON(w, http.StatusOK, s.toAPIWebhook(out, b[out.ID]))
}

// DeleteWebhook implements DELETE /api/v1/projects/{id}/webhooks/{webhook_id}.
func (s *Server) DeleteWebhook(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.WebhookID) {
	p, wh, ok := s.webhook(w, r, id, "delete webhook")
	if !ok {
		return
	}
	if err := s.webhooks.Delete(r.Context(), p, wh); err != nil {
		s.automationError(w, "delete webhook", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestWebhook implements POST /api/v1/projects/{id}/webhooks/{webhook_id}/test.
func (s *Server) TestWebhook(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.WebhookID) {
	p, wh, ok := s.webhook(w, r, id, "test webhook")
	if !ok {
		return
	}
	res, err := s.webhooks.SendTest(r.Context(), p, wh)
	if err != nil {
		s.automationError(w, "test webhook", err)
		return
	}
	out := gen.WebhookTestResult{EventId: res.EventID, Ok: res.Error == "" && res.StatusCode >= 200 && res.StatusCode < 300}
	if res.StatusCode > 0 {
		out.StatusCode = &res.StatusCode
		out.Response = &res.Snippet
	}
	if res.Latency > 0 {
		ms := int(res.Latency.Milliseconds())
		out.LatencyMs = &ms
	}
	if res.Error != "" {
		out.Error = &res.Error
	}
	writeJSON(w, http.StatusOK, out)
}

// RotateWebhookSecret implements POST /api/v1/projects/{id}/webhooks/{webhook_id}/rotate-secret.
func (s *Server) RotateWebhookSecret(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.WebhookID) {
	_, wh, ok := s.webhook(w, r, id, "rotate webhook secret")
	if !ok {
		return
	}
	secret, err := s.webhooks.RotateSecret(r.Context(), wh)
	if err != nil {
		s.automationError(w, "rotate webhook secret", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.WebhookSecret{Secret: secret})
}

// ListWebhookDeliveries implements GET /api/v1/projects/{id}/webhooks/{webhook_id}/deliveries.
func (s *Server) ListWebhookDeliveries(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.WebhookID, params gen.ListWebhookDeliveriesParams) {
	_, wh, ok := s.webhook(w, r, id, "webhook deliveries")
	if !ok {
		return
	}
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	rows, err := store.New(s.db).ListDeliveries(r.Context(), store.ListDeliveriesParams{
		WebhookID: wh.ID, DeadOnly: params.Dead != nil && *params.Dead, MaxRows: int32(limit),
	})
	if err != nil {
		s.internalError(w, "webhook deliveries", err)
		return
	}
	out := gen.WebhookDeliveryList{Items: make([]gen.WebhookDelivery, 0, len(rows))}
	for _, d := range rows {
		item := gen.WebhookDelivery{
			Id: d.ID, EventId: d.EventID, Attempt: int(d.Attempt), Response: d.ResponseSnippet, Error: d.Error,
			Succeeded: d.Succeeded, DeadLettered: d.DeadLettered, ReplayedAt: d.ReplayedAt, CreatedAt: d.CreatedAt,
		}
		if d.StatusCode != nil {
			c := int(*d.StatusCode)
			item.StatusCode = &c
		}
		if d.LatencyMs != nil {
			l := int(*d.LatencyMs)
			item.LatencyMs = &l
		}
		out.Items = append(out.Items, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// ReplayWebhook implements POST /api/v1/projects/{id}/webhooks/{webhook_id}/replay.
func (s *Server) ReplayWebhook(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.WebhookID) {
	p, wh, ok := s.webhook(w, r, id, "replay webhook")
	if !ok {
		return
	}
	var req gen.ReplayRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var ids []int64
	switch {
	case req.All != nil && *req.All:
	case req.Ids != nil && len(*req.Ids) > 0:
		ids = *req.Ids
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "name the dead letters (ids) or replay all of them (all: true)")
		return
	}
	n, err := s.webhooks.Replay(r.Context(), p, wh, ids)
	if err != nil {
		s.automationError(w, "replay webhook", err)
		return
	}
	auditFrom(r.Context()).set("replayed", n)
	writeJSON(w, http.StatusOK, gen.ReplayResult{Queued: n})
}

// ---- Scheduled jobs --------------------------------------------------------

func (s *Server) toAPIJob(r *http.Request, j store.ScheduledJob) gen.Job {
	out := gen.Job{
		Id: j.ID, ProjectId: j.ProjectID, Name: j.Name, Cron: j.Cron, Timezone: j.Timezone, Kind: gen.JobKind(j.Kind),
		TimeoutSeconds: int(j.TimeoutS), Overlap: gen.JobOverlap(j.Overlap), Enabled: j.Enabled, NextRunAt: j.NextRunAt,
		ConsecutiveFailures: int(j.ConsecutiveFailures), Upcoming: schedjobs.Upcoming(j, s.now(), 5), CreatedAt: j.CreatedAt,
	}
	if out.Upcoming == nil {
		out.Upcoming = []time.Time{}
	}
	if sql, h, _, err := s.jobs.Spec(j); err == nil {
		if j.Kind == schedjobs.KindSQL {
			out.Sql = &sql
		} else if h != nil {
			out.Http = toAPIHTTPSpec(h)
		}
	}
	if last, err := store.New(s.db).LastJobRun(r.Context(), j.ID); err == nil {
		lr := toAPIJobRun(last)
		out.LastRun = &lr
	}
	return out
}

func toAPIHTTPSpec(h *schedjobs.HTTPSpec) *gen.HttpJobSpec {
	m := gen.HttpJobSpecMethod(h.Method)
	out := &gen.HttpJobSpec{Method: &m, Url: h.URL}
	if len(h.Headers) > 0 {
		hd := h.Headers
		out.Headers = &hd
	}
	if h.Body != "" {
		b := h.Body
		out.Body = &b
	}
	return out
}

func fromAPIHTTPSpec(h *gen.HttpJobSpec) *schedjobs.HTTPSpec {
	if h == nil {
		return nil
	}
	out := &schedjobs.HTTPSpec{URL: h.Url}
	if h.Method != nil {
		out.Method = string(*h.Method)
	}
	if h.Headers != nil {
		out.Headers = *h.Headers
	}
	if h.Body != nil {
		out.Body = *h.Body
	}
	return out
}

func toAPIJobRun(r store.JobRun) gen.JobRun {
	out := gen.JobRun{
		Id: r.ID, ScheduledFor: r.ScheduledFor, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Status: gen.JobRunStatus(r.Status),
		RowsAffected: r.RowsAffected, Error: r.Error, Trigger: gen.JobRunTrigger(r.Trigger),
	}
	if r.StatusCode != nil {
		c := int(*r.StatusCode)
		out.StatusCode = &c
	}
	return out
}

// ListJobs implements GET /api/v1/projects/{id}/jobs.
func (s *Server) ListJobs(w http.ResponseWriter, r *http.Request, _ gen.ProjectID) {
	p, ok := s.automationProject(w, r, "list jobs")
	if !ok {
		return
	}
	rows, err := store.New(s.db).ListJobs(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, "list jobs", err)
		return
	}
	out := gen.JobList{Items: make([]gen.Job, 0, len(rows))}
	for _, j := range rows {
		out.Items = append(out.Items, s.toAPIJob(r, j))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateJob implements POST /api/v1/projects/{id}/jobs.
func (s *Server) CreateJob(w http.ResponseWriter, r *http.Request, _ gen.ProjectID) {
	p, ok := s.automationProject(w, r, "create job")
	if !ok {
		return
	}
	var req gen.JobRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", p.ID.String())
	a.set("name", req.Name)
	a.set("kind", string(req.Kind))
	a.set("cron", req.Cron)
	in := schedjobs.Params{Name: req.Name, Cron: req.Cron, Kind: string(req.Kind), HTTP: fromAPIHTTPSpec(req.Http), Enabled: req.Enabled == nil || *req.Enabled}
	if req.Timezone != nil {
		in.Timezone = *req.Timezone
	}
	if req.Sql != nil {
		in.SQL = *req.Sql
	}
	if req.TimeoutSeconds != nil {
		in.Timeout = time.Duration(*req.TimeoutSeconds) * time.Second
	}
	if req.Overlap != nil {
		in.Overlap = string(*req.Overlap)
	}
	if in.HTTP != nil {
		a.set("url", in.HTTP.URL)
	}
	c, err := s.jobs.Create(r.Context(), p, in, userID(r.Context()))
	if err != nil {
		s.automationError(w, "create job", err)
		return
	}
	a.set("job_id", c.Job.ID.String())
	out := gen.JobCreated{Job: s.toAPIJob(r, c.Job)}
	if c.Secret != "" {
		out.Secret = &c.Secret
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) job(w http.ResponseWriter, r *http.Request, id gen.JobID, what string) (store.Project, store.ScheduledJob, bool) {
	p, ok := s.automationProject(w, r, what)
	if !ok {
		return p, store.ScheduledJob{}, false
	}
	j, err := store.New(s.db).GetJob(r.Context(), store.GetJobParams{ID: id, ProjectID: p.ID})
	if err != nil {
		s.automationError(w, what, err)
		return p, j, false
	}
	a := auditFrom(r.Context())
	a.target("project", p.ID.String())
	a.set("job_id", j.ID.String())
	return p, j, true
}

// GetJob implements GET /api/v1/projects/{id}/jobs/{job_id}.
func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.JobID) {
	if _, j, ok := s.job(w, r, id, "get job"); ok {
		writeJSON(w, http.StatusOK, s.toAPIJob(r, j))
	}
}

// UpdateJob implements PATCH /api/v1/projects/{id}/jobs/{job_id}.
func (s *Server) UpdateJob(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.JobID) {
	p, j, ok := s.job(w, r, id, "update job")
	if !ok {
		return
	}
	var req gen.JobUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	sql, h, _, err := s.jobs.Spec(j)
	if err != nil {
		s.internalError(w, "update job", err)
		return
	}
	in := schedjobs.Params{Name: j.Name, Cron: j.Cron, Timezone: j.Timezone, Kind: j.Kind, SQL: sql, HTTP: h,
		Timeout: time.Duration(j.TimeoutS) * time.Second, Overlap: j.Overlap, Enabled: j.Enabled}
	if req.Name != nil {
		in.Name = *req.Name
	}
	if req.Cron != nil {
		in.Cron = *req.Cron
	}
	if req.Timezone != nil {
		in.Timezone = *req.Timezone
	}
	if req.Sql != nil {
		in.SQL = *req.Sql
	}
	if req.Http != nil {
		in.HTTP = fromAPIHTTPSpec(req.Http)
	}
	if req.TimeoutSeconds != nil {
		in.Timeout = time.Duration(*req.TimeoutSeconds) * time.Second
	}
	if req.Overlap != nil {
		in.Overlap = string(*req.Overlap)
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
		auditFrom(r.Context()).set("enabled", in.Enabled)
	}
	out, err := s.jobs.Update(r.Context(), p, j, in)
	if err != nil {
		s.automationError(w, "update job", err)
		return
	}
	writeJSON(w, http.StatusOK, s.toAPIJob(r, out))
}

// DeleteJob implements DELETE /api/v1/projects/{id}/jobs/{job_id}.
func (s *Server) DeleteJob(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.JobID) {
	_, j, ok := s.job(w, r, id, "delete job")
	if !ok {
		return
	}
	if err := store.New(s.db).DeleteJob(r.Context(), j.ID); err != nil {
		s.internalError(w, "delete job", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunJob implements POST /api/v1/projects/{id}/jobs/{job_id}/run.
func (s *Server) RunJob(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.JobID) {
	_, j, ok := s.job(w, r, id, "run job")
	if !ok {
		return
	}
	run, err := s.jobs.RunNow(r.Context(), j)
	if err != nil {
		s.automationError(w, "run job", err)
		return
	}
	writeJSON(w, http.StatusAccepted, toAPIJobRun(run))
}

// ListJobRuns implements GET /api/v1/projects/{id}/jobs/{job_id}/runs.
func (s *Server) ListJobRuns(w http.ResponseWriter, r *http.Request, _ gen.ProjectID, id gen.JobID, params gen.ListJobRunsParams) {
	_, j, ok := s.job(w, r, id, "job runs")
	if !ok {
		return
	}
	limit := 100
	if params.Limit != nil {
		limit = *params.Limit
	}
	rows, err := store.New(s.db).ListJobRuns(r.Context(), store.ListJobRunsParams{JobID: j.ID, MaxRows: int32(limit)})
	if err != nil {
		s.internalError(w, "job runs", err)
		return
	}
	out := gen.JobRunList{Items: make([]gen.JobRun, 0, len(rows))}
	for _, run := range rows {
		out.Items = append(out.Items, toAPIJobRun(run))
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Outbound controls (platform admin, V2 §10.7) --------------------------

func (s *Server) orgOutbound(r *http.Request, org gen.OrgID) (gen.OrgOutbound, error) {
	q := store.New(s.db)
	o, err := q.GetOrg(r.Context(), org)
	if err != nil {
		return gen.OrgOutbound{}, err
	}
	allow, err := q.ListOutboundAllowlist(r.Context(), org)
	if err != nil {
		return gen.OrgOutbound{}, err
	}
	counters, err := q.ListOutboundCounters(r.Context(), store.ListOutboundCountersParams{OrgID: org, Since: outbound.Day(s.now().AddDate(0, 0, -30))})
	if err != nil {
		return gen.OrgOutbound{}, err
	}
	out := gen.OrgOutbound{Allowlist: []string{}, OutboundDisabled: o.OutboundDisabled, Hosts: []gen.OutboundHost{}}
	for _, a := range allow {
		out.Allowlist = append(out.Allowlist, a.Host)
	}
	for _, c := range counters {
		out.Hosts = append(out.Hosts, gen.OutboundHost{Host: c.Host, Requests: c.Requests, Failures: c.Failures, LastDay: openapi_types.Date{Time: c.LastDay.Time}})
	}
	return out, nil
}

// AdminGetOrgOutbound implements GET /api/v1/admin/orgs/{org}/outbound.
func (s *Server) AdminGetOrgOutbound(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	if !s.requireAutomation(w) {
		return
	}
	out, err := s.orgOutbound(r, org)
	if err != nil {
		s.automationError(w, "outbound", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminSetOrgOutboundAllowlist implements PUT /api/v1/admin/orgs/{org}/outbound.
func (s *Server) AdminSetOrgOutboundAllowlist(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	if !s.requireAutomation(w) {
		return
	}
	var req gen.OutboundAllowlist
	if !decodeJSON(w, r, &req) {
		return
	}
	want := map[string]bool{}
	for _, h := range req.Hosts {
		if err := outbound.ValidAllowHost(h); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", h+": "+err.Error())
			return
		}
		want[outbound.NormalizeHost(h)] = true
	}
	if len(want) > 100 {
		writeError(w, http.StatusBadRequest, "bad_request", "at most 100 hosts")
		return
	}
	a := auditFrom(r.Context())
	a.target("org", org.String())
	a.set("hosts", req.Hosts)
	err := func() error {
		q := store.New(s.db)
		if _, err := q.GetOrg(r.Context(), org); err != nil {
			return err
		}
		cur, err := q.ListOutboundAllowlist(r.Context(), org)
		if err != nil {
			return err
		}
		for _, c := range cur {
			if !want[c.Host] {
				if _, err := q.RemoveOutboundAllow(r.Context(), store.RemoveOutboundAllowParams{OrgID: org, Host: c.Host}); err != nil {
					return err
				}
			}
		}
		for h := range want {
			if err := q.AddOutboundAllow(r.Context(), store.AddOutboundAllowParams{OrgID: org, Host: h, CreatedBy: userID(r.Context())}); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		s.automationError(w, "outbound", err)
		return
	}
	out, err := s.orgOutbound(r, org)
	if err != nil {
		s.automationError(w, "outbound", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
