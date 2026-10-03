package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/backup"
	"github.com/israel-duff/pgdock/internal/store"
)

// ---- Storage targets (V2 §6) ---------------------------------------------------

func (s *Server) storageTargetError(w http.ResponseWriter, what string, err error) {
	var inUse *backup.InUseError
	switch {
	case errors.As(err, &inUse):
		writeError(w, http.StatusConflict, "target_in_use", err.Error())
	case errors.Is(err, backup.ErrTargetInUse):
		writeError(w, http.StatusConflict, "target_in_use", err.Error())
	case errors.Is(err, backup.ErrTargetNotFound), errors.Is(err, backup.ErrTargetGone):
		writeError(w, http.StatusNotFound, "not_found", "storage target not found")
	case errors.Is(err, backup.ErrNoKey):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	default:
		s.backupError(w, what, err)
	}
}

func targetKind(t store.StorageTarget) gen.StorageTargetKind {
	if t.OrgID != nil {
		return gen.StorageTargetKindOrg
	}
	return gen.StorageTargetKindPlatform
}

func (s *Server) toAPIStorageTarget(r *http.Request, t store.StorageTarget) (gen.StorageTarget, error) {
	u, err := s.backups.TargetUse(r.Context(), t)
	if err != nil {
		return gen.StorageTarget{}, err
	}
	updated := t.UpdatedAt
	return gen.StorageTarget{
		Id: t.ID, Name: t.Name, Kind: targetKind(t), Endpoint: t.Endpoint, Region: t.Region, Bucket: t.Bucket,
		Prefix: t.Prefix, PathStyle: t.PathStyle, IsDefault: t.IsDefault, CreatedAt: t.CreatedAt, UpdatedAt: &updated,
		Usage: gen.StorageTargetUsage{Projects: u.Projects, Backups: u.Backups, Bytes: u.Bytes},
	}, nil
}

func targetInput(req gen.StorageTargetRequest) backup.TargetInput {
	in := backup.TargetInput{Name: req.Name, Endpoint: req.Endpoint, Bucket: req.Bucket}
	if req.Region != nil {
		in.Region = *req.Region
	}
	if req.Prefix != nil {
		in.Prefix = *req.Prefix
	}
	if req.AccessKey != nil {
		in.AccessKey = *req.AccessKey
	}
	if req.SecretKey != nil {
		in.SecretKey = *req.SecretKey
	}
	if req.PathStyle != nil {
		in.PathStyle = *req.PathStyle
	}
	if req.IsDefault != nil {
		in.IsDefault = *req.IsDefault
	}
	return in
}

