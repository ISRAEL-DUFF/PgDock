package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/orgs"
	"github.com/israel-duff/pgdock/internal/store"
)

func genAdminUser(u store.User, orgCount int) gen.AdminUser {
	return gen.AdminUser{
		Id: u.ID, Email: u.Email, Name: u.Name, PlatformRole: gen.AdminUserPlatformRole(u.PlatformRole),
		EmailVerified: u.EmailVerifiedAt != nil, Approved: u.ApprovedAt != nil, Disabled: u.DisabledAt != nil,
		TotpEnabled: len(u.TotpSecret) > 0, OrgCount: orgCount, LastActiveAt: u.LastActiveAt, CreatedAt: u.CreatedAt,
	}
}

// ListUsers implements GET /api/v1/admin/users.
func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request, p gen.ListUsersParams) {
	rows, err := store.New(s.db).ListUsers(r.Context(), store.ListUsersParams{
		Query: p.Q, PendingOnly: p.Pending != nil && *p.Pending, MaxRows: 1000,
	})
	if err != nil {
		s.internalError(w, "list users", err)
		return
	}
	out := gen.UserList{Items: make([]gen.AdminUser, 0, len(rows))}
	for _, row := range rows {
		u := store.User{ID: row.ID, Email: row.Email, Name: row.Name, PlatformRole: row.PlatformRole, EmailVerifiedAt: row.EmailVerifiedAt,
			ApprovedAt: row.ApprovedAt, DisabledAt: row.DisabledAt, TotpSecret: row.TotpSecret, LastActiveAt: row.LastActiveAt, CreatedAt: row.CreatedAt}
		out.Items = append(out.Items, genAdminUser(u, int(row.OrgCount)))
	}
	writeJSON(w, http.StatusOK, out)
}

