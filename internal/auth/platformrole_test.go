package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/auth"
)

// signUp creates a verified, enrolled ordinary account.
func signUp(t *testing.T, s *auth.Service, box *inbox, terms int, addr string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.SetSignupPolicy(ctx, auth.SignupPolicy{Mode: auth.SignupOpen}); err != nil {
		t.Fatal(err)
	}
	if err := s.Signup(ctx, auth.SignupParams{Email: addr, Password: password, TermsVersion: terms}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyEmail(ctx, box.lastToken(t, addr)); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformRoleChanges(t *testing.T) {
	s, c, box, terms, _ := newAccounts(t)
	ctx := context.Background()
	owner, err := s.UserByEmail(ctx, email)
	if err != nil {
		t.Fatal(err)
	}

	// Verified but no authenticator yet: not eligible through the UI.
	const carol = "carol@example.com"
	signUp(t, s, box, terms, carol)
	uc, _ := s.UserByEmail(ctx, carol)
	var inel *auth.IneligibleError
	if _, err := s.SetPlatformRole(ctx, uc.ID, auth.RolePlatformAdmin, auth.RoleChange{}); !errors.As(err, &inel) || !strings.Contains(err.Error(), "two-factor") {
		t.Fatalf("no 2FA: %v", err)
	}
	enrol(t, s, c, carol)

	// Now eligible; they are emailed and their sessions end.
	before := box.count(carol)
	u, err := s.SetPlatformRole(ctx, uc.ID, auth.RolePlatformAdmin, auth.RoleChange{})
	if err != nil || u.PlatformRole != auth.RolePlatformAdmin {
		t.Fatalf("promote: %+v %v", u, err)
	}
	if box.count(carol) != before+1 {
		t.Fatal("the new admin should be emailed")
	}
	if admins, _ := s.PlatformAdmins(ctx); len(admins) != 2 {
		t.Fatalf("admins: %d", len(admins))
	}
	// Doing it again changes nothing and sends nothing.
	if _, err := s.SetPlatformRole(ctx, uc.ID, auth.RolePlatformAdmin, auth.RoleChange{}); err != nil || box.count(carol) != before+1 {
		t.Fatalf("repeat: %v", err)
	}

	// A disabled account can't be promoted either.
	const dave = "dave@example.com"
	signUp(t, s, box, terms, dave)
	ud, _ := s.UserByEmail(ctx, dave)
	enrol(t, s, c, dave)
	if _, err := s.SetDisabled(ctx, ud.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlatformRole(ctx, ud.ID, auth.RolePlatformAdmin, auth.RoleChange{}); !errors.As(err, &inel) || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled: %v", err)
	}

	// Demote one of two; then the last one can't be demoted.
	if _, err := s.SetPlatformRole(ctx, uc.ID, auth.RoleUser, auth.RoleChange{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPlatformRole(ctx, owner.ID, auth.RoleUser, auth.RoleChange{}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("last admin: %v", err)
	}
	// Even an operator on the server can't leave nobody (use promote first).
	if _, err := s.SetPlatformRole(ctx, owner.ID, auth.RoleUser, auth.RoleChange{Recovery: true}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("last admin by operator: %v", err)
	}
}

func TestOperatorRecovery(t *testing.T) {
	s, _, box, terms, secret := newAccounts(t)
	ctx := context.Background()
	const erin = "erin@example.com"
	signUp(t, s, box, terms, erin)
	ue, _ := s.UserByEmail(ctx, erin)

	// Promotion on the server needs no authenticator, and approves, verifies
	// and re-enables the account.
	if _, err := s.SetDisabled(ctx, ue.ID, true); err != nil {
		t.Fatal(err)
	}
	u, err := s.SetPlatformRole(ctx, ue.ID, auth.RolePlatformAdmin, auth.RoleChange{Recovery: true})
	if err != nil || u.PlatformRole != auth.RolePlatformAdmin {
		t.Fatalf("recovery promote: %+v %v", u, err)
	}
	if got, _ := s.UserByEmail(ctx, erin); got.DisabledAt != nil || got.ApprovedAt == nil || got.EmailVerifiedAt == nil {
		t.Fatalf("recovery should enable the account: %+v", got)
	}

	// A reset link works once and signs the account in with the new password.
	link, err := s.PasswordResetLink(ctx, erin)
	if err != nil || !strings.HasPrefix(link, "https://pgdock.test/reset-password?token=") {
		t.Fatalf("link: %q %v", link, err)
	}
	const next = "a brand new passphrase"
	if err := s.ResetPassword(ctx, tokenRe.FindStringSubmatch(link)[1], next); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, erin, next); err != nil {
		t.Fatalf("login after reset: %v", err)
	}

	// Resetting the first admin's 2FA makes the next sign-in enrol again.
	owner, _ := s.UserByEmail(ctx, email)
	if err := s.ResetTOTP(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	ch, err := s.Login(ctx, email, password)
	if err != nil || ch.Enroll == nil {
		t.Fatalf("after 2FA reset: %+v %v", ch, err)
	}
	_ = secret
}
