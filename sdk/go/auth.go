package pgdock

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"
)

// User is a project's user.
type User struct {
	ID               string         `json:"id"`
	Email            *string        `json:"email"`
	Phone            *string        `json:"phone"`
	EmailConfirmedAt *time.Time     `json:"email_confirmed_at"`
	PhoneConfirmedAt *time.Time     `json:"phone_confirmed_at"`
	IsAnonymous      bool           `json:"is_anonymous"`
	AppMetadata      map[string]any `json:"app_metadata"`
	UserMetadata     map[string]any `json:"user_metadata"`
	Identities       []struct {
		ID         string `json:"id"`
		Provider   string `json:"provider"`
		ProviderID string `json:"provider_id"`
	} `json:"identities"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSignInAt *time.Time `json:"last_sign_in_at"`
}

// Session is a signed-in user's tokens.
type Session struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	User         User   `json:"user"`
}

// Expires is when the access token expires.
func (s *Session) Expires() time.Time { return time.Unix(s.ExpiresAt, 0) }

// Auth signs users up and in and keeps the session's tokens current.
// One Client holds one session (an app, a CLI, a test); a server serving
// many users uses WithToken per request instead.
type Auth struct {
	c *Client

	mu         sync.Mutex
	session    *Session
	refreshing chan struct{}
	refreshErr error
	// OnChange is called after every sign-in, refresh and sign-out.
	OnChange func(event string, s *Session)

	jwksMu  sync.Mutex
	jwks    map[string]*ecdsa.PublicKey
	jwksAt  time.Time
	stopBkg context.CancelFunc
}

// refreshMargin is how long before expiry a token is refreshed.
const refreshMargin = time.Minute

func (a *Auth) set(s *Session, event string) {
	a.mu.Lock()
	a.session = s
	cb := a.OnChange
	a.mu.Unlock()
	a.changed(event, s)
	if cb != nil {
		cb(event, s)
	}
}

// changed tells realtime's channels about the new token.
func (a *Auth) changed(event string, s *Session) {
	if a.c.Realtime == nil || event == "SIGNED_OUT" {
		return
	}
	if s != nil {
		go a.c.Realtime.SetToken(context.Background(), s.AccessToken)
	}
}

// Session is the current session (nil when signed out), refreshed first
// when its access token is about to expire.
func (a *Auth) Session(ctx context.Context) (*Session, error) {
	a.mu.Lock()
	s := a.session
	a.mu.Unlock()
	if s == nil {
		return nil, nil
	}
	if time.Until(s.Expires()) > refreshMargin {
		return s, nil
	}
	return a.Refresh(ctx)
}

// SetSession adopts a session kept from before (an app restarting).
func (a *Auth) SetSession(s *Session) { a.set(s, "SIGNED_IN") }

func (a *Auth) currentToken(ctx context.Context) (string, error) {
	s, err := a.Session(ctx)
	if err != nil || s == nil {
		return "", err
	}
	return s.AccessToken, nil
}

// Refresh gets new tokens. One refresh runs at a time: a refresh token
// works once, and presenting it again ends the session.
func (a *Auth) Refresh(ctx context.Context) (*Session, error) {
	a.mu.Lock()
	if ch := a.refreshing; ch != nil {
		a.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.session, a.refreshErr
	}
	s := a.session
	if s == nil {
		a.mu.Unlock()
		return nil, &Error{Status: 401, Code: "no_session", Message: "not signed in"}
	}
	ch := make(chan struct{})
	a.refreshing = ch
	a.mu.Unlock()

	var next Session
	err := a.c.doJSON(ctx, request{path: "/auth/v1/token", query: url.Values{"grant_type": {"refresh_token"}},
		body: map[string]string{"refresh_token": s.RefreshToken}, noToken: true}, &next)
	a.mu.Lock()
	a.refreshing, a.refreshErr = nil, err
	var e *Error
	switch {
	case err == nil:
		a.session = &next
	case errors.As(err, &e) && e.Status >= 400 && e.Status < 500:
		a.session = nil // refused: signed out
	}
	cur, cb := a.session, a.OnChange
	a.mu.Unlock()
	close(ch)
	if err == nil {
		a.changed("TOKEN_REFRESHED", cur)
	}
	if cb != nil {
		if err == nil {
			cb("TOKEN_REFRESHED", cur)
		} else if cur == nil {
			cb("SIGNED_OUT", nil)
		}
	}
	if err != nil {
		return nil, err
	}
	return cur, nil
}

// AutoRefresh refreshes the session in the background ahead of expiry
// until ctx ends (or the session does). Requests refresh on their own
// too; this keeps a long-idle client signed in.
func (a *Auth) AutoRefresh(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if a.stopBkg != nil {
		a.stopBkg()
	}
	a.stopBkg = cancel
	a.mu.Unlock()
	go func() {
		for {
			a.mu.Lock()
			s := a.session
			a.mu.Unlock()
			wait := time.Minute
			if s != nil {
				wait = max(time.Until(s.Expires())-refreshMargin, time.Second)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if s != nil {
				if _, err := a.Refresh(ctx); err != nil {
					var e *Error
					if !errors.As(err, &e) { // the network: try again soon
						select {
						case <-ctx.Done():
							return
						case <-time.After(10 * time.Second):
						}
					}
				}
			}
		}
	}()
}

type signInResult struct {
	Session
	ConfirmationSent bool `json:"confirmation_sent"`
}

func (a *Auth) signedIn(ctx context.Context, path string, query url.Values, body any) (*Session, error) {
	var r signInResult
	if err := a.c.doJSON(ctx, request{path: path, query: query, body: body, noToken: true}, &r); err != nil {
		return nil, err
	}
	if r.AccessToken == "" {
		return nil, nil // a confirmation was sent
	}
	s := r.Session
	a.set(&s, "SIGNED_IN")
	return &s, nil
}

// Credentials are an email or a phone, and a password.
type Credentials struct {
	Email    string         `json:"email,omitempty"`
	Phone    string         `json:"phone,omitempty"`
	Password string         `json:"password"`
	Data     map[string]any `json:"data,omitempty"`
	// RedirectTo is where a confirmation link goes (sign-up).
	RedirectTo   string `json:"redirect_to,omitempty"`
	CaptchaToken string `json:"captcha_token,omitempty"`
}

// SignUp makes an account; the session is nil while confirmation is pending.
func (a *Auth) SignUp(ctx context.Context, c Credentials) (*Session, error) {
	return a.signedIn(ctx, "/auth/v1/signup", nil, c)
}

// SignInWithPassword signs in by email or phone and password.
func (a *Auth) SignInWithPassword(ctx context.Context, c Credentials) (*Session, error) {
	return a.signedIn(ctx, "/auth/v1/signin/password", nil, c)
}

// SignInAnonymously makes an anonymous user (when the project allows them).
func (a *Auth) SignInAnonymously(ctx context.Context) (*Session, error) {
	return a.signedIn(ctx, "/auth/v1/signup", nil, map[string]any{})
}

// OTP is a request for a code: Email, or Phone with Channel "sms" or "whatsapp".
type OTP struct {
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	Channel      string `json:"channel,omitempty"`
	CreateUser   *bool  `json:"create_user,omitempty"`
	RedirectTo   string `json:"redirect_to,omitempty"`
	CaptchaToken string `json:"captcha_token,omitempty"`
}

// SignInWithOTP sends a code (and by email a magic link).
func (a *Auth) SignInWithOTP(ctx context.Context, o OTP) error {
	return a.c.doJSON(ctx, request{path: "/auth/v1/signin/otp", body: o, noToken: true}, nil)
}

// Verify is a code to check: Type is signup, magiclink, email, recovery,
// invite or email_change with Email; sms, whatsapp or phone_change with Phone.
type Verify struct {
	Type      string `json:"type"`
	Email     string `json:"email,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Token     string `json:"token,omitempty"`
	TokenHash string `json:"token_hash,omitempty"`
}