func (s *Server) listTargets(w http.ResponseWriter, r *http.Request, orgID *uuid.UUID) {
	if !s.requireBackups(w) {
		return
	}
	rows, err := s.backups.ListTargets(r.Context(), orgID)
	if err != nil {
		s.internalError(w, "list storage targets", err)
		return
	}
	out := gen.StorageTargetList{Items: []gen.StorageTarget{}}
	for _, t := range rows {
		gt, err := s.toAPIStorageTarget(r, t)
		if err != nil {
			s.internalError(w, "list storage targets", err)
			return
		}
		out.Items = append(out.Items, gt)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTarget(w http.ResponseWriter, r *http.Request, orgID *uuid.UUID, id uuid.UUID) {
	if !s.requireBackups(w) {
		return
	}
	t, err := s.backups.GetTarget(r.Context(), orgID, id)
	if err != nil {
		s.storageTargetError(w, "storage target", err)
		return
	}
	gt, err := s.toAPIStorageTarget(r, t)
	if err != nil {
		s.internalError(w, "storage target", err)
		return
	}
	writeJSON(w, http.StatusOK, gt)
}

// saveTarget creates (id nil) or updates a target; a failing live test is
// a 200 with saved false and nothing stored.
func (s *Server) saveTarget(w http.ResponseWriter, r *http.Request, orgID, id *uuid.UUID) {
	if !s.requireBackups(w) {
		return
	}
	var req gen.StorageTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.set("name", req.Name)
	a.set("endpoint", req.Endpoint)
	a.set("bucket", req.Bucket)
	if id != nil {
		a.target("storage_target", id.String())
	}
	t, steps, err := s.backups.SaveTarget(r.Context(), orgID, id, targetInput(req), userID(r.Context()))
	if errors.Is(err, backup.ErrTargetTest) {
		writeJSON(w, http.StatusOK, gen.StorageTargetSaveResult{Saved: false, Test: toAPIStorageTest(steps, false, false)})
		return
	}
	if err != nil {
		s.storageTargetError(w, "save storage target", err)
		return
	}
	a.target("storage_target", t.ID.String())
	gt, err := s.toAPIStorageTarget(r, t)
	if err != nil {
		s.internalError(w, "save storage target", err)
		return
	}
	status := http.StatusOK
	if id == nil {
		status = http.StatusCreated
	}
	writeJSON(w, status, gen.StorageTargetSaveResult{Saved: true, Test: toAPIStorageTest(steps, true, true), Target: &gt})
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request, orgID *uuid.UUID, id uuid.UUID, accept *bool) {
	if !s.requireBackups(w) {
		return
	}
	acceptLoss := accept != nil && *accept
	a := auditFrom(r.Context())
	a.target("storage_target", id.String())
	a.set("accept_unrestorable", acceptLoss)
	use, err := s.backups.DeleteTarget(r.Context(), orgID, id, acceptLoss)
	if err != nil {
		s.storageTargetError(w, "delete storage target", err)
		return
	}
	a.set("backups_made_unrestorable", use.Backups)
	w.WriteHeader(http.StatusNoContent)
}

// ListOrgStorageTargets implements GET /api/v1/orgs/{org}/storage-targets.
func (s *Server) ListOrgStorageTargets(w http.ResponseWriter, r *http.Request, _ gen.OrgID) {
	org := accessFrom(r.Context()).OrgID
	s.listTargets(w, r, &org)
}

// CreateOrgStorageTarget implements POST /api/v1/orgs/{org}/storage-targets.
func (s *Server) CreateOrgStorageTarget(w http.ResponseWriter, r *http.Request, _ gen.OrgID) {
	org := accessFrom(r.Context()).OrgID
	s.saveTarget(w, r, &org, nil)
}

// GetOrgStorageTarget implements GET /api/v1/orgs/{org}/storage-targets/{target_id}.
func (s *Server) GetOrgStorageTarget(w http.ResponseWriter, r *http.Request, _ gen.OrgID, id gen.TargetID) {
	org := accessFrom(r.Context()).OrgID
	s.getTarget(w, r, &org, id)
}

// UpdateOrgStorageTarget implements PATCH /api/v1/orgs/{org}/storage-targets/{target_id}.
func (s *Server) UpdateOrgStorageTarget(w http.ResponseWriter, r *http.Request, _ gen.OrgID, id gen.TargetID) {
	org := accessFrom(r.Context()).OrgID
	s.saveTarget(w, r, &org, &id)
}

// DeleteOrgStorageTarget implements DELETE /api/v1/orgs/{org}/storage-targets/{target_id}.
func (s *Server) DeleteOrgStorageTarget(w http.ResponseWriter, r *http.Request, _ gen.OrgID, id gen.TargetID, params gen.DeleteOrgStorageTargetParams) {
	org := accessFrom(r.Context()).OrgID
	s.deleteTarget(w, r, &org, id, params.AcceptUnrestorable)
}

// ListPlatformStorageTargets implements GET /api/v1/admin/storage-targets.
func (s *Server) ListPlatformStorageTargets(w http.ResponseWriter, r *http.Request) {
	s.listTargets(w, r, nil)
}

// CreatePlatformStorageTarget implements POST /api/v1/admin/storage-targets.
func (s *Server) CreatePlatformStorageTarget(w http.ResponseWriter, r *http.Request) {
	s.saveTarget(w, r, nil, nil)
}

// GetPlatformStorageTarget implements GET /api/v1/admin/storage-targets/{target_id}.
func (s *Server) GetPlatformStorageTarget(w http.ResponseWriter, r *http.Request, id gen.TargetID) {
	s.getTarget(w, r, nil, id)
}

// UpdatePlatformStorageTarget implements PATCH /api/v1/admin/storage-targets/{target_id}.
func (s *Server) UpdatePlatformStorageTarget(w http.ResponseWriter, r *http.Request, id gen.TargetID) {
	s.saveTarget(w, r, nil, &id)
}

// DeletePlatformStorageTarget implements DELETE /api/v1/admin/storage-targets/{target_id}.
func (s *Server) DeletePlatformStorageTarget(w http.ResponseWriter, r *http.Request, id gen.TargetID, params gen.DeletePlatformStorageTargetParams) {
	s.deleteTarget(w, r, nil, id, params.AcceptUnrestorable)
}

// TestStorageTarget implements POST /api/v1/storage-targets/test.
func (s *Server) TestStorageTarget(w http.ResponseWriter, r *http.Request, _ gen.TestStorageTargetParams) {
	if !s.requireBackups(w) {
		return
	}
	var req gen.StorageTargetTestRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := targetInput(gen.StorageTargetRequest{
		Name: "test", Endpoint: req.Endpoint, Region: req.Region, Bucket: req.Bucket, Prefix: req.Prefix,
		AccessKey: req.AccessKey, SecretKey: req.SecretKey, PathStyle: req.PathStyle,
	})
	t := storageTarget(gen.StorageRequest{Endpoint: in.Endpoint, Region: &in.Region, Bucket: in.Bucket, Prefix: &in.Prefix,
		AccessKey: in.AccessKey, SecretKey: &in.SecretKey, PathStyle: &in.PathStyle})
	if req.TargetId != nil && (t.AccessKey == "" || t.SecretKey == "") {
		// Fill in the stored credentials of a target the caller may see: the
		// organisation's own, or a platform target for the platform admin.
		org := accessFrom(r.Context()).OrgID
		row, err := s.backups.GetTarget(r.Context(), &org, *req.TargetId)
		if errors.Is(err, backup.ErrTargetNotFound) {
			if sess, ok := sessionFrom(r.Context()); ok && sess.PlatformAdmin() {
				row, err = s.backups.GetTarget(r.Context(), nil, *req.TargetId)
			}
		}
		if err != nil {
			s.storageTargetError(w, "test storage target", err)
			return
		}
		stored, err := s.backups.TargetByID(r.Context(), row.ID)
		if err != nil {
			s.storageTargetError(w, "test storage target", err)
			return
		}
		if t.AccessKey == "" {
			t.AccessKey = stored.AccessKey
		}
		if t.SecretKey == "" && t.AccessKey == stored.AccessKey {
			t.SecretKey = stored.SecretKey
		}
	}
	steps, passed, err := s.backups.TestStorage(r.Context(), t)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPIStorageTest(steps, passed, false))
}

// ---- A project's backup storage -------------------------------------------------

func toAPIProjectKey(k *store.BackupKey) gen.ProjectBackupKey {
	if k == nil {
		return gen.ProjectBackupKey{Enabled: false}
	}
	return gen.ProjectBackupKey{Enabled: true, Id: &k.ID, Fingerprint: &k.Fingerprint, CreatedAt: &k.CreatedAt}
}

// projectStorage describes where a project's backups go and what it could
// choose.
func (s *Server) projectStorage(r *http.Request, p store.Project) (gen.ProjectBackupStorage, error) {
	ctx := r.Context()
	out := gen.ProjectBackupStorage{Choices: []gen.ProjectStorageChoice{}}
	platform, err := s.backups.ListTargets(ctx, nil)
	if err != nil {
		return out, err
	}
	org, err := s.backups.ListTargets(ctx, &p.OrgID)
	if err != nil {
		return out, err
	}
	for _, t := range platform {
		if t.IsDefault {
			out.Choices = append([]gen.ProjectStorageChoice{{Id: nil, Kind: gen.StorageTargetKindPlatform, Name: t.Name + " (platform default)", IsDefault: true}}, out.Choices...)
			continue
		}
		id := t.ID
		out.Choices = append(out.Choices, gen.ProjectStorageChoice{Id: &id, Kind: gen.StorageTargetKindPlatform, Name: t.Name})
	}
	for _, t := range org {
		id := t.ID
		out.Choices = append(out.Choices, gen.ProjectStorageChoice{Id: &id, Kind: gen.StorageTargetKindOrg, Name: t.Name})
	}
	out.Target = gen.ProjectStorageChoice{Kind: gen.StorageTargetKindPlatform, Name: "platform default", IsDefault: true}
	if len(out.Choices) > 0 && out.Choices[0].IsDefault {
		out.Target = out.Choices[0]
	}
	if p.StorageTargetID != nil {
		for _, c := range out.Choices {
			if c.Id != nil && *c.Id == *p.StorageTargetID {
				out.Target = c
			}
		}
	}
	out.CountsTowardQuota = out.Target.Kind == gen.StorageTargetKindPlatform
	if p.BackupKeyID != nil {
		k, err := store.New(s.db).GetBackupKey(ctx, *p.BackupKeyID)
		if err != nil {
			return out, err
		}
		out.Key = toAPIProjectKey(&k)
	} else {
		out.Key = toAPIProjectKey(nil)
	}
	return out, nil
}

// GetProjectStorageTarget implements GET /api/v1/projects/{id}/storage-target.
func (s *Server) GetProjectStorageTarget(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireBackups(w) {
		return
	}
	p, err := s.projects.Get(r.Context(), id)
	if err != nil {
		s.provisionError(w, "project storage", err)
		return
	}
	out, err := s.projectStorage(r, p)
	if err != nil {
		s.internalError(w, "project storage", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// SetProjectStorageTarget implements PUT /api/v1/projects/{id}/storage-target.
func (s *Server) SetProjectStorageTarget(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireBackups(w) {
		return
	}
	var req gen.ProjectStorageTargetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	p, err := s.projects.Get(r.Context(), id)
	if err != nil {
		s.provisionError(w, "project storage", err)
		return
	}
	sp := backup.SwitchParams{TargetID: req.TargetId, By: userID(r.Context())}
	if req.CopyExisting != nil {
		sp.CopyExisting = *req.CopyExisting
	}
	if req.DeleteOriginals != nil {
		sp.DeleteOriginals = *req.DeleteOriginals
	}
	if req.TargetId != nil {
		a.set("storage_target", req.TargetId.String())
	} else {
		a.set("storage_target", "platform default")
	}
	a.set("copy_existing", sp.CopyExisting)
	a.set("delete_originals", sp.DeleteOriginals)
	if (sp.CopyExisting || p.Tier == "dedicated") && s.tenancy != nil && !s.checkQuota(w, s.tenancy.CheckOperation(r.Context(), p.OrgID)) {
		return
	}
	op, err := s.backups.SwitchTarget(r.Context(), p, sp)
	if err != nil {
		s.storageTargetError(w, "switch storage target", err)
		return
	}
	if p, err = s.projects.Get(r.Context(), id); err != nil {
		s.provisionError(w, "project storage", err)
		return
	}
	st, err := s.projectStorage(r, p)
	if err != nil {
		s.internalError(w, "project storage", err)
		return
	}
	out := gen.ProjectStorageTargetResult{Storage: st}
	if op != nil {
		o, err := toAPIOperation(*op)
		if err != nil {
			s.internalError(w, "switch storage target", err)
			return
		}
		out.Operation = &o
		a.set("operation", op.ID.String())
	}
	writeJSON(w, http.StatusOK, out)
}

// EnableProjectBackupKey implements POST /api/v1/projects/{id}/backup-key.
func (s *Server) EnableProjectBackupKey(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireBackups(w) {
		return
	}
	var req gen.ProjectBackupKeyRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	p, err := s.projects.Get(r.Context(), id)
	if err != nil {
		s.provisionError(w, "backup key", err)
		return
	}
	rotate := req.Rotate != nil && *req.Rotate
	k, changed, err := s.backups.EnableProjectKey(r.Context(), p, userID(r.Context()), rotate)
	if err != nil {
		s.storageTargetError(w, "backup key", err)
		return
	}
	a.set("fingerprint", k.Fingerprint)
	a.set("generated", changed)
	writeJSON(w, http.StatusOK, toAPIProjectKey(&k))
}

// DownloadProjectBackupKey implements GET /api/v1/projects/{id}/backup-key/download.
// The guard requires a recent re-authentication and audits it.
func (s *Server) DownloadProjectBackupKey(w http.ResponseWriter, r *http.Request, id gen.ProjectID) {
	if !s.requireBackups(w) {
		return
	}
	a := auditFrom(r.Context())
	a.target("project", id.String())
	p, err := s.projects.Get(r.Context(), id)
	if err != nil {
		s.provisionError(w, "backup key", err)
		return
	}
	file, k, err := s.backups.ProjectKeyFile(r.Context(), p)
	if err != nil {
		s.storageTargetError(w, "backup key", err)
		return
	}
	a.set("fingerprint", k.Fingerprint)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pgdock-backup-key-%s.asc"`, p.ID))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(file))
}
