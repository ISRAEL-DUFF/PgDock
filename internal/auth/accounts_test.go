package auth_test

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"

	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/mail"
)

// inbox captures account email.
type inbox struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (b *inbox) Send(_ context.Context, m mail.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, m)
	return nil
}

var tokenRe = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

// lastToken returns the token in the latest email to addr.
func (b *inbox) lastToken(t *testing.T, addr string) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.msgs) - 1; i >= 0; i-- {
		if b.msgs[i].To[0] == addr {
			if m := tokenRe.FindStringSubmatch(b.msgs[i].Body); m != nil {
				return m[1]
			}
		}
	}
	t.Fatalf("no email with a link to %s", addr)
	return ""
}

func (b *inbox) count(addr string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, m := range b.msgs {
		if m.To[0] == addr {
			n++
		}
	}
	return n
}

func newAccounts(t *testing.T) (*auth.Service, *clock, *inbox, int, string) {
	t.Helper()
	s, c := newService(t, auth.Config{PublicURL: "https://pgdock.test"})
	box := &inbox{}
	s.SetMailer(box)
	ctx := context.Background()
	if err := s.EnsureTerms(ctx); err != nil {
		t.Fatal(err)
	}
	terms, err := s.CurrentTerms(ctx)
	if err != nil || terms.Version != 1 {
		t.Fatalf("default terms: %+v %v", terms, err)
	}
	secret := setupOwner(t, s, c)
	return s, c, box, int(terms.Version), secret
}

// enrol completes a first sign-in, enrolling TOTP, and returns the secret
// and recovery codes.
func enrol(t *testing.T, s *auth.Service, c *clock, addr string) (string, []string) {
	t.Helper()
	ctx := context.Background()
	ch, err := s.Login(ctx, addr, password)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if ch.Enroll == nil {
		t.Fatal("a first sign-in should enrol TOTP")
	}
	in, err := s.CompleteLogin(ctx, ch.Token, code(t, ch.Enroll.Secret, c), nil, "")
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if len(in.RecoveryCodes) != auth.RecoveryCodeCount || in.Session.PlatformRole != auth.RoleUser {
		t.Fatalf("enrolment: %+v", in)
	}
	c.Step()
	return ch.Enroll.Secret, in.RecoveryCodes
}

