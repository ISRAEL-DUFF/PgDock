package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/projauth"
	"github.com/israel-duff/pgdock/internal/store"
)

// The dashboard's view of a project's users (V4 §4.9), read and changed in
// the project's database as the platform's admin.

// withProjectAuth runs fn in a transaction on p's database, once its
// pgd_auth tables exist.
func (s *Service) withProjectAuth(ctx context.Context, projectID uuid.UUID, fn func(store.Project, pgx.Tx) error) error {
	q := store.New(s.db)
	svc, err := q.GetProjectServices(ctx, projectID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!svc.Enabled || svc.SchemaVersion < 2)) {
		return fmt.Errorf("%w: backend services are off (or still being set up)", ErrConflict)
	}
	if err != nil {
		return err
	}
	p, err := q.GetProject(ctx, projectID)
	if err != nil {
		return err
	}
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error { return fn(p, tx) })
}

// UserPage is a page of a project's users.
type UserPage struct {
	Users []*projauth.User
	Total int
	Stats projauth.Stats
}

// ListAuthUsers is a page of users matching search.
func (s *Service) ListAuthUsers(ctx context.Context, projectID uuid.UUID, search string, limit, offset int) (UserPage, error) {
	var out UserPage
	err := s.withProjectAuth(ctx, projectID, func(_ store.Project, tx pgx.Tx) error {
		var err error
		if out.Users, out.Total, err = projauth.ListUsers(ctx, tx, search, limit, offset); err != nil {
			return err
		}
		out.Stats, err = projauth.UserStats(ctx, tx)
		return err
	})
	return out, err
}

// UserDetail is one user with their identities, sessions and audit log.
type UserDetail struct {
	User       *projauth.User
	Identities []projauth.Identity
	Sessions   []*projauth.Session
	Audit      []projauth.AuditEntry
}

func authUserErr(err error) error {
	switch {
	case errors.Is(err, projauth.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, projauth.ErrExists):
		return fmt.Errorf("%w: %v", ErrConflict, err) //nolint:errorlint // one sentinel is enough
	}
	return err
}

// GetAuthUser is a user's detail.
func (s *Service) GetAuthUser(ctx context.Context, projectID, userID uuid.UUID) (UserDetail, error) {
	var d UserDetail
	err := s.withProjectAuth(ctx, projectID, func(_ store.Project, tx pgx.Tx) error {
		var err error
		if d.User, err = projauth.GetUser(ctx, tx, userID, false); err != nil {
			return err
		}
		if d.Identities, err = projauth.Identities(ctx, tx, userID); err != nil {
			return err
		}
		if d.Sessions, err = projauth.Sessions(ctx, tx, userID); err != nil {
			return err
		}
		d.Audit, err = projauth.AuditLog(ctx, tx, &userID, 50)
		return err
	})
	return d, authUserErr(err)
}

// AuthAuditLog is the project's latest auth events.
func (s *Service) AuthAuditLog(ctx context.Context, projectID uuid.UUID, limit int) ([]projauth.AuditEntry, error) {
	var out []projauth.AuditEntry
	err := s.withProjectAuth(ctx, projectID, func(_ store.Project, tx pgx.Tx) error {
		var err error
		out, err = projauth.AuditLog(ctx, tx, nil, limit)
		return err
	})
	return out, err
}

// NewAuthUser is a user the dashboard adds: invited (an email with a
// link), or made with a password.
type NewAuthUser struct {
	Email        string
	Password     string
	EmailConfirm bool
	Invite       bool
}

