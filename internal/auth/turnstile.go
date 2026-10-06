package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ErrChallengeFailed is a signup whose anti-bot challenge didn't pass.
var ErrChallengeFailed = errors.New("the anti-bot check didn't pass; reload the page and try again")

// TurnstileURL is Cloudflare Turnstile's verification endpoint.
const TurnstileURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// BotCheck verifies a signup's anti-bot token (V3 §7.4).
type BotCheck interface {
	Verify(ctx context.Context, token string, ip *netip.Addr) error
}

// Turnstile verifies Cloudflare Turnstile tokens.
type Turnstile struct {
	SiteKey string
	Secret  string
	URL     string
	Client  *http.Client
}

// Verify asks Turnstile whether token was solved, from ip.
func (t Turnstile) Verify(ctx context.Context, token string, ip *netip.Addr) error {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 2048 {
		return ErrChallengeFailed
	}
	form := url.Values{"secret": {t.Secret}, "response": {token}}
	if ip != nil {
		form.Set("remoteip", ip.String())
	}
	endpoint := t.URL
	if endpoint == "" {
		endpoint = TurnstileURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c := t.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("turnstile: %w", err)
	}
	defer res.Body.Close()
	var out struct {
		Success bool     `json:"success"`
		Codes   []string `json:"error-codes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return fmt.Errorf("turnstile: %d: %w", res.StatusCode, err)
	}
	if !out.Success {
		return fmt.Errorf("%w (%s)", ErrChallengeFailed, strings.Join(out.Codes, ", "))
	}
	return nil
}

// SignupGuard is open signup's protection (V3 §7.4): an anti-bot challenge
// and a cap on accounts created from one IP address a day.
type SignupGuard struct {
	Check   BotCheck
	SiteKey string
	PerIP   int
}

// SetSignupGuard installs open signup's protection.
func (s *Service) SetSignupGuard(g SignupGuard) { s.guard = g }

// ChallengeSiteKey is the challenge's public key for the signup page, or
// empty when there is no challenge.
func (s *Service) ChallengeSiteKey() string {
	if s.guard.Check == nil {
		return ""
	}
	return s.guard.SiteKey
}
