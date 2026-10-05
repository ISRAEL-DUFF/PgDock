package api

import (
	"net/http"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/ha"
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
