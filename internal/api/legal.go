package api

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/legal"
	"github.com/israel-duff/pgdock/internal/store"
)

func (s *Server) legalSvc(w http.ResponseWriter) *legal.Service {
	if s.legal == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "legal documents aren't set up on this server")
	}
	return s.legal
}

func (s *Server) legalError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, legal.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, legal.ErrStale):
		writeError(w, http.StatusConflict, "stale", err.Error())
	case errors.Is(err, legal.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "document not found")
	default:
		s.internalError(w, what, err)
	}
}

func toAPILegalDocument(d store.LegalDocument) gen.LegalDocument {
	return gen.LegalDocument{
		Id: d.ID, Kind: gen.LegalDocumentKind(d.Kind), OrgId: d.OrgID, Version: d.Version,
		Title: d.Title, BodyMd: d.BodyMd, PublishedAt: d.PublishedAt,
	}
}

func (s *Server) writeOrgLegal(w http.ResponseWriter, r *http.Request, ls *legal.Service, org uuid.UUID) {
	docs, err := ls.ForOrg(r.Context(), org)
	if err != nil {
		s.legalError(w, "legal documents", err)
		return
	}
	out := gen.OrgLegal{Items: make([]gen.OrgLegalDocument, 0, len(docs))}
	for _, d := range docs {
		od := gen.OrgLegalDocument{Document: toAPILegalDocument(d.Doc)}
		if d.AcceptedAt != nil {
			od.AcceptedAt, od.AcceptedBy = &d.AcceptedAt.AcceptedAt, d.AcceptedAt.AcceptedBy
		} else {
			out.Outstanding = true
		}
		out.Items = append(out.Items, od)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetLegal implements GET /api/v1/legal.
func (s *Server) GetLegal(w http.ResponseWriter, r *http.Request) {
	ls := s.legalSvc(w)
	if ls == nil {
		return
	}
	out := gen.LegalDocumentList{Items: []gen.LegalDocument{}}
	for _, k := range []string{legal.SLA, legal.DPA} {
		d, err := ls.Current(r.Context(), k)
		if errors.Is(err, legal.ErrNotFound) {
			continue
		}
		if err != nil {
			s.legalError(w, "legal documents", err)
			return
		}
		out.Items = append(out.Items, toAPILegalDocument(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetOrgLegal implements GET /api/v1/orgs/{org}/legal.
func (s *Server) GetOrgLegal(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	if ls := s.legalSvc(w); ls != nil {
		s.writeOrgLegal(w, r, ls, org)
	}
}

// AcceptOrgLegal implements POST /api/v1/orgs/{org}/legal/{document_id}/accept.
func (s *Server) AcceptOrgLegal(w http.ResponseWriter, r *http.Request, org gen.OrgID, id uuid.UUID) {
	ls := s.legalSvc(w)
	if ls == nil {
		return
	}
	if err := ls.Accept(r.Context(), org, id, userID(r.Context()), ipFrom(r.Context())); err != nil {
		s.legalError(w, "accept", err)
		return
	}
	auditFrom(r.Context()).set("document_id", id.String())
	s.writeOrgLegal(w, r, ls, org)
}

// AdminListLegal implements GET /api/v1/admin/legal.
func (s *Server) AdminListLegal(w http.ResponseWriter, r *http.Request) {
	rows, err := store.New(s.db).ListLegalVersions(r.Context())
	if err != nil {
		s.internalError(w, "legal versions", err)
		return
	}
	out := gen.LegalVersionList{Items: make([]gen.LegalVersion, 0, len(rows))}
	for _, v := range rows {
		out.Items = append(out.Items, gen.LegalVersion{Id: v.ID, Kind: v.Kind, Version: v.Version, Title: v.Title, PublishedAt: v.PublishedAt, Acceptances: v.Acceptances})
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminPublishLegal implements POST /api/v1/admin/legal.
func (s *Server) AdminPublishLegal(w http.ResponseWriter, r *http.Request) {
	ls := s.legalSvc(w)
	if ls == nil {
		return
	}
	var req gen.LegalPublish
	if !decodeJSON(w, r, &req) {
		return
	}
	d, err := ls.Publish(r.Context(), string(req.Kind), nil, req.Title, req.BodyMd, userID(r.Context()))
	if err != nil {
		s.legalError(w, "publish", err)
		return
	}
	au := auditFrom(r.Context())
	au.set("kind", d.Kind)
	au.set("version", d.Version)
	writeJSON(w, http.StatusCreated, toAPILegalDocument(d))
}

// AdminGetLegal implements GET /api/v1/admin/legal/{document_id}.
func (s *Server) AdminGetLegal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	q := store.New(s.db)
	d, err := q.GetLegalDocument(r.Context(), id)
	if err != nil {
		s.legalError(w, "legal document", err)
		return
	}
	rows, err := q.LegalAcceptances(r.Context(), id)
	if err != nil {
		s.internalError(w, "acceptances", err)
		return
	}
	out := gen.LegalDocumentDetail{Document: toAPILegalDocument(d), Acceptances: make([]gen.LegalAcceptance, 0, len(rows))}
	for _, a := range rows {
		out.Acceptances = append(out.Acceptances, gen.LegalAcceptance{OrgId: a.OrgID, OrgName: a.OrgName, AcceptedAt: a.AcceptedAt, AcceptedBy: a.AcceptedBy})
	}
	writeJSON(w, http.StatusOK, out)
}

// AdminPublishOrderForm implements POST /api/v1/admin/orgs/{org}/order-form.
func (s *Server) AdminPublishOrderForm(w http.ResponseWriter, r *http.Request, org gen.OrgID) {
	ls := s.legalSvc(w)
	if ls == nil {
		return
	}
	var req gen.OrderFormPublish
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := store.New(s.db).GetOrg(r.Context(), org); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "organisation not found")
		return
	}
	d, err := ls.Publish(r.Context(), legal.OrderForm, &org, req.Title, req.BodyMd, userID(r.Context()))
	if err != nil {
		s.legalError(w, "order form", err)
		return
	}
	auditFrom(r.Context()).set("version", d.Version)
	writeJSON(w, http.StatusCreated, toAPILegalDocument(d))
}
