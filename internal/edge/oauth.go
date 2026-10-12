package edge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/jwtes"
	"github.com/israel-duff/pgdock/internal/projauth"
)

// OAuth sign-in (V4 §4.1) with the project's own app at Google, Apple,
// GitHub, Facebook or Microsoft. The app starts at /authorize with a PKCE
// challenge; the provider comes back to /callback, where the edge
// exchanges the provider's code itself and hands the app a one-time code
// for /token?grant_type=pkce with its verifier. Without a challenge the
// tokens go back in the redirect's fragment (the implicit flow).

const (
	flowTTL     = 10 * time.Minute
	authCodeTTL = 5 * time.Minute
)

// OAuthEndpoints are a provider's URLs.
type OAuthEndpoints struct {
	AuthURL, TokenURL, UserURL, EmailsURL string
}

type oauthProvider struct {
	OAuthEndpoints
	scopes []string
	// pkce sends a PKCE challenge to the provider too.
	pkce bool
	// emailVerified says whether the email it reports is verified when the
	// provider doesn't say.
	emailVerified bool
}

var oauthProviders = map[string]oauthProvider{
	"google": {OAuthEndpoints: OAuthEndpoints{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token", UserURL: "https://openidconnect.googleapis.com/v1/userinfo"},
		scopes: []string{"openid", "email", "profile"}, pkce: true},
	"apple": {OAuthEndpoints: OAuthEndpoints{AuthURL: "https://appleid.apple.com/auth/authorize",
		TokenURL: "https://appleid.apple.com/auth/token"}, scopes: []string{"name", "email"}},
	"github": {OAuthEndpoints: OAuthEndpoints{AuthURL: "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token", UserURL: "https://api.github.com/user",
		EmailsURL: "https://api.github.com/user/emails"}, scopes: []string{"read:user", "user:email"}},
	"facebook": {OAuthEndpoints: OAuthEndpoints{AuthURL: "https://www.facebook.com/v19.0/dialog/oauth",
		TokenURL: "https://graph.facebook.com/v19.0/oauth/access_token", UserURL: "https://graph.facebook.com/me?fields=id,name,email,picture"},
		scopes: []string{"email", "public_profile"}, pkce: true, emailVerified: true},
	"microsoft": {OAuthEndpoints: OAuthEndpoints{AuthURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token", UserURL: "https://graph.microsoft.com/oidc/userinfo"},
		scopes: []string{"openid", "email", "profile"}, pkce: true},
}

// OAuthProviders are the providers the edge knows.
func OAuthProviders() []string {
	out := make([]string, 0, len(oauthProviders))
	for n := range oauthProviders {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func (e *Edge) provider(name string) (oauthProvider, bool) {
	p, ok := oauthProviders[name]
	if !ok {
		return p, false
	}
	if o, ok := e.cfg.OAuthEndpoints[name]; ok {
		if o.AuthURL != "" {
			p.AuthURL = o.AuthURL
		}
		if o.TokenURL != "" {
			p.TokenURL = o.TokenURL
		}
		if o.UserURL != "" {
			p.UserURL = o.UserURL
		}
		if o.EmailsURL != "" {
			p.EmailsURL = o.EmailsURL
		}
	}
	return p, true
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func s256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (e *Edge) callbackURL(p *project) string {
	return "https://" + p.cfg.Ref + "." + e.cfg.Domain + "/auth/v1/callback"
}

// authorize starts an OAuth sign-in (req nil: no API key, from a browser)
// or, from /user/identities/authorize with the user's token, a link.
func (e *Edge) oauthAuthorize(c *call, req *Request) {
	if !e.authLimit(c, "signin") {
		return
	}
	q := c.r.URL.Query()
	name := q.Get("provider")
	a := c.p.cfg.Auth
	client, on := a.OAuth[name]
	prov, known := e.provider(name)
	if !known || !on {
		c.fail(http.StatusBadRequest, "provider_disabled", fmt.Sprintf("sign-in with %q isn't turned on for this project", name))
		return
	}
	redirect, ok := redirectFor(a, q.Get("redirect_to"))
	if !ok || redirect == "" {
		c.fail(http.StatusBadRequest, "redirect_not_allowed", "redirect_to isn't one of the project's redirect URLs (or set the site URL)")
		return
	}
	challenge, method := q.Get("code_challenge"), q.Get("code_challenge_method")
	switch {
	case challenge == "":
		method = ""
	case method == "" || strings.EqualFold(method, "s256"):
		method = "s256"
	case strings.EqualFold(method, "plain"):
		method = "plain"
	default:
		c.fail(http.StatusBadRequest, "invalid_code_challenge_method", "code_challenge_method is S256 or plain")
		return
	}
	if challenge != "" && (len(challenge) < 43 || len(challenge) > 128) {
		c.fail(http.StatusBadRequest, "invalid_code_challenge", "code_challenge must be 43 to 128 characters")
		return
	}
	var link *uuid.UUID
	if req != nil {
		uid, _, ok := sessionOf(c, *req)
		if !ok {
			return
		}
		if !a.ManualLinking {
			c.fail(http.StatusForbidden, "manual_linking_disabled", "linking identities is turned off for this project")
			return
		}
		link = &uid
	}
	state, verifier, nonce := randToken(32), randToken(48), randToken(16)
	ctx := c.r.Context()
	err := e.authTx(c, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM pgd_auth.flow_state WHERE expires_at < now() - interval '1 hour'`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO pgd_auth.flow_state (provider, state_hash, provider_verifier, nonce, code_challenge,
			code_challenge_method, redirect_to, link_user_id, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			name, sha256hex(state), verifier, nonce, challenge, method, redirect, link, time.Now().Add(flowTTL))
		return err
	})
	if err != nil {
		e.authDBError(c, err)
		return
	}
	scopes := client.Scopes
	if len(scopes) == 0 {
		scopes = prov.scopes
	}
	if extra := strings.Fields(q.Get("scopes")); len(extra) > 0 {
		scopes = append(slices.Clone(scopes), extra...)
	}
	v := url.Values{"client_id": {client.ClientID}, "redirect_uri": {e.callbackURL(c.p)}, "response_type": {"code"},
		"scope": {strings.Join(scopes, " ")}, "state": {state}}
	if prov.pkce {
		v.Set("code_challenge", s256(verifier))
		v.Set("code_challenge_method", "S256")
	}
	switch name {
	case "google", "microsoft":
		v.Set("nonce", nonce)
	case "apple":
		v.Set("response_mode", "form_post")
		v.Set("nonce", nonce)
	}
	target := prov.AuthURL
	if strings.Contains(target, "?") {
		target += "&" + v.Encode()
	} else {
		target += "?" + v.Encode()
	}
	if req != nil || q.Get("skip_http_redirect") == "true" {
		c.json(http.StatusOK, map[string]any{"url": target, "provider": name})
		return
	}
	c.w.Header().Set("Cache-Control", "no-store")
	http.Redirect(c.w, c.r, target, http.StatusFound)
}

type flowRow struct {
	id                            uuid.UUID
	provider, verifier, nonce     string
	challenge, method, redirectTo string
	linkUser                      *uuid.UUID
}

// providerUser is who the provider says signed in.
type providerUser struct {
	ID            string
	Email         string
	EmailVerified bool
	Name          string
	Avatar        string
	Data          map[string]any
}

// callback is where the provider sends the browser back (GET, or a POST
// form for Apple).
func (e *Edge) callback(c *call) {
	if !e.authLimit(c, "verify") {
		return
	}
	if err := c.r.ParseForm(); err != nil {
		linkPage(c, http.StatusBadRequest, "The sign-in response is malformed.")
		return
	}
	state, code := c.r.Form.Get("state"), c.r.Form.Get("code")
	if state == "" {
		linkPage(c, http.StatusBadRequest, "The sign-in response has no state.")
		return
	}
	ctx := c.r.Context()
	var f flowRow
	var found bool
	err := e.withAuth(ctx, c.p, func(tx pgx.Tx) error {
		// The state works once.
		err := tx.QueryRow(ctx, `UPDATE pgd_auth.flow_state SET state_hash = 'used:' || id::text
			WHERE state_hash = $1 AND expires_at > now() AND auth_code_hash IS NULL
			RETURNING id, provider, provider_verifier, coalesce(nonce, ''), code_challenge, code_challenge_method, redirect_to, link_user_id`,
			sha256hex(state)).Scan(&f.id, &f.provider, &f.verifier, &f.nonce, &f.challenge, &f.method, &f.redirectTo, &f.linkUser)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		found = err == nil
		return err
	})
	if err != nil {
		e.cfg.Log.Warn("oauth callback", "project", c.p.cfg.Ref, "err", err)
		linkPage(c, http.StatusServiceUnavailable, "The sign-in couldn't be finished; try again.")
		return
	}
	if !found {
		linkPage(c, http.StatusBadRequest, "This sign-in has expired or was already used; start again.")
		return
	}
	fail := func(code, msg string) { redirectError(c, f.redirectTo, f.challenge != "", code, msg) }
	if pe := c.r.Form.Get("error"); pe != "" {
		fail("provider_error", strings.TrimSpace(pe+" "+c.r.Form.Get("error_description")))
		return
	}
	client, on := c.p.cfg.Auth.OAuth[f.provider]
	prov, _ := e.provider(f.provider)
	if !on || code == "" {
		fail("provider_disabled", "the provider is turned off or sent no code")
		return
	}
	pu, err := e.providerUser(ctx, c, f, prov, client, code)
	if err != nil {
		e.cfg.Log.Warn("oauth exchange", "project", c.p.cfg.Ref, "provider", f.provider, "err", err)
		fail("provider_exchange_failed", "the provider didn't confirm the sign-in")
		return
	}
	var out tokenResponse
	var authCode string
	var refusal *apiErr
	err = e.authTx(c, func(tx pgx.Tx) error {
		u, a, err := e.oauthUser(ctx, c, tx, f, pu)
		if err != nil || a != nil {
			refusal = a
			return err
		}
		if f.challenge == "" {
			out, err = e.startSession(ctx, c, tx, u, f.provider)
			_, derr := tx.Exec(ctx, `DELETE FROM pgd_auth.flow_state WHERE id = $1`, f.id)
			return errors.Join(err, derr)
		}
		authCode = randToken(32)
		_, err = tx.Exec(ctx, `UPDATE pgd_auth.flow_state SET user_id = $2, auth_code_hash = $3, expires_at = $4 WHERE id = $1`,
			f.id, u.ID, sha256hex(authCode), time.Now().Add(authCodeTTL))
		return err
	})
	if err != nil {
		var a *apiErr
		if errors.As(err, &a) {
			fail(a.code, a.msg)
			return
		}
		e.cfg.Log.Warn("oauth sign-in", "project", c.p.cfg.Ref, "err", err)
		fail("server_error", "the sign-in couldn't be finished; try again")
		return
	}
	if refusal != nil {
		fail(refusal.code, refusal.msg)
		return
	}
	u, _ := url.Parse(f.redirectTo)
	c.w.Header().Set("Cache-Control", "no-store")
	c.w.Header().Set("Referrer-Policy", "no-referrer")
	if authCode != "" {
		qv := u.Query()
		qv.Set("code", authCode)
		u.RawQuery, u.Fragment = qv.Encode(), ""
		http.Redirect(c.w, c.r, u.String(), http.StatusSeeOther)
		return
	}
	frag := url.Values{"access_token": {out.AccessToken}, "refresh_token": {out.RefreshToken}, "expires_in": {strconv.Itoa(out.ExpiresIn)},
		"expires_at": {strconv.FormatInt(out.ExpiresAt, 10)}, "token_type": {"bearer"}, "provider_type": {f.provider}}
	u.Fragment = ""
	http.Redirect(c.w, c.r, u.String()+"#"+frag.Encode(), http.StatusSeeOther)
}

// redirectError sends the browser back to the app with an error (in the
// query for PKCE, the fragment otherwise, as Supabase's clients read it).
func redirectError(c *call, to string, pkce bool, code, msg string) {
	u, err := url.Parse(to)
	if err != nil || to == "" {
		linkPage(c, http.StatusBadRequest, msg)
		return
	}
	v := url.Values{"error": {"access_denied"}, "error_code": {code}, "error_description": {msg}}
	c.w.Header().Set("Cache-Control", "no-store")
	if pkce {
		q := u.Query()
		for k, vs := range v {
			q[k] = vs
		}
		u.RawQuery, u.Fragment = q.Encode(), ""
		http.Redirect(c.w, c.r, u.String(), http.StatusSeeOther)
		return
	}
	u.Fragment = ""
	http.Redirect(c.w, c.r, u.String()+"#"+v.Encode(), http.StatusSeeOther)
}

// oauthUser finds or makes the user for pu: its identity's user, the
// user linking it, a user with the same verified email, or a new user.
func (e *Edge) oauthUser(ctx context.Context, c *call, tx pgx.Tx, f flowRow, pu providerUser) (*projauth.User, *apiErr, error) {
	a := c.p.cfg.Auth
	data := map[string]any{"sub": pu.ID, "provider": f.provider}
	for k, v := range pu.Data {
		data[k] = v
	}
	if pu.Email != "" {
		data["email"], data["email_verified"] = pu.Email, pu.EmailVerified
	}
	owner, err := projauth.IdentityOwner(ctx, tx, f.provider, pu.ID)
	if err != nil && !errors.Is(err, projauth.ErrNotFound) {
		return nil, nil, err
	}
	var u *projauth.User
	switch {
	case err == nil:
		if f.linkUser != nil && *f.linkUser != owner {
			return nil, refuse(http.StatusUnprocessableEntity, "identity_already_exists", "this account is already linked to another user"), nil
		}
		if u, err = projauth.GetUser(ctx, tx, owner, true); err != nil {
			return nil, nil, err
		}
	case f.linkUser != nil:
		if u, err = projauth.GetUser(ctx, tx, *f.linkUser, true); err != nil {
			if errors.Is(err, projauth.ErrNotFound) {
				return nil, refuse(http.StatusUnauthorized, "user_not_found", "the user no longer exists"), nil
			}
			return nil, nil, err
		}
	default:
		email := projauth.NormalizeEmail(pu.Email)
		if email != "" && pu.EmailVerified {
			existing, err := projauth.UserByEmail(ctx, tx, email, true)
			if err != nil && !errors.Is(err, projauth.ErrNotFound) {
				return nil, nil, err
			}
			if existing != nil {
				// The provider proved the address: a password set by
				// whoever signed up with it unconfirmed doesn't come along.
				if existing.EmailConfirmedAt == nil {
					up := projauth.Update{ConfirmEmail: true}
					if existing.HasPassword() {
						none := ""
						up.PasswordHash = &none
					}
					if _, err := projauth.EndSessions(ctx, tx, existing.ID, nil); err != nil {
						return nil, nil, err
					}
					if existing, err = projauth.UpdateUser(ctx, tx, existing.ID, up); err != nil {
						return nil, nil, err
					}
				}
				u = existing
			}
		}
		if u == nil {
			if !a.SignupEnabled {
				return nil, refuse(http.StatusForbidden, "signup_disabled", "sign-ups are turned off for this project"), nil
			}
			n := projauth.NewUser{EmailConfirmed: pu.EmailVerified && email != ""}
			if email != "" {
				if other, err := projauth.UserByEmail(ctx, tx, email, false); err == nil && other != nil {
					// An unverified address someone else holds: sign up
					// without it.
					n.EmailConfirmed = false
				} else {
					n.Email = email
				}
			}
			meta := map[string]any{"full_name": pu.Name, "name": pu.Name, "avatar_url": pu.Avatar, "provider_id": pu.ID}
			n.UserMetadata, _ = json.Marshal(meta)
			n.AppMetadata, _ = json.Marshal(map[string]any{"provider": f.provider, "providers": []string{f.provider}})
			if u, err = e.createUser(ctx, c, tx, n, f.provider); err != nil {
				return nil, nil, err
			}
			if n.Email != "" && n.EmailConfirmed {
				if err := projauth.EnsureEmailIdentity(ctx, tx, u.ID, n.Email); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	if u.Banned(time.Now()) {
		return nil, refuse(http.StatusForbidden, "user_banned", "this user is banned"), nil
	}
	if err := projauth.EnsureIdentity(ctx, tx, u.ID, f.provider, pu.ID, data); err != nil {
		if errors.Is(err, projauth.ErrExists) {
			return nil, refuse(http.StatusUnprocessableEntity, "identity_already_exists", "this account is already linked to another user"), nil
		}
		return nil, nil, err
	}
	if u.IsAnonymous {
		if u, err = projauth.UpdateUser(ctx, tx, u.ID, projauth.Update{NotAnonymous: true}); err != nil {
			return nil, nil, err
		}
	}
	if f.linkUser != nil && err == nil {
		if err := projauth.Audit(ctx, tx, &u.ID, projauth.ActLinked, c.ip, map[string]any{"provider": f.provider}); err != nil {
			return nil, nil, err
		}
	}
	return u, nil, nil
}

// exchangePKCE is POST /token?grant_type=pkce: the app's one-time code and
// its verifier for a session.
func (e *Edge) exchangePKCE(c *call) {
	var in struct {
		AuthCode     string `json:"auth_code"`
		CodeVerifier string `json:"code_verifier"`
	}
	if !authBody(c, &in) {
		return
	}
	if in.AuthCode == "" || in.CodeVerifier == "" || len(in.AuthCode) > 200 || len(in.CodeVerifier) > 128 {
		c.fail(http.StatusBadRequest, "invalid_request", "auth_code and code_verifier are required")
		return
	}
	ctx := c.r.Context()
	var out tokenResponse
	var fail *apiErr
	err := e.authTx(c, func(tx pgx.Tx) error {
		var id, uid uuid.UUID
		var provider, challenge, method string
		err := tx.QueryRow(ctx, `DELETE FROM pgd_auth.flow_state WHERE auth_code_hash = $1 AND expires_at > now()
			RETURNING id, provider, user_id, code_challenge, code_challenge_method`, sha256hex(in.AuthCode)).
			Scan(&id, &provider, &uid, &challenge, &method)
		if errors.Is(err, pgx.ErrNoRows) {
			fail = refuse(http.StatusNotFound, "flow_state_not_found", "the code is invalid, expired or already used")
			return nil
		}
		if err != nil {
			return err
		}
		want := in.CodeVerifier
		if method == "s256" {
			want = s256(in.CodeVerifier)
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) != 1 {
			fail = refuse(http.StatusForbidden, "bad_code_verifier", "code_verifier doesn't match the code challenge")
			return nil
		}
		u, err := projauth.GetUser(ctx, tx, uid, true)
		if errors.Is(err, projauth.ErrNotFound) {
			fail = refuse(http.StatusNotFound, "user_not_found", "the user no longer exists")
			return nil
		}
		if err != nil {
			return err
		}
		if u.Banned(time.Now()) {
			fail = refuse(http.StatusForbidden, "user_banned", "this user is banned")
			return nil
		}
		out, err = e.startSession(ctx, c, tx, u, provider)
		return err
	})
	if err != nil {
		e.authDBError(c, err)
		return
	}
	if fail != nil {
		fail.send(c)
		return
	}
	c.json(http.StatusOK, out)
}

// ---- Talking to providers --------------------------------------------------------

func (e *Edge) postForm(ctx context.Context, u string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return e.doJSON(req, out)
}

func (e *Edge) getJSON(ctx context.Context, u, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	return e.doJSON(req, out)
}

func (e *Edge) doJSON(req *http.Request, out any) error {
	res, err := e.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %.200s", req.Method, req.URL.Host, res.StatusCode, raw)
	}
	return json.Unmarshal(raw, out)
}

// appleSecret is Apple's client secret: a JWT signed with the app's key.
func appleSecret(client edgeapi.OAuthClient) (string, error) {
	der, err := jwtes.ParsePEM(client.PrivateKey)
	if err != nil {
		return "", err
	}
	now := time.Now()
	return jwtes.Sign(der, client.KeyID, map[string]any{"iss": client.TeamID, "iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(), "aud": "https://appleid.apple.com", "sub": client.ClientID})
}

// idTokenClaims reads an ID token's claims. It came straight from the
// provider's token endpoint over TLS, so TLS vouches for it (OpenID
// Connect Core §3.1.3.7); its audience, expiry and nonce are still
// checked.
func idTokenClaims(tok, clientID, nonce string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed id_token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var cl map[string]any
	if err := json.Unmarshal(raw, &cl); err != nil {
		return nil, err
	}
	switch aud := cl["aud"].(type) {
	case string:
		if aud != clientID {
			return nil, errors.New("id_token is for another client")
		}
	case []any:
		if !slices.Contains(aud, any(clientID)) {
			return nil, errors.New("id_token is for another client")
		}
	default:
		return nil, errors.New("id_token has no audience")
	}
	if exp, _ := cl["exp"].(float64); int64(exp) < time.Now().Add(-time.Minute).Unix() {
		return nil, errors.New("id_token expired")
	}
	if n, ok := cl["nonce"].(string); ok && nonce != "" && n != nonce {
		return nil, errors.New("id_token nonce mismatch")
	}
	return cl, nil
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	}
	return ""
}

func boolish(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true"
	}
	return false
}

// providerUser exchanges the provider's code and asks who signed in.
func (e *Edge) providerUser(ctx context.Context, c *call, f flowRow, prov oauthProvider, client edgeapi.OAuthClient, code string) (providerUser, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	secret := client.ClientSecret
	if f.provider == "apple" && client.PrivateKey != "" {
		var err error
		if secret, err = appleSecret(client); err != nil {
			return providerUser{}, err
		}
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {e.callbackURL(c.p)},
		"client_id": {client.ClientID}, "client_secret": {secret}}
	if prov.pkce {
		form.Set("code_verifier", f.verifier)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
	}
	if err := e.postForm(ctx, prov.TokenURL, form, &tok); err != nil {
		return providerUser{}, err
	}
	if tok.Error != "" || (tok.AccessToken == "" && tok.IDToken == "") {
		return providerUser{}, fmt.Errorf("token endpoint: %q", tok.Error)
	}
	var pu providerUser
	switch f.provider {
	case "apple":
		cl, err := idTokenClaims(tok.IDToken, client.ClientID, f.nonce)
		if err != nil {
			return pu, err
		}
		pu = providerUser{ID: str(cl, "sub"), Email: str(cl, "email"), EmailVerified: boolish(cl["email_verified"]), Data: map[string]any{}}
		// Apple sends the name once, in the first callback's form.
		if raw := c.r.Form.Get("user"); raw != "" {
			var u struct {
				Name struct{ FirstName, LastName string } `json:"name"`
			}
			if json.Unmarshal([]byte(raw), &u) == nil {
				pu.Name = strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
			}
		}
	case "github":
		var u struct {
			ID        int64  `json:"id"`
			Login     string `json:"login"`
			Name      string `json:"name"`
			AvatarURL string `json:"avatar_url"`
			Email     string `json:"email"`
		}
		if err := e.getJSON(ctx, prov.UserURL, tok.AccessToken, &u); err != nil {
			return pu, err
		}
		pu = providerUser{ID: strconv.FormatInt(u.ID, 10), Name: u.Name, Avatar: u.AvatarURL,
			Data: map[string]any{"user_name": u.Login, "name": u.Name, "avatar_url": u.AvatarURL}}
		if pu.Name == "" {
			pu.Name = u.Login
		}
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		if prov.EmailsURL != "" && e.getJSON(ctx, prov.EmailsURL, tok.AccessToken, &emails) == nil {
			for _, m := range emails {
				if m.Primary {
					pu.Email, pu.EmailVerified = m.Email, m.Verified
				}
			}
		}
	default:
		var u map[string]any
		if err := e.getJSON(ctx, prov.UserURL, tok.AccessToken, &u); err != nil {
			return pu, err
		}
		id := str(u, "sub")
		if id == "" {
			id = str(u, "id")
		}
		pu = providerUser{ID: id, Email: str(u, "email"), Name: str(u, "name"), Data: map[string]any{"name": str(u, "name")}}
		if v, ok := u["email_verified"]; ok {
			pu.EmailVerified = boolish(v)
		} else {
			pu.EmailVerified = prov.emailVerified
		}
		if pic := str(u, "picture"); pic != "" {
			pu.Avatar = pic
		} else if p, ok := u["picture"].(map[string]any); ok {
			if d, ok := p["data"].(map[string]any); ok {
				pu.Avatar = str(d, "url")
			}
		}
		if f.provider == "google" && tok.IDToken != "" {
			if _, err := idTokenClaims(tok.IDToken, client.ClientID, f.nonce); err != nil {
				return pu, err
			}
		}
	}
	if pu.ID == "" {
		return pu, errors.New("the provider named no user")
	}
	pu.Data["avatar_url"] = pu.Avatar
	return pu, nil
}
