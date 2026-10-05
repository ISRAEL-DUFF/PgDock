package api

import (
	"net/http"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/ha"
	"github.com/israel-duff/pgdock/internal/store"
)

func (s *Server) etcdSvc(w http.ResponseWriter) *ha.Service {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return nil
	}
	if ds.Etcd == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "HA is not available on this server")
		return nil
	}
	return ds.Etcd
}

// GetEtcdCluster implements GET /api/v1/admin/etcd.
func (s *Server) GetEtcdCluster(w http.ResponseWriter, r *http.Request) {
	es := s.etcdSvc(w)
	if es == nil {
		return
	}
	ms, err := es.Refresh(r.Context())
	if err != nil {
		s.internalError(w, "etcd members", err)
		return
	}
	out := gen.EtcdCluster{Members: []gen.EtcdMember{}, Ready: true}
	for _, m := range ms {
		out.Members = append(out.Members, gen.EtcdMember{NodeId: m.NodeID, NodeName: m.NodeName, Name: m.Name, ClientUrl: m.ClientUrl,
			Status: gen.EtcdMemberStatus(m.Status), Error: m.Error, CheckedAt: m.CheckedAt})
	}
	if err := es.Ready(r.Context()); err != nil {
		msg := err.Error()
		out.Ready, out.Reason = false, &msg
	}
	writeJSON(w, http.StatusOK, out)
}

// SetupEtcdCluster implements POST /api/v1/admin/etcd.
func (s *Server) SetupEtcdCluster(w http.ResponseWriter, r *http.Request) {
	es := s.etcdSvc(w)
	if es == nil {
		return
	}
	var req gen.EtcdSetupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.set("nodes", req.NodeIds)
	op, err := es.Setup(r.Context(), req.NodeIds, userID(r.Context()))
	if err != nil {
		s.provisionError(w, "etcd setup", err)
		return
	}
	s.writeOperation(w, "etcd setup", op)
}

func (s *Server) haStatus(w http.ResponseWriter, r *http.Request, id gen.ProjectID) (gen.HAStatus, bool) {
	ctx := r.Context()
	q := store.New(s.db)
	p, err := q.GetProject(ctx, id)
	if err != nil {
		s.provisionError(w, "HA", err)
		return gen.HAStatus{}, false
	}
	inst, err := q.GetInstance(ctx, p.InstanceID)
	if err != nil {
		s.internalError(w, "HA", err)
		return gen.HAStatus{}, false
	}
	out := gen.HAStatus{Enabled: inst.HaEnabled, Synchronous: inst.SyncReplication, Members: []gen.HAMember{}, Failovers: []gen.FailoverEvent{}}
	ms, err := q.ListInstanceMembers(ctx, inst.ID)
	if err != nil {
		s.internalError(w, "HA members", err)
		return gen.HAStatus{}, false
	}
	for _, m := range ms {
		updated := m.UpdatedAt
		out.Members = append(out.Members, gen.HAMember{Id: m.ID, NodeId: m.NodeID, NodeName: m.NodeName, Role: gen.HAMemberRole(m.Role),
			State: m.State, LagBytes: m.LagBytes, Timeline: i32(m.Timeline), UpdatedAt: &updated})
	}
	evs, err := q.ListFailoverEvents(ctx, store.ListFailoverEventsParams{InstanceID: inst.ID, Lim: 20})
	if err != nil {
		s.internalError(w, "failover history", err)
		return gen.HAStatus{}, false
	}
	for _, e := range evs {
		out.Failovers = append(out.Failovers, gen.FailoverEvent{Kind: gen.FailoverEventKind(e.Kind), FromNode: e.FromNodeName, ToNode: e.ToNodeName,
			DurationMs: i32(e.DurationMs), OccurredAt: e.OccurredAt})
	}
	if ds := s.backups; ds != nil && ds.Dedicated != nil && inst.HaEnabled {
		if a, err := ds.Dedicated.Availability(ctx, p.ID, time.Now()); err == nil {
			av := gen.Availability{Month: a.Month, MeasuredMinutes: a.Measured, UnavailableMinutes: a.Unavailable}
			outages := []gen.OutageMinute{}
			if a.Percent != nil {
				pc := float32(*a.Percent)
				av.Percent = &pc
			}
			for _, m := range a.Recent {
				outages = append(outages, gen.OutageMinute{Minute: m.Minute, InternalOk: m.InternalOk, ExternalOk: m.ExternalOk})
			}
			av.RecentOutages = &outages
			out.Availability = &av
		} else {
			s.log.Warn("availability", "project", p.ID, "err", err)
		}
	}
	return out, true
}

// GetProjectHA implements GET /api/v1/projects/{id}/ha.
func (s *Server) GetProjectHA(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if out, ok := s.haStatus(w, r, id); ok {
		writeJSON(w, http.StatusOK, out)
	}
}

// EnableProjectHA implements POST /api/v1/projects/{id}/ha.
func (s *Server) EnableProjectHA(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil || !s.requireProjects(w) {
		return
	}
	var req gen.HAEnableRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	sync := req.Synchronous != nil && *req.Synchronous
	a.set("synchronous", sync)
	if s.tenancy != nil && !s.checkQuota(w, s.tenancy.CheckSpendCap(r.Context(), accessFrom(r.Context()).OrgID)) {
		return
	}
	op, err := ds.EnableHA(r.Context(), dedicated.HAParams{ProjectID: id, NodeID: req.NodeId, Synchronous: sync, CreatedBy: userID(r.Context())})
	if err != nil {
		s.provisionError(w, "enable HA", err)
		return
	}
	s.writeOperation(w, "enable HA", op)
}

// UpdateProjectHA implements PATCH /api/v1/projects/{id}/ha.
func (s *Server) UpdateProjectHA(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return
	}
	var req gen.HAUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := store.New(s.db).GetProject(r.Context(), id)
	if err != nil {
		s.provisionError(w, "HA", err)
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	a.set("synchronous", req.Synchronous)
	if err := ds.SetSynchronous(r.Context(), p, req.Synchronous); err != nil {
		s.provisionError(w, "synchronous replication", err)
		return
	}
	s.GetProjectHA(w, r, id)
}

// DisableProjectHA implements DELETE /api/v1/projects/{id}/ha.
func (s *Server) DisableProjectHA(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return
	}
	auditFrom(r.Context()).target("project", id.String())
	op, err := ds.DisableHA(r.Context(), id, userID(r.Context()))
	if err != nil {
		s.provisionError(w, "disable HA", err)
		return
	}
	s.writeOperation(w, "disable HA", op)
}

// SwitchoverProject implements POST /api/v1/projects/{id}/switchover.
func (s *Server) SwitchoverProject(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return
	}
	var req gen.SwitchoverRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	auditFrom(r.Context()).target("project", id.String())
	op, err := ds.Switchover(r.Context(), id, req.Candidate, userID(r.Context()))
	if err != nil {
		s.provisionError(w, "switchover", err)
		return
	}
	s.writeOperation(w, "switchover", op)
}