// CreateAuthUser adds a user.
func (s *Service) CreateAuthUser(ctx context.Context, projectID uuid.UUID, n NewAuthUser, by string) (*projauth.User, error) {
	email := projauth.NormalizeEmail(n.Email)
	if email == "" || len(email) > 254 {
		return nil, fmt.Errorf("%w: an email address is required", ErrInvalid)
	}
	cfg, err := s.GetAuthConfig(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var hash string
	if !n.Invite {
		if len(n.Password) < cfg.Resolved.PasswordMinLength || len(n.Password) > 256 {
			return nil, fmt.Errorf("%w: the password must be %d to 256 characters", ErrInvalid, cfg.Resolved.PasswordMinLength)
		}
		if hash, err = projauth.HashPassword(n.Password); err != nil {
			return nil, err
		}
	}
	var u *projauth.User
	var invite *edgeapi.AuthMessage
	err = s.withProjectAuth(ctx, projectID, func(p store.Project, tx pgx.Tx) error {
		var err error
		if u, err = projauth.CreateUser(ctx, tx, projauth.NewUser{Email: email, PasswordHash: hash,
			EmailConfirmed: n.EmailConfirm && !n.Invite, Invited: n.Invite}); err != nil {
			return err
		}
		if err := projauth.EnsureEmailIdentity(ctx, tx, u.ID, email); err != nil {
			return err
		}
		action := projauth.ActSignup
		if n.Invite {
			action = projauth.ActInvited
			code, err := projauth.NewCode(ctx, tx, u.ID, projauth.CodeInvite, email, projauth.InviteTTL)
			if err != nil {
				return err
			}
			svc, err := store.New(s.db).GetProjectServices(ctx, projectID)
			if err != nil {
				return err
			}
			q := url.Values{"token": {code.Token}, "type": {"invite"}}
			if cfg.Resolved.SiteURL != "" {
				q.Set("redirect_to", cfg.Resolved.SiteURL)
			}
			invite = &edgeapi.AuthMessage{Ref: svc.Ref, Channel: edgeapi.ChannelEmail, Kind: edgeapi.EmailInvite, To: email,
				Link: s.URL(svc.Ref, p.Region) + "/auth/v1/verify?" + q.Encode()}
		}
		return projauth.Audit(ctx, tx, &u.ID, action, "", map[string]any{"by": by})
	})
	if err != nil {
		return nil, authUserErr(err)
	}
	if invite != nil {
		if err := s.QueueAuthMessage(ctx, *invite); err != nil {
			return u, fmt.Errorf("the user was added, but the invitation couldn't be sent: %w", err)
		}
	}
	return u, nil
}

// AuthUserUpdate is what the dashboard changes: a ban ("none" lifts it),
// a confirmation, the metadata.
type AuthUserUpdate struct {
	BanDuration  *string
	EmailConfirm bool
	UserMetadata json.RawMessage
	AppMetadata  json.RawMessage
}

// UpdateAuthUser changes a user.
func (s *Service) UpdateAuthUser(ctx context.Context, projectID, userID uuid.UUID, up AuthUserUpdate, by string) (*projauth.User, error) {
	pu := projauth.Update{ConfirmEmail: up.EmailConfirm, UserMetadata: up.UserMetadata, AppMetadata: up.AppMetadata}
	action := projauth.ActUpdated
	if up.BanDuration != nil {
		t, err := projauth.ParseBan(*up.BanDuration)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		pu.Ban = &t
		action = projauth.ActBanned
		if t.IsZero() {
			action = projauth.ActUnbanned
		}
	}
	var u *projauth.User
	err := s.withProjectAuth(ctx, projectID, func(_ store.Project, tx pgx.Tx) error {
		var err error
		if u, err = projauth.UpdateUser(ctx, tx, userID, pu); err != nil {
			return err
		}
		return projauth.Audit(ctx, tx, &userID, action, "", map[string]any{"by": by})
	})
	return u, authUserErr(err)
}

// DeleteAuthUser removes a user.
func (s *Service) DeleteAuthUser(ctx context.Context, projectID, userID uuid.UUID, by string) error {
	return authUserErr(s.withProjectAuth(ctx, projectID, func(_ store.Project, tx pgx.Tx) error {
		if err := projauth.DeleteUser(ctx, tx, userID); err != nil {
			return err
		}
		return projauth.Audit(ctx, tx, &userID, projauth.ActDeleted, "", map[string]any{"by": by})
	}))
}

// SignOutAuthUser ends every session of a user.
func (s *Service) SignOutAuthUser(ctx context.Context, projectID, userID uuid.UUID, by string) (int64, error) {
	var n int64
	err := s.withProjectAuth(ctx, projectID, func(_ store.Project, tx pgx.Tx) error {
		if _, err := projauth.GetUser(ctx, tx, userID, false); err != nil {
			return err
		}
		var err error
		if n, err = projauth.EndSessions(ctx, tx, userID, nil); err != nil {
			return err
		}
		return projauth.Audit(ctx, tx, &userID, projauth.ActAdminSignOut, "", map[string]any{"by": by, "sessions": n})
	})
	return n, authUserErr(err)
}
