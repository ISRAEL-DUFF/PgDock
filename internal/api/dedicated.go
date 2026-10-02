package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/backup"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

func (s *Server) dedicatedSvc(w http.ResponseWriter) *dedicated.Service {
	if s.backups == nil || s.backups.Dedicated == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "the dedicated tier is not available on this server")
		return nil
	}
	return s.backups.Dedicated
}

func i32(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

// instanceSummaries maps live instances to their API summary.
func (s *Server) instanceSummaries(ctx context.Context) map[uuid.UUID]gen.InstanceSummary {
	out := map[uuid.UUID]gen.InstanceSummary{}
	if s.db == nil {
		return out
	}
	rows, err := store.New(s.db).ListInstanceSummaries(ctx)
	if err != nil {
		s.log.Warn("instance summaries", "err", err)
		return out
	}
	for _, r := range rows {
		sum := gen.InstanceSummary{
			Id: r.ID, Kind: gen.InstanceSummaryKind(r.Kind), Status: r.Status, Error: r.Error,
			NodeId: r.NodeID, NodeName: r.NodeName, Profile: r.Profile, MemoryMb: i32(r.MemLimitMb), VolumeGb: i32(r.VolumeGb),
		}
		if f, err := r.CpuLimit.Float64Value(); err == nil && f.Valid {
			v := float32(f.Float64)
			sum.Cpus = &v
		}
		out[r.ID] = sum
	}
	return out
}

// ListProfiles implements GET /api/v1/profiles.
func (s *Server) ListProfiles(w http.ResponseWriter, _ *http.Request) {
	out := gen.ProfileList{DefaultProfile: dedicated.DefaultProfile, DefaultVolumeGb: dedicated.DefaultVolumeGB}
	for _, p := range dedicated.Profiles {
		out.Items = append(out.Items, gen.Profile{Name: p.Name, Cpus: float32(p.CPUs), MemoryMb: p.MemoryMB})
	}
	writeJSON(w, http.StatusOK, out)
}

// RestoreProjectPITR implements POST /api/v1/projects/{id}/pitr.
func (s *Server) RestoreProjectPITR(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireBackups(w) || s.dedicatedSvc(w) == nil {
		return
	}
	var req gen.PitrRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	if req.TargetTime != nil {
		a.set("target_time", req.TargetTime.UTC())
	}
	if s.tenancy != nil {
		org := accessFrom(r.Context()).OrgID
		if !s.checkQuota(w, s.tenancy.CheckCreateProject(r.Context(), org)) || !s.checkQuota(w, s.tenancy.CheckOperation(r.Context(), org)) {
			return
		}
		// The recovery runs on a new instance the size of the source's.
		if src, err := s.tenantProjectLive(r.Context()); err == nil {
			if inst, err := store.New(s.db).GetInstance(r.Context(), src.InstanceID); err == nil && !s.withinAllowance(w, r, org, instanceSize(inst)) {
				return
			}
		}
	}
	c, err := s.backups.PITR(r.Context(), backup.PITRParams{ProjectID: id, TargetTime: req.TargetTime, Name: req.Name, CreatedBy: userID(r.Context()), CreatorRole: creatorRole(accessFrom(r.Context()))})
	if err != nil {
		s.backupError(w, "point-in-time recovery", err)
		return
	}
	a.set("new_project", c.Project.ID.String())
	s.writeCredentials(w, c.Project, c.Operation, c.Password)
}

// ProjectInstanceAction implements POST /api/v1/projects/{id}/instance.
func (s *Server) ProjectInstanceAction(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil || !s.requireProjects(w) {
		return
	}
	var req gen.InstanceActionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	a.set("action", string(req.Action))
	p, err := s.tenantProjectLive(r.Context())
	if err != nil {
		s.provisionError(w, "instance action", err)
		return
	}
	res, err := ds.Act(r.Context(), p, string(req.Action))
	if err != nil {
		s.provisionError(w, "instance action", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.InstanceState{State: res.State, Running: res.Running, Container: &res.Container})
}

// CreateNode implements POST /api/v1/nodes.
func (s *Server) CreateNode(w http.ResponseWriter, r *http.Request) {
	if !s.requireBackups(w) {
		return
	}
	var req gen.CreateNodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.set("name", req.Name)
	a.set("role", string(req.Role))
	n, tok, exp, err := s.nodes.CreateNode(r.Context(), req.Name, req.PrivateAddr, string(req.Role))
	if err != nil {
		s.backupError(w, "create node", err)
		return
	}
	a.target("node", n.ID.String())
	writeJSON(w, http.StatusCreated, gen.NodeCreated{
		Node: s.toAPINode(n), Token: tok, ExpiresAt: exp, Command: registerCommand(r, tok, req.PrivateAddr),
	})
}

func registerCommand(r *http.Request, token, addr string) string {
	if addr == "" {
		addr = "<this node's private address>"
	}
	return fmt.Sprintf("pgdock-agent register --server https://%s --token %s --advertise %s:7070", r.Host, token, addr)
}

// GetNode implements GET /api/v1/nodes/{id}.
func (s *Server) GetNode(w http.ResponseWriter, r *http.Request, id gen.NodeID) {
	if !s.requireBackups(w) {
		return
	}
	q := store.New(s.db)
	n, err := q.GetNode(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		s.internalError(w, "get node", err)
		return
	}
	rows, err := q.ListNodeInstances(r.Context(), id)
	if err != nil {
		s.internalError(w, "get node", err)
		return
	}
	out := gen.NodeDetail{Node: s.toAPINode(n), Instances: []gen.NodeInstance{}}
	for _, i := range rows {
		ni := gen.NodeInstance{
			Id: i.ID, Kind: i.Kind, Status: i.Status, Error: i.Error, Profile: i.Profile,
			MemoryMb: i32(i.MemLimitMb), VolumeGb: i32(i.VolumeGb), Projects: int(i.Projects), CreatedAt: &i.CreatedAt,
		}
		if f, err := i.CpuLimit.Float64Value(); err == nil && f.Valid {
			v := float32(f.Float64)
			ni.Cpus = &v
		}
		host := n.PrivateAddr
		if i.Host != nil {
			host = *i.Host
		}
		addr := fmt.Sprintf("%s:%d", host, i.Port)
		ni.Address = &addr
		out.Instances = append(out.Instances, ni)
	}
	writeJSON(w, http.StatusOK, out)
}

// RemoveNode implements DELETE /api/v1/nodes/{id}.
func (s *Server) RemoveNode(w http.ResponseWriter, r *http.Request, id gen.NodeID) {
	if !s.requireBackups(w) {
		return
	}
	auditFrom(r.Context()).target("node", id.String())
	if err := s.nodes.RemoveNode(r.Context(), id); err != nil {
		s.backupError(w, "remove node", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateSharedCluster implements POST /api/v1/nodes/{id}/shared-cluster.
func (s *Server) CreateSharedCluster(w http.ResponseWriter, r *http.Request, id gen.NodeID) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return
	}
	var req gen.SharedClusterRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("node", id.String())
	a.set("memory_mb", req.MemoryMb)
	op, err := ds.AddSharedCluster(r.Context(), id, req.MemoryMb, userID(r.Context()))
	if err != nil {
		s.provisionError(w, "create shared cluster", err)
		return
	}
	s.writeOperation(w, "create shared cluster", op)
}

// UpdateNode implements PATCH /api/v1/nodes/{id}.
func (s *Server) UpdateNode(w http.ResponseWriter, r *http.Request, id gen.NodeID) {
	if !s.requireBackups(w) {
		return
	}
	var req gen.UpdateNodeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("node", id.String())
	a.set("role", string(req.Role))
	n, err := s.nodes.SetRole(r.Context(), id, string(req.Role))
	if err != nil {
		s.backupError(w, "update node", err)
		return
	}
	writeJSON(w, http.StatusOK, s.toAPINode(n))
}

// GetPromotionEstimate implements GET /api/v1/projects/{id}/promote.
func (s *Server) GetPromotionEstimate(w http.ResponseWriter, r *http.Request, _ gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil || !s.requireProjects(w) {
		return
	}
	p, err := s.tenantProjectLive(r.Context())
	if err != nil {
		s.provisionError(w, "promotion estimate", err)
		return
	}
	if p.Tier != provision.TierShared {
		writeError(w, http.StatusBadRequest, "bad_request", "only shared projects can be promoted")
		return
	}
	est, err := ds.EstimatePromotion(r.Context(), p)
	if err != nil {
		s.internalError(w, "promotion estimate", err)
		return
	}
	writeJSON(w, http.StatusOK, gen.PromotionEstimate{SizeBytes: est.SizeBytes, EstimatedDowntimeSeconds: int(est.Downtime.Seconds())})
}

// PromoteProject implements POST /api/v1/projects/{id}/promote.
func (s *Server) PromoteProject(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	ds := s.dedicatedSvc(w)
	if ds == nil || !s.requireProjects(w) {
		return
	}
	var req gen.PromoteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	pp := dedicated.PromoteParams{ProjectID: id, NodeID: req.NodeId, CreatedBy: userID(r.Context())}
	if req.Profile != nil {
		pp.Profile = *req.Profile
		a.set("profile", pp.Profile)
	}
	if req.VolumeGb != nil {
		pp.VolumeGB = *req.VolumeGb
	}
	if s.tenancy != nil {
		org := accessFrom(r.Context()).OrgID
		ok, err := s.tenancy.WithinAllowance(r.Context(), org, profileSize(pp.Profile, pp.VolumeGB))
		if err != nil {
			s.internalError(w, "promote", err)
			return
		}
		if !ok {
			// Beyond the allowance, the promotion becomes a request (V2 §10.6).
			s.requestDedicated(w, r, org, id, pp, req.Reason)
			return
		}
	}
	op, err := ds.Promote(r.Context(), pp)
	if err != nil {
		s.provisionError(w, "promote", err)
		return
	}
	s.writeOperation(w, "promote", op)
}
