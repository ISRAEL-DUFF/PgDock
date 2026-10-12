package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/faildomain"
	"github.com/israel-duff/pgdock/internal/store"
)

// GetAdminRegionOverview implements GET /api/v1/admin/regions/{region_id}/overview
// (V4.1 §8.1): the pooler pair and its failure domains, the etcd cluster,
// and the HA projects whose state is in another region's etcd.
func (s *Server) GetAdminRegionOverview(w http.ResponseWriter, r *http.Request, region string) {
	ctx := r.Context()
	q := store.New(s.db)
	if _, err := q.GetRegion(ctx, region); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "no such region")
		return
	} else if err != nil {
		s.internalError(w, "region", err)
		return
	}
	out := gen.RegionOverview{Region: region, PoolerHosts: []gen.RegionPoolerHost{}, EtcdMembers: []gen.RegionEtcdMember{}, HaElsewhere: []gen.RegionHAElsewhere{}}
	hosts, err := q.RegionPoolerHosts(ctx, region)
	if err != nil {
		s.internalError(w, "region pooler hosts", err)
		return
	}
	for _, h := range hosts {
		out.PoolerHosts = append(out.PoolerHosts, gen.RegionPoolerHost{Id: h.ID, Name: h.Name, Status: h.Status, FailureDomain: h.FailureDomain})
	}
	probs, err := faildomain.Check(ctx, q)
	if err != nil {
		s.internalError(w, "failure domains", err)
		return
	}
	for _, p := range probs {
		if p.Group == faildomain.GroupPoolerPair && p.Region == region {
			d := p.Detail
			out.PoolerPairProblem = &d
		}
	}
	members, err := q.ListRegionEtcdMembers(ctx, region)
	if err != nil {
		s.internalError(w, "etcd members", err)
		return
	}
	for _, m := range members {
		x := gen.RegionEtcdMember{NodeId: m.NodeID, NodeName: m.NodeName, Status: m.Status}
		if n, err := q.GetNode(ctx, m.NodeID); err == nil {
			x.FailureDomain = n.FailureDomain
		}
		out.EtcdMembers = append(out.EtcdMembers, x)
	}
	if ds := s.backups; ds != nil && ds.Dedicated != nil && ds.Dedicated.Etcd != nil {
		if err := ds.Dedicated.Etcd.Ready(ctx, region); err != nil {
			msg := err.Error()
			out.EtcdProblem = &msg
		} else {
			out.EtcdReady = true
		}
		list, err := ds.Dedicated.HAOnOtherEtcd(ctx, region)
		if err != nil {
			s.internalError(w, "HA projects on another etcd", err)
			return
		}
		for _, p := range list {
			out.HaElsewhere = append(out.HaElsewhere, gen.RegionHAElsewhere{ProjectId: p.ID, Name: p.Name, EtcdRegion: p.EtcdRegion})
		}
	} else {
		msg := "HA is not available on this server"
		out.EtcdProblem = &msg
	}
	if op, err := q.LatestRegionOperation(ctx, store.LatestRegionOperationParams{Kind: dedicated.KindRegionEtcdMoveAll, Region: region}); err == nil {
		if o, err := toAPIOperation(op); err == nil {
			out.MoveAll = &o
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// MoveAllToRegionEtcd implements POST /api/v1/admin/regions/{region_id}/etcd-move-all.
func (s *Server) MoveAllToRegionEtcd(w http.ResponseWriter, r *http.Request, region string) {
	ds := s.dedicatedSvc(w)
	if ds == nil {
		return
	}
	auditFrom(r.Context()).target("region", region)
	op, err := ds.MoveAllToRegionEtcd(r.Context(), region, userID(r.Context()))
	if err != nil {
		s.provisionError(w, "move the region's projects to its etcd", err)
		return
	}
	s.writeOperation(w, "move the region's projects to its etcd", op)
}
