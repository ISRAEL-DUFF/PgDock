package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Platform administrators are made and unmade here: in the admin UI by an
// existing administrator, or on the server by `pgdock-server admin …` (for
// when there is nobody left who can).

// ErrLastAdmin refuses to take the platform's last active administrator
// away, which would leave nobody able to manage it.
var ErrLastAdmin = errors.New("this is the last platform admin; make someone else an admin first")

// IneligibleError says why an account can't become a platform admin.
type IneligibleError struct{ Reason string }

func (e *IneligibleError) Error() string { return e.Reason }

// RoleChange is how a platform role is changed.
type RoleChange struct {
	// Recovery skips the checks that need the user's own cooperation (an
	// authenticator), for an operator on the server: the account is also
	// approved and its address marked verified.
	Recovery bool
}

// SetPlatformRole makes target a platform admin (RolePlatformAdmin) or an
// ordinary user (RoleUser). It refuses to demote the last active admin and
// ends the target's sessions, so the change is immediate. The target is
// emailed.
func (s *Service) SetPlatformRole(ctx context.Context, target uuid.UUID, role string, opt RoleChange) (store.User, error) {
	if role != RolePlatformAdmin && role != RoleSupport && role != RoleUser {
		return store.User{}, fmt.Errorf("unknown platform role %q", role)
	}
	var out store.User
	var changed bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.LockPlatformRoles(ctx); err != nil {
			return err
		}
		u, err := q.GetUser(ctx, target)
		if err != nil {
			return err
		}
		out = u
		if u.PlatformRole == role {
			return nil
		}
		switch {
		case role == RolePlatformAdmin && opt.Recovery:
			if _, err := q.ApproveUser(ctx, u.ID); err != nil {
				return err
			}
			if err := q.MarkEmailVerified(ctx, u.ID); err != nil {
				return err
			}
			if u.DisabledAt != nil {
				if _, err := q.SetUserDisabled(ctx, store.SetUserDisabledParams{ID: u.ID, Disabled: false}); err != nil {
					return err
				}
			}
		case role != RoleUser:
			// Admins and support staff need a working second factor.
			if reason := ineligible(u); reason != "" {
				return &IneligibleError{Reason: reason}
			}
		}
		if u.PlatformRole == RolePlatformAdmin && role != RolePlatformAdmin {
			n, err := q.CountPlatformAdmins(ctx)
			if err != nil {
				return err
			}
			// n counts active admins, this one included when it is active.
			others := n
			if u.DisabledAt == nil {
				others--
			}
			if others < 1 {
				return ErrLastAdmin
			}
		}
		if out, err = q.SetUserPlatformRole(ctx, store.SetUserPlatformRoleParams{ID: u.ID, Role: role}); err != nil {
			return err
		}
		changed = true
		return q.DeleteUserSessions(ctx, store.DeleteUserSessionsParams{UserID: u.ID})
	})
	if err != nil {
		return out, err
	}
	if changed {
		var subject, body string
		switch role {
		case RolePlatformAdmin:
			subject, body = "You are now a PGDock platform admin", "You were made a platform admin. You can manage users, plans, nodes and the platform's settings. Sign in again to use it."
		case RoleSupport:
			subject, body = "You are now PGDock support staff", "You were given the support role. You can answer tickets in the support console and see organisations' plans, billing status and usage, but not their data. Sign in again to use it."
		default:
			subject, body = "Your PGDock platform role was removed", "Your platform access was removed. Your own organisations and projects are unchanged. Sign in again to continue."
		}
		if err := s.send(ctx, out.Email, subject, body+"\n\nIf this wasn't expected, contact a platform admin."); err != nil {
			s.log.Warn("platform role email", "user_id", out.ID, "err", err)
		}
	}
	return out, nil
}

// ineligible says why u can't be made an admin through the UI: an admin
// needs a working second factor, and must be a normal, active account.
func ineligible(u store.User) string {
	switch {
	case u.DisabledAt != nil:
		return "this account is disabled"
	case u.ApprovedAt == nil:
		return "this account hasn't been approved yet"
	case u.EmailVerifiedAt == nil:
		return "this account hasn't verified its email address"
	case len(u.TotpSecret) == 0:
		return "this account hasn't set up two-factor authentication yet; a platform admin needs it"
	}
	return ""
}

// PasswordResetLink returns a one-hour password reset link without emailing
// it (for an operator on the server when mail doesn't work). It is
// the same single-use link the "forgot password" email carries.
func (s *Service) PasswordResetLink(ctx context.Context, email string) (string, error) {
	u, err := store.New(s.db).GetUserByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		return "", err
	}
	token, err := s.emailToken(ctx, u.ID, "reset", ResetTTL)
	if err != nil {
		return "", err
	}
	return s.cfg.PublicURL + "/reset-password?token=" + token, nil
}

// UserByEmail looks an account up for the operator commands.
func (s *Service) UserByEmail(ctx context.Context, email string) (store.User, error) {
	return store.New(s.db).GetUserByEmail(ctx, strings.TrimSpace(email))
}

// PlatformAdmins lists the platform's administrators.
func (s *Service) PlatformAdmins(ctx context.Context) ([]store.User, error) {
	return store.New(s.db).ListPlatformAdmins(ctx)
}
