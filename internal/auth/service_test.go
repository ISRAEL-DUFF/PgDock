package auth_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/auth"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

// clock advances 30s per TOTP use, so consecutive logins get fresh steps.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Step()          { c.mu.Lock(); c.t = c.t.Add(30 * time.Second); c.mu.Unlock() }

func newService(t *testing.T, cfg auth.Config) (*auth.Service, *clock) {
	t.Helper()
	pool := storetest.New(t)
	key, _ := crypto.GenerateKey()
	kr, _ := crypto.NewKeyring(key)
	c := &clock{t: time.Now()}
	cfg.Now = c.Now
	return auth.NewService(pool, kr, cfg, "setup-code", slog.New(slog.NewTextHandler(io.Discard, nil))), c
}

const (
	email    = "owner@example.com"
	password = "correct horse battery staple"
)

func code(t *testing.T, secret string, c *clock) string {
	t.Helper()
	s, err := auth.TOTPCode(secret, c.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// setupOwner runs the first-run wizard and returns the TOTP secret.
func setupOwner(t *testing.T, s *auth.Service, c *clock) string {
	t.Helper()
	ctx := context.Background()
	enr, err := s.BeginSetup(ctx, "setup-code", email, password)
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.CompleteSetup(ctx, enr.Token, code(t, enr.Secret, c), nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	tok, sess := in.Token, in.Session
	if tok == "" || sess.Email != email || sess.PlatformRole != auth.RolePlatformAdmin || len(in.RecoveryCodes) != auth.RecoveryCodeCount {
		t.Fatalf("setup session: %q %+v", tok, sess)
	}
	c.Step()
	return enr.Secret
}

func login(t *testing.T, s *auth.Service, c *clock, secret string) (string, auth.Session) {
	t.Helper()
	ctx := context.Background()
	ch, err := s.Login(ctx, email, password)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	in, err := s.CompleteLogin(ctx, ch.Token, code(t, secret, c), nil, "test")
	if err != nil {
		t.Fatalf("totp: %v", err)
	}
	c.Step()
	return in.Token, in.Session
}

func TestSetupFlow(t *testing.T) {
	s, c := newService(t, auth.Config{})
	ctx := context.Background()

	if needed, _ := s.SetupNeeded(ctx); !needed {
		t.Fatal("fresh install should need setup")
	}
	if _, err := s.BeginSetup(ctx, "wrong", email, password); !errors.Is(err, auth.ErrBadSetupCode) {
		t.Fatalf("wrong setup code: %v", err)
	}
	if _, err := s.BeginSetup(ctx, "setup-code", "not-an-email", password); err == nil {
		t.Fatal("bad email accepted")
	}
	if _, err := s.BeginSetup(ctx, "setup-code", email, "short"); !errors.Is(err, auth.ErrWeakPassword) {
		t.Fatalf("weak password: %v", err)
	}

	enr, err := s.BeginSetup(ctx, "setup-code", email, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteSetup(ctx, enr.Token, "000000", nil, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong enrolment code: %v", err)
	}
	if needed, _ := s.SetupNeeded(ctx); !needed {
		t.Fatal("account created before TOTP was proven")
	}
	if _, err := s.CompleteSetup(ctx, enr.Token, code(t, enr.Secret, c), nil, ""); err != nil {
		t.Fatal(err)
	}
	if needed, _ := s.SetupNeeded(ctx); needed {
		t.Fatal("setup still needed")
	}
	if _, err := s.BeginSetup(ctx, "setup-code", "x@example.com", password); !errors.Is(err, auth.ErrSetupDone) {
		t.Fatalf("second setup: %v", err)
	}
}

func TestLoginSessionLogout(t *testing.T) {
	s, c := newService(t, auth.Config{})
	ctx := context.Background()
	secret := setupOwner(t, s, c)

	if _, err := s.Login(ctx, email, "wrong password!!"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := s.Login(ctx, "nobody@example.com", password); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("unknown email: %v", err)
	}

	tok, sess := login(t, s, c, secret)
	got, err := s.Authenticate(ctx, tok)
	if err != nil || got.UserID != sess.UserID || got.Email != email {
		t.Fatalf("authenticate: %+v %v", got, err)
	}
	if !s.RecentlyReauthenticated(got) {
		t.Fatal("a fresh login should count as recent authentication")
	}
	if _, err := s.Authenticate(ctx, "forged-token"); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("forged token: %v", err)
	}
	if err := s.Logout(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, tok); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("after logout: %v", err)
	}
}

func TestTOTPReplayAndChallengeExpiry(t *testing.T) {
	s, c := newService(t, auth.Config{})
	ctx := context.Background()
	secret := setupOwner(t, s, c)

	ch, err := s.Login(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	cd := code(t, secret, c)
	if _, err := s.CompleteLogin(ctx, ch.Token, cd, nil, ""); err != nil {
		t.Fatal(err)
	}
	// The same code cannot be used again, even with a new challenge.
	ch2, _ := s.Login(ctx, email, password)
	if _, err := s.CompleteLogin(ctx, ch2.Token, cd, nil, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("replayed code: %v", err)
	}
	// A challenge is single-use.
	c.Step()
	if _, err := s.CompleteLogin(ctx, ch.Token, code(t, secret, c), nil, ""); !errors.Is(err, auth.ErrChallengeExpired) {
		t.Fatalf("reused challenge: %v", err)
	}
}

func TestLockout(t *testing.T) {
	s, c := newService(t, auth.Config{LockoutThreshold: 3, LockoutDuration: time.Hour})
	ctx := context.Background()
	secret := setupOwner(t, s, c)

	for i := range 2 {
		if _, err := s.Login(ctx, email, "wrong password!!"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Login(ctx, email, "wrong password!!"); !errors.Is(err, auth.ErrLocked) {
		t.Fatalf("third failure should lock: %v", err)
	}
	// Even the right password is refused while locked.
	if _, err := s.Login(ctx, email, password); !errors.Is(err, auth.ErrLocked) {
		t.Fatalf("locked account accepted the right password: %v", err)
	}
	_ = secret
}

func TestWrongTOTPCountsTowardLockout(t *testing.T) {
	s, c := newService(t, auth.Config{LockoutThreshold: 2, LockoutDuration: time.Hour})
	ctx := context.Background()
	setupOwner(t, s, c)

	ch, err := s.Login(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompleteLogin(ctx, ch.Token, "000001", nil, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("first wrong code: %v", err)
	}
	if _, err := s.CompleteLogin(ctx, ch.Token, "000002", nil, ""); !errors.Is(err, auth.ErrLocked) {
		t.Fatalf("second wrong code should lock: %v", err)
	}
}

func TestReauthAndIdleTimeout(t *testing.T) {
	s, c := newService(t, auth.Config{ReauthWindow: time.Minute, IdleTimeout: time.Hour})
	ctx := context.Background()
	secret := setupOwner(t, s, c)
	tok, _ := login(t, s, c, secret)

	// Move past the re-auth window.
	for range 4 {
		c.Step()
	}
	sess, err := s.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if s.RecentlyReauthenticated(sess) {
		t.Fatal("re-auth should have expired")
	}
	if err := s.Reauthenticate(ctx, sess, "wrong password!!", code(t, secret, c)); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("reauth with wrong password: %v", err)
	}
	if err := s.Reauthenticate(ctx, sess, password, code(t, secret, c)); err != nil {
		t.Fatal(err)
	}
	sess, _ = s.Authenticate(ctx, tok)
	if !s.RecentlyReauthenticated(sess) {
		t.Fatalf("reauth not recorded: %+v", sess.ReauthAt)
	}

	// Two hours idle ends the session.
	for range 240 {
		c.Step()
	}
	if _, err := s.Authenticate(ctx, tok); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("idle session still valid: %v", err)
	}
}

func TestLimiter(t *testing.T) {
	l := auth.NewLimiter(3, time.Minute)
	for i := range 3 {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("attempt %d refused", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("fourth attempt allowed")
	}
	if !l.Allow("5.6.7.8") {
		t.Fatal("other address refused")
	}
}
