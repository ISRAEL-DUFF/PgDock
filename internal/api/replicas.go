package api

import (
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/store"
)

// ListProjectReplicas implements GET /api/v1/projects/{id}/replicas.
func (s *Server) ListProjectReplicas(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return
	}
	ctx := r.Context()
	q := store.New(s.db)
	p, err := q.GetProject(ctx, id)
	if err != nil {
		s.provisionError(w, "read replicas", err)
		return
	}
	rows, err := q.ListProjectReplicas(ctx, p.ID)
	if err != nil {
		s.internalError(w, "read replicas", err)
		return
	}
	read := p.DbName + pooler.ReadOnlySuffix
	out := gen.ReplicaList{Replicas: []gen.ReadReplica{}, MaxReplicas: dedicated.MaxReplicas, MaxLagMs: ds.ReplicaMaxLag().Milliseconds(), ReadDatabase: read}
	if len(rows) > 0 {
		c := s.projects.ConnectionFor(p)
		c.Database = read
		u := c.PooledURL("")
		out.ReadUrl = &u
	}
	for _, rr := range rows {
		region := rr.NodeRegion
		if rr.RegionID != nil {
			region = *rr.RegionID
		}
		out.Replicas = append(out.Replicas, gen.ReadReplica{Id: rr.ID, NodeId: rr.NodeID, NodeName: rr.NodeName, Region: region, Size: rr.Size,
			Status: gen.ReadReplicaStatus(rr.Status), InRotation: rr.InRotation, LagBytes: rr.LagBytes, LagMs: rr.LagMs, Error: rr.Error,
			RotationChangedAt: rr.RotationChangedAt, CreatedAt: rr.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProjectReplica implements POST /api/v1/projects/{id}/replicas.
func (s *Server) CreateProjectReplica(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return
	}
	var req gen.ReplicaCreateRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	if s.tenancy != nil {
		// A replica is an instance the size of the primary's (V4 §7).
		org := accessFrom(r.Context()).OrgID
		if !s.checkQuota(w, s.tenancy.CheckOperation(r.Context(), org)) {
			return
		}
		if src, err := s.tenantProjectLive(r.Context()); err == nil {
			if inst, err := store.New(s.db).GetInstance(r.Context(), src.InstanceID); err == nil && !s.withinAllowance(w, r, org, instanceSize(inst)) {
				return
			}
		}
	}
	p := dedicated.ReplicaParams{ProjectID: id, NodeID: req.NodeId, CreatedBy: userID(r.Context())}
	if req.Region != nil {
		p.Region = *req.Region
		a.set("region", p.Region)
	}
	if req.NodeId != nil {
		a.set("node", req.NodeId.String())
	}
	op, err := ds.CreateReplica(r.Context(), p)
	if err != nil {
		s.provisionError(w, "create read replica", err)
		return
	}
	s.writeOperation(w, "create read replica", op)
}

// DeleteProjectReplica implements DELETE /api/v1/projects/{id}/replicas/{replica_id}.
func (s *Server) DeleteProjectReplica(w http.ResponseWriter, r *http.Request, id gen.ProjectID, replicaID gen.ReplicaID) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	a.set("replica", replicaID.String())
	op, err := ds.DeleteReplica(r.Context(), id, replicaID, userID(r.Context()))
	if err != nil {
		s.provisionError(w, "delete read replica", err)
		return
	}
	s.writeOperation(w, "delete read replica", op)
}

// DetachProjectReplica implements POST /api/v1/projects/{id}/replicas/{replica_id}/detach.
func (s *Server) DetachProjectReplica(w http.ResponseWriter, r *http.Request, id gen.ProjectID, replicaID gen.ReplicaID) {
	ds := s.dedicatedSvc(w)
	if ds == nil || !s.requireProjects(w) {
		return
	}
	var req gen.ReplicaDetachRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	a.set("replica", replicaID.String())
	if s.tenancy != nil {
		org := accessFrom(r.Context()).OrgID
		if !s.checkQuota(w, s.tenancy.CheckCreateProject(r.Context(), org)) || !s.checkQuota(w, s.tenancy.CheckOperation(r.Context(), org)) {
			return
		}
	}
	c, err := ds.DetachReplica(r.Context(), dedicated.DetachParams{ProjectID: id, ReplicaID: replicaID, Name: req.Name,
		CreatedBy: userID(r.Context()), CreatorRole: creatorRole(accessFrom(r.Context()))})
	if err != nil {
		s.provisionError(w, "detach read replica", err)
		return
	}
	a.set("new_project", c.Project.ID.String())
	s.writeCredentials(w, c.Project, c.Operation, c.Password)
}