func TestSignupModes(t *testing.T) {
	s, c, box, terms, _ := newAccounts(t)
	ctx := context.Background()
	const alice = "alice@example.com"

	// Invite-only by default.
	if err := s.Signup(ctx, auth.SignupParams{Email: alice, Password: password, TermsVersion: terms}); !errors.Is(err, auth.ErrSignupClosed) {
		t.Fatalf("invite-only signup: %v", err)
	}

	// Open, with a domain allow-list.
	if _, err := s.SetSignupPolicy(ctx, auth.SignupPolicy{Mode: auth.SignupOpen, Domains: []string{"Example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Signup(ctx, auth.SignupParams{Email: "eve@elsewhere.net", Password: password, TermsVersion: terms}); !errors.Is(err, auth.ErrDomainNotAllowed) {
		t.Fatalf("other domain: %v", err)
	}
	if err := s.Signup(ctx, auth.SignupParams{Email: alice, Password: password, TermsVersion: terms + 1}); !errors.Is(err, auth.ErrTermsNotAccepted) {
		t.Fatalf("stale terms version: %v", err)
	}
	if err := s.Signup(ctx, auth.SignupParams{Email: alice, Password: password, Name: "Alice", TermsVersion: terms}); err != nil {
		t.Fatal(err)
	}
	// Nothing works before the address is verified.
	if _, err := s.Login(ctx, alice, password); !errors.Is(err, auth.ErrEmailUnverified) {
		t.Fatalf("unverified login: %v", err)
	}
	// Signing up again with the address says nothing; it sends a notice.
	before := box.count(alice)
	if err := s.Signup(ctx, auth.SignupParams{Email: alice, Password: password, TermsVersion: terms}); err != nil {
		t.Fatal(err)
	}
	if box.count(alice) != before+1 {
		t.Fatal("a repeated signup should email the address")
	}
	if _, err := s.VerifyEmail(ctx, "bogus"); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("bogus token: %v", err)
	}
	if err := s.ResendVerification(ctx, alice); err != nil {
		t.Fatal(err)
	}
	u, err := s.VerifyEmail(ctx, box.lastToken(t, alice))
	if err != nil || u.EmailVerifiedAt == nil {
		t.Fatalf("verify: %+v %v", u, err)
	}
	enrol(t, s, c, alice)

	// Approval mode: verified, but waiting for the platform admin.
	if _, err := s.SetSignupPolicy(ctx, auth.SignupPolicy{Mode: auth.SignupApproval}); err != nil {
		t.Fatal(err)
	}
	const bob = "bob@example.org"
	if err := s.Signup(ctx, auth.SignupParams{Email: bob, Password: password, TermsVersion: terms}); err != nil {
		t.Fatal(err)
	}
	ub, err := s.VerifyEmail(ctx, box.lastToken(t, bob))
	if err != nil || ub.ApprovedAt != nil {
		t.Fatalf("verify bob: %+v %v", ub, err)
	}
	if _, err := s.Login(ctx, bob, password); !errors.Is(err, auth.ErrPendingApproval) {
		t.Fatalf("unapproved login: %v", err)
	}
	if _, err := s.ApproveUser(ctx, ub.ID); err != nil {
		t.Fatal(err)
	}
	enrol(t, s, c, bob)
}

func TestRecoveryCodes(t *testing.T) {
	s, c, box, terms, _ := newAccounts(t)
	ctx := context.Background()
	if _, err := s.SetSignupPolicy(ctx, auth.SignupPolicy{Mode: auth.SignupOpen}); err != nil {
		t.Fatal(err)
	}
	const carol = "carol@example.com"
	if err := s.Signup(ctx, auth.SignupParams{Email: carol, Password: password, TermsVersion: terms}); err != nil {
		t.Fatal(err)
	}
	u, err := s.VerifyEmail(ctx, box.lastToken(t, carol))
	if err != nil {
		t.Fatal(err)
	}
	_, codes := enrol(t, s, c, carol)

	ch, err := s.Login(ctx, carol, password)
	if err != nil || ch.Enroll != nil {
		t.Fatalf("second login: %+v %v", ch, err)
	}
	if _, err := s.CompleteLogin(ctx, ch.Token, codes[3], nil, ""); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	if n, _ := s.RecoveryCodesLeft(ctx, u.ID); n != auth.RecoveryCodeCount-1 {
		t.Fatalf("codes left: %d", n)
	}
	// A recovery code works once.
	ch, _ = s.Login(ctx, carol, password)
	if _, err := s.CompleteLogin(ctx, ch.Token, codes[3], nil, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("reused recovery code: %v", err)
	}
	fresh, err := s.RegenerateRecoveryCodes(ctx, u.ID)
	if err != nil || len(fresh) != auth.RecoveryCodeCount {
		t.Fatal(err)
	}
	ch, _ = s.Login(ctx, carol, password)
	if _, err := s.CompleteLogin(ctx, ch.Token, codes[0], nil, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old code after regenerating: %v", err)
	}

	// The platform admin resets 2FA: the next sign-in enrols again.
	if err := s.ResetTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	c.Step()
	enrol(t, s, c, carol)
}

func TestPasswordReset(t *testing.T) {
	s, c, box, _, secret := newAccounts(t)
	ctx := context.Background()
	tok, _ := login(t, s, c, secret)

	if err := s.RequestPasswordReset(ctx, "nobody@example.com"); err != nil {
		t.Fatalf("unknown address: %v", err)
	}
	if err := s.RequestPasswordReset(ctx, email); err != nil {
		t.Fatal(err)
	}
	link := box.lastToken(t, email)
	const next = "an entirely new passphrase"
	if err := s.ResetPassword(ctx, link, "short"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}
	if err := s.ResetPassword(ctx, link, next); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, tok); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("a reset should end every session: %v", err)
	}
	if err := s.ResetPassword(ctx, link, next); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("reused link: %v", err)
	}
	if _, err := s.Login(ctx, email, password); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old password: %v", err)
	}
	if _, err := s.Login(ctx, email, next); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestTermsVersions(t *testing.T) {
	s, c, _, _, secret := newAccounts(t)
	ctx := context.Background()
	_, sess := login(t, s, c, secret)
	if pending, _, err := s.TermsOutstanding(ctx, sess.UserID); err != nil || pending {
		t.Fatalf("setup accepted version 1: %v %v", pending, err)
	}
	v2, err := s.PublishTerms(ctx, "# Terms v2", "# Privacy v2", sess.UserID)
	if err != nil || v2.Version != 2 {
		t.Fatalf("publish: %+v %v", v2, err)
	}
	pending, v, err := s.TermsOutstanding(ctx, sess.UserID)
	if err != nil || !pending || v != 2 {
		t.Fatalf("after publishing: %v %d %v", pending, v, err)
	}
	if err := s.AcceptTerms(ctx, sess.UserID, 1, nil); !errors.Is(err, auth.ErrTermsNotAccepted) {
		t.Fatalf("accepting an old version: %v", err)
	}
	if err := s.AcceptTerms(ctx, sess.UserID, 2, nil); err != nil {
		t.Fatal(err)
	}
	if pending, _, _ := s.TermsOutstanding(ctx, sess.UserID); pending {
		t.Fatal("still pending after accepting")
	}
}