// UpdateUser implements PATCH /api/v1/admin/users/{user}.
func (s *Server) UpdateUser(w http.ResponseWriter, r *http.Request, user gen.UserID) {
	var req gen.UpdateUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	a := auditFrom(r.Context())
	a.target("user", user.String())
	sess, _ := sessionFrom(r.Context())
	q := store.New(s.db)
	u, err := q.GetUser(r.Context(), user)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "no such user")
		return
	}
	if err != nil {
		s.internalError(w, "update user", err)
		return
	}
	if req.Approved != nil && *req.Approved {
		a.set("approved", true)
		if u, err = s.auth.ApproveUser(r.Context(), user); err != nil {
			s.internalError(w, "approve user", err)
			return
		}
	}
	if req.Disabled != nil {
		if *req.Disabled && user == sess.UserID {
			writeError(w, http.StatusBadRequest, "bad_request", "you can't disable your own account")
			return
		}
		a.set("disabled", *req.Disabled)
		if u, err = s.auth.SetDisabled(r.Context(), user, *req.Disabled); err != nil {
			s.internalError(w, "disable user", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, genAdminUser(u, 0))
}

// ResetUserTotp implements POST /api/v1/admin/users/{user}/reset-2fa.
func (s *Server) ResetUserTotp(w http.ResponseWriter, r *http.Request, user gen.UserID) {
	auditFrom(r.Context()).target("user", user.String())
	if err := s.auth.ResetTOTP(r.Context(), user); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "no such user")
			return
		}
		s.internalError(w, "reset 2fa", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListPlatformInvitations implements GET /api/v1/admin/invitations.
func (s *Server) ListPlatformInvitations(w http.ResponseWriter, r *http.Request) {
	rows, err := store.New(s.db).ListPlatformInvitations(r.Context())
	if err != nil {
		s.internalError(w, "list invitations", err)
		return
	}
	out := gen.InvitationList{Items: make([]gen.Invitation, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, genInvitation(store.Invitation{
			ID: row.ID, Email: row.Email, Kind: row.Kind, ProjectRoles: row.ProjectRoles, ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt,
		}, row.InviterEmail))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreatePlatformInvitation implements POST /api/v1/admin/invitations.
func (s *Server) CreatePlatformInvitation(w http.ResponseWriter, r *http.Request) {
	var req gen.EmailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sess, _ := sessionFrom(r.Context())
	a := auditFrom(r.Context())
	a.set("email", string(req.Email))
	in, err := s.orgs.Invite(r.Context(), orgs.InviteParams{Email: string(req.Email), InvitedBy: sess.UserID, InviterName: displayName(sess.Name, sess.Email)})
	if err != nil {
		s.orgError(w, "invite", err)
		return
	}
	a.target("invitation", in.Invitation.ID.String())
	writeJSON(w, http.StatusCreated, s.invitationCreated(in, sess.Email))
}

// RevokePlatformInvitation implements DELETE /api/v1/admin/invitations/{invitation_id}.
func (s *Server) RevokePlatformInvitation(w http.ResponseWriter, r *http.Request, id gen.InvitationID) {
	auditFrom(r.Context()).target("invitation", id.String())
	n, err := store.New(s.db).RevokePlatformInvitation(r.Context(), id)
	if err != nil {
		s.internalError(w, "revoke invitation", err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no pending invitation with that id")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetSignupSettings implements GET /api/v1/admin/settings/signup.
func (s *Server) GetSignupSettings(w http.ResponseWriter, r *http.Request) {
	p, err := s.auth.SignupPolicy(r.Context())
	if err != nil {
		s.internalError(w, "signup settings", err)
		return
	}
	writeJSON(w, http.StatusOK, genSignup(p))
}

func genSignup(p auth.SignupPolicy) gen.SignupSettings {
	d := p.Domains
	if d == nil {
		d = []string{}
	}
	return gen.SignupSettings{Mode: gen.SignupSettingsMode(p.Mode), Domains: &d}
}

// PutSignupSettings implements PUT /api/v1/admin/settings/signup.
func (s *Server) PutSignupSettings(w http.ResponseWriter, r *http.Request) {
	var req gen.SignupSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	p := auth.SignupPolicy{Mode: string(req.Mode)}
	if req.Domains != nil {
		p.Domains = *req.Domains
	}
	auditFrom(r.Context()).set("mode", p.Mode)
	p, err := s.auth.SetSignupPolicy(r.Context(), p)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, genSignup(p))
}

func genMail(m mail.Settings) gen.MailSettings {
	out := gen.MailSettings{Configured: m.Configured}
	if m.Configured {
		tls := gen.MailSettingsTls(m.TLS)
		out.Host, out.Port, out.Username, out.HasPassword, out.From, out.Tls = &m.Host, &m.Port, &m.Username, &m.HasPassword, &m.From, &tls
	}
	return out
}

// GetMailSettings implements GET /api/v1/admin/settings/mail.
func (s *Server) GetMailSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.mail.Settings(r.Context())
	if err != nil {
		s.internalError(w, "mail settings", err)
		return
	}
	writeJSON(w, http.StatusOK, genMail(m))
}

// PutMailSettings implements PUT /api/v1/admin/settings/mail.
func (s *Server) PutMailSettings(w http.ResponseWriter, r *http.Request) {
	var req gen.MailSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u := mail.Update{Host: req.Host, From: req.From, Password: req.Password}
	if req.Port != nil {
		u.Port = *req.Port
	}
	if req.Username != nil {
		u.Username = *req.Username
	}
	if req.Tls != nil {
		u.TLS = string(*req.Tls)
	}
	a := auditFrom(r.Context())
	a.set("host", req.Host)
	cfg, err := s.mail.Resolve(r.Context(), u)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := mail.Send(ctx, cfg, mail.Message{
		To: []string{string(req.TestTo)}, Subject: "PGDock email works",
		Body: "This is a test from PGDock's email settings. Account email, invitations, and alerts will arrive like this.",
	}); err != nil {
		writeError(w, http.StatusBadGateway, "smtp_test_failed", "the test email could not be sent: "+err.Error())
		return
	}
	if err := s.mail.Save(r.Context(), cfg); err != nil {
		s.internalError(w, "save mail settings", err)
		return
	}
	m, err := s.mail.Settings(r.Context())
	if err != nil {
		s.internalError(w, "mail settings", err)
		return
	}
	writeJSON(w, http.StatusOK, genMail(m))
}

// PublishTerms implements POST /api/v1/admin/settings/terms.
func (s *Server) PublishTerms(w http.ResponseWriter, r *http.Request) {
	var req gen.PublishTermsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	sess, _ := sessionFrom(r.Context())
	t, err := s.auth.PublishTerms(r.Context(), req.TermsMd, req.PrivacyMd, sess.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	auditFrom(r.Context()).set("version", t.Version)
	writeJSON(w, http.StatusCreated, gen.Terms{Version: int(t.Version), TermsMd: t.TermsMd, PrivacyMd: t.PrivacyMd, PublishedAt: t.PublishedAt})
}