// VerifyOTP signs in with a code.
func (a *Auth) VerifyOTP(ctx context.Context, v Verify) (*Session, error) {
	return a.signedIn(ctx, "/auth/v1/verify", nil, v)
}

// ResetPasswordForEmail sends a reset link and code.
func (a *Auth) ResetPasswordForEmail(ctx context.Context, email, redirectTo string) error {
	return a.c.doJSON(ctx, request{path: "/auth/v1/recover", body: map[string]string{"email": email, "redirect_to": redirectTo}, noToken: true}, nil)
}

// OAuthStart is a provider sign-in to send the user to.
type OAuthStart struct {
	URL string
	// Verifier finishes it with ExchangeCode; keep it for the callback.
	Verifier string
}

// SignInWithOAuth is the authorize URL for a provider, with PKCE.
func (a *Auth) SignInWithOAuth(provider, redirectTo string) OAuthStart {
	b := make([]byte, 32)
	_, _ = randRead(b)
	verifier := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	v := url.Values{"provider": {provider}, "redirect_to": {redirectTo},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"s256"}}
	return OAuthStart{URL: a.c.URL + "/auth/v1/authorize?" + v.Encode(), Verifier: verifier}
}

// ExchangeCode finishes an OAuth sign-in with the redirect's code.
func (a *Auth) ExchangeCode(ctx context.Context, code, verifier string) (*Session, error) {
	return a.signedIn(ctx, "/auth/v1/token", url.Values{"grant_type": {"pkce"}}, map[string]string{"auth_code": code, "code_verifier": verifier})
}

