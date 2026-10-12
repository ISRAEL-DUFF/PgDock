package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/israel-duff/pgdock/internal/auth"
)

// A Turnstile challenge and the per-IP cap on open signup (V3 §7.4).
func TestSignupGuard(t *testing.T) {
	s, _, _, terms, _ := newAccounts(t)
	ctx := context.Background()
	if _, err := s.SetSignupPolicy(ctx, auth.SignupPolicy{Mode: auth.SignupOpen}); err != nil {
		t.Fatal(err)
	}
	var gotIP string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotIP = r.PostForm.Get("remoteip")
		if r.PostForm.Get("secret") == "sekrit" && r.PostForm.Get("response") == "solved" {
			_, _ = w.Write([]byte(`{"success": true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success": false, "error-codes": ["invalid-input-response"]}`))
	}))
	defer ts.Close()
	s.SetSignupGuard(auth.SignupGuard{Check: auth.Turnstile{SiteKey: "site", Secret: "sekrit", URL: ts.URL}, SiteKey: "site", PerIP: 2})
	if s.ChallengeSiteKey() != "site" {
		t.Fatal("site key")
	}
	ip := netip.MustParseAddr("203.0.113.7")
	signup := func(email, challenge string) error {
		return s.Signup(ctx, auth.SignupParams{Email: email, Password: password, TermsVersion: terms, IP: &ip, Challenge: challenge})
	}
	if err := signup("bot@example.com", ""); !errors.Is(err, auth.ErrChallengeFailed) {
		t.Fatalf("no token: %v", err)
	}
	if err := signup("bot@example.com", "guessed"); !errors.Is(err, auth.ErrChallengeFailed) {
		t.Fatalf("a wrong token: %v", err)
	}
	for _, e := range []string{"one@example.com", "two@example.com"} {
		if err := signup(e, "solved"); err != nil {
			t.Fatalf("%s: %v", e, err)
		}
	}
	if gotIP != ip.String() {
		t.Errorf("remote IP sent to Turnstile: %q", gotIP)
	}
	if err := signup("three@example.com", "solved"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("a third account from one address: %v", err)
	}
	other := netip.MustParseAddr("198.51.100.9")
	if err := s.Signup(ctx, auth.SignupParams{Email: "three@example.com", Password: password, TermsVersion: terms, IP: &other, Challenge: "solved"}); err != nil {
		t.Fatalf("another address: %v", err)
	}
}