// SignOut ends the session ("local"), the others, or all ("global").
func (a *Auth) SignOut(ctx context.Context, scope string) error {
	if scope == "" {
		scope = "local"
	}
	tok, err := a.c.accessToken(ctx)
	if err != nil {
		return err
	}
	if tok != "" {
		if err := a.c.doJSON(ctx, request{path: "/auth/v1/signout", query: url.Values{"scope": {scope}}, body: map[string]any{}, token: &tok}, nil); err != nil {
			var e *Error
			if !errors.As(err, &e) || e.Status != 401 {
				return err
			}
		}
	}
	if scope != "others" {
		a.set(nil, "SIGNED_OUT")
	}
	return nil
}

// User is the signed-in user, fresh from the API.
func (a *Auth) User(ctx context.Context) (*User, error) {
	var u User
	if err := a.c.doJSON(ctx, request{path: "/auth/v1/user"}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// UserUpdate changes the signed-in user.
type UserUpdate struct {
	Password string         `json:"password,omitempty"`
	Email    string         `json:"email,omitempty"`
	Phone    string         `json:"phone,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

// UpdateUser changes the password, metadata, email or phone.
func (a *Auth) UpdateUser(ctx context.Context, up UserUpdate) (*User, error) {
	var u User
	if err := a.c.doJSON(ctx, request{method: "PATCH", path: "/auth/v1/user", body: up}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// ---- Verifying tokens (servers) --------------------------------------------------

// Claims are a verified access token's claims.
type Claims map[string]any

// Subject is the user's id.
func (c Claims) Subject() string { s, _ := c["sub"].(string); return s }

// Role is "user" for a user's token.
func (c Claims) Role() string { s, _ := c["role"].(string); return s }

// VerifyToken checks an access token's ES256 signature against the
// project's JWKS (cached for 10 minutes, refetched for an unknown key),
// its expiry and its audience (the project ref), and returns its claims.
func (a *Auth) VerifyToken(ctx context.Context, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("pgdock: not a JWT")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSeg(parts[0], &hdr); err != nil || hdr.Alg != "ES256" {
		return nil, errors.New("pgdock: not an ES256 token")
	}
	key, err := a.key(ctx, hdr.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("pgdock: bad signature encoding")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(key, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("pgdock: invalid signature")
	}
	var c Claims
	if err := decodeSeg(parts[1], &c); err != nil {
		return nil, errors.New("pgdock: bad claims")
	}
	if exp, ok := c["exp"].(float64); !ok || time.Now().After(time.Unix(int64(exp), 0)) {
		return nil, errors.New("pgdock: the token has expired")
	}
	if ref := a.ref(); ref != "" {
		if aud, _ := c["aud"].(string); aud != ref {
			return nil, fmt.Errorf("pgdock: the token is for project %q, not %q", aud, ref)
		}
	}
	return c, nil
}

// ref is the project ref from the URL's first label.
func (a *Auth) ref() string {
	u, err := url.Parse(a.c.URL)
	if err != nil {
		return ""
	}
	h := u.Hostname()
	if i := strings.IndexByte(h, '.'); i > 0 && !isIP(h) {
		return h[:i]
	}
	return ""
}

func isIP(h string) bool {
	return strings.Count(h, ".") == 3 && strings.Trim(h, "0123456789.") == "" || strings.Contains(h, ":")
}

func decodeSeg(s string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (a *Auth) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	a.jwksMu.Lock()
	defer a.jwksMu.Unlock()
	if k := a.jwks[kid]; k != nil && time.Since(a.jwksAt) < 10*time.Minute {
		return k, nil
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := a.c.doJSON(ctx, request{path: "/auth/v1/.well-known/jwks.json", noToken: true}, &set); err != nil {
		return nil, err
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" {
			continue
		}
		x, err1 := base64.RawURLEncoding.DecodeString(k.X)
		y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
		if err1 != nil || err2 != nil || len(x) > 32 || len(y) > 32 {
			continue
		}
		point := make([]byte, 65)
		point[0] = 4
		copy(point[1+32-len(x):33], x)
		copy(point[33+32-len(y):], y)
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	a.jwks, a.jwksAt = keys, time.Now()
	if k := keys[kid]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("pgdock: no key %q in the project's JWKS", kid)
}
