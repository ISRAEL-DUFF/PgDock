// Package edgeapi is the contract between pgdock-server and pgdock-edge
// (V4 §2.1): the configuration feed the edge keeps its per-project cache
// from, and the reports it sends back (usage, request logs, keys in use,
// projects to wake). Every request is signed with the shared edge secret
// (the PGDock-Signature scheme over the method, path and body), so only an
// edge can read project configuration, which includes the edge login's
// password.
package edgeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/signature"
)

// Paths on pgdock-server.
const (
	PathConfig = "/api/v1/edge/config"
	PathReport = "/api/v1/edge/report"
	PathWake   = "/api/v1/edge/wake"
	// PathAuthMessage queues an auth email or code (V4 §4.5, §4.6).
	PathAuthMessage = "/api/v1/edge/auth-message"
	// PathAuthHook delivers an auth event to the project's webhooks, or
	// asks its before-sign-up webhook (V4 §4.7).
	PathAuthHook = "/api/v1/edge/auth-hook"
)

// Key kinds.
const (
	KindPublishable = "publishable"
	KindSecret      = "secret"
)

// HashKey is how API keys are stored and looked up: hex SHA-256.
func HashKey(key string) string {
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:])
}

// Tolerance is how far a request's signature time may be from the
// receiver's clock.
const Tolerance = 5 * time.Minute

// Project states, as the edge acts on them.
const (
	StateActive    = "active"    // served
	StatePaused    = "paused"    // 503 and a wake
	StateSuspended = "suspended" // its organisation is suspended: refused
	StateDisabled  = "disabled"  // services off, deleted, or moved away: not found
)

// Key is a live API key, by its hash.
type Key struct {
	ID   uuid.UUID `json:"id"`
	Kind string    `json:"kind"` // publishable | secret
	Hash string    `json:"hash"` // hex SHA-256 of the key
}

// Settings are a project's gateway settings, with defaults filled in.
type Settings struct {
	// StatementTimeoutMs bounds each request's query.
	StatementTimeoutMs int `json:"statement_timeout_ms"`
	// RatePerIP and RatePerKey are requests per minute.
	RatePerIP  int `json:"rate_per_ip"`
	RatePerKey int `json:"rate_per_key"`
	// AllowSecretInBrowser lets a secret key be used from a page (a
	// request with an Origin header).
	AllowSecretInBrowser bool `json:"allow_secret_in_browser"`
	// MaxQueryCost refuses data API reads whose estimated cost (EXPLAIN)
	// is higher (V4 §3.6).
	MaxQueryCost float64 `json:"max_query_cost"`
}

// Project is one project's configuration on the edge.
type Project struct {
	Ref       string    `json:"ref"`
	ProjectID uuid.UUID `json:"project_id"`
	OrgID     uuid.UUID `json:"org_id"`
	Region    string    `json:"region"`
	State     string    `json:"state"`
	Version   int64     `json:"version"`
	Seq       int64     `json:"seq"`
	// Database is the pooler database name; the edge logs in as EdgeUser
	// and SETs ROLE to AnonRole, UserRole or ServiceRole per request.
	Database    string `json:"database,omitempty"`
	EdgeUser    string `json:"edge_user,omitempty"`
	Password    string `json:"password,omitempty"`
	PoolerHost  string `json:"pooler_host,omitempty"`
	PoolerPort  int    `json:"pooler_port,omitempty"`
	AnonRole    string `json:"anon_role,omitempty"`
	UserRole    string `json:"user_role,omitempty"`
	ServiceRole string `json:"service_role,omitempty"`
	// HookRole runs the project's Postgres auth hooks.
	HookRole    string   `json:"hook_role,omitempty"`
	Keys        []Key    `json:"keys,omitempty"`
	CORSOrigins []string `json:"cors_origins,omitempty"`
	// ExposedSchemas are the schemas the data API serves; PublicTables
	// ("schema.table") may be read by anon and user without row-level
	// security (V4 §3.6).
	ExposedSchemas []string `json:"exposed_schemas,omitempty"`
	PublicTables   []string `json:"public_tables,omitempty"`
	Settings       Settings `json:"settings"`
	// JWKs are the public keys user tokens are verified with (JWK JSON).
	JWKs []json.RawMessage `json:"jwks,omitempty"`
	// SigningKey is the active key access tokens are signed with (V4 §4.4).
	SigningKey *SigningKey `json:"signing_key,omitempty"`
	// Auth is the project's auth settings, defaults filled in.
	Auth AuthConfig `json:"auth"`
}

// SigningKey is a project's active signing key: its kid and PKCS#8 private
// key.
type SigningKey struct {
	Kid     string `json:"kid"`
	Private []byte `json:"private"`
}

// AuthConfig is a project's auth settings (V4 §4), as the edge applies
// them.
type AuthConfig struct {
	// SiteURL is where links go when a request names no redirect.
	SiteURL string `json:"site_url"`
	// RedirectURLs are the allowed redirect_to values: exact, or with
	// "*" (any characters but "/") and "**" (anything) when
	// AllowWildcardRedirects is on.
	RedirectURLs           []string `json:"redirect_urls"`
	AllowWildcardRedirects bool     `json:"allow_wildcard_redirects"`
	SignupEnabled          bool     `json:"signup_enabled"`
	// EmailConfirm requires a new address to be confirmed before the user
	// can sign in.
	EmailConfirm     bool `json:"email_confirm"`
	MagicLinkEnabled bool `json:"magic_link_enabled"`
	// PasswordMinLength and PasswordRequireMixed (letters and digits)
	// are the password rules.
	PasswordMinLength    int  `json:"password_min_length"`
	PasswordRequireMixed bool `json:"password_require_mixed"`
	// AccessTokenTTL is the access tokens' lifetime in seconds.
	AccessTokenTTL int `json:"access_token_ttl"`
	// SessionMaxSeconds and SessionInactivitySeconds end sessions (0:
	// never); SingleSession signs a user's other sessions out at sign-in.
	SessionMaxSeconds        int  `json:"session_max_seconds"`
	SessionInactivitySeconds int  `json:"session_inactivity_seconds"`
	SingleSession            bool `json:"single_session"`

	// Phone sign-in (V4 §4.1): the channels on (sms, whatsapp), whether a
	// phone sign-up needs its code, and the countries numbers may be in
	// (ISO codes, "*" for any).
	PhoneChannels  []string `json:"phone_channels,omitempty"`
	PhoneConfirm   bool     `json:"phone_confirm"`
	PhoneCountries []string `json:"phone_countries,omitempty"`
	// AnonymousEnabled allows sign-in without credentials.
	AnonymousEnabled bool `json:"anonymous_enabled"`
	// MFA is off, optional, required (every user, aal2 for the data API)
	// or claim (those whose app_metadata.mfa_required is true); MFAPhone
	// allows phone factors.
	MFA      string `json:"mfa"`
	MFAPhone bool   `json:"mfa_phone"`
	// OAuth are the providers turned on, with their credentials.
	OAuth map[string]OAuthClient `json:"oauth,omitempty"`
	// Hooks (V4 §4.7): Postgres functions ("schema.name") called as the
	// project's auth hook role, and whether webhooks wait for events.
	CustomClaimsHook string `json:"custom_claims_hook,omitempty"`
	BeforeSignupHook string `json:"before_signup_hook,omitempty"`
	BeforeSignupURL  bool   `json:"before_signup_url"`
	AfterSignupHook  bool   `json:"after_signup_hook"`
	AfterSigninHook  bool   `json:"after_signin_hook"`
	CaptchaSecret    string `json:"captcha_secret,omitempty"`
	CaptchaVerifyURL string `json:"captcha_verify_url,omitempty"`
	ManualLinking    bool   `json:"manual_linking"`
}

// OAuthClient is a project's OAuth app at a provider (V4 §4.1).
type OAuthClient struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	// Apple signs its client secret: the team, key id and private key.
	TeamID     string `json:"team_id,omitempty"`
	KeyID      string `json:"key_id,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

// HasChannel reports whether phone codes may go by ch.
func (a AuthConfig) HasChannel(ch string) bool {
	for _, c := range a.PhoneChannels {
		if c == ch {
			return true
		}
	}
	return false
}

// Auth email kinds.
const (
	EmailConfirmation = "confirmation"
	EmailMagicLink    = "magic_link"
	EmailRecovery     = "recovery"
	EmailInvite       = "invite"
	EmailChange       = "email_change"
)

// Message channels.
const (
	ChannelEmail    = "email"
	ChannelSMS      = "sms"
	ChannelWhatsApp = "whatsapp"
)

// Phone message kinds.
const (
	PhoneCode   = "phone_code"   // sign-up, sign-in, confirmation
	PhoneChange = "phone_change" // a new number
	PhoneMFA    = "phone_mfa"    // a second factor
)

// AuthMessage asks pgdock-server to send one auth message for a project:
// an email, or a code by SMS or WhatsApp.
type AuthMessage struct {
	Ref     string `json:"ref"`
	Channel string `json:"channel"` // email (default) | sms | whatsapp
	Kind    string `json:"kind"`
	To      string `json:"to"`
	// Code is the 6-digit code and Link the one-click link (both in the
	// message; templates use either).
	Code string `json:"code"`
	Link string `json:"link"`
}

// ActiveUser is a user who signed in or refreshed a token (monthly active
// users, V4 §12).
type ActiveUser struct {
	ProjectID uuid.UUID `json:"project_id"`
	UserID    uuid.UUID `json:"user_id"`
	At        time.Time `json:"at"`
}

// Config is a page of the feed: projects changed after the request's
// "since", in order. Next is the position to ask from next. With Full,
// the page is a snapshot from the start and More says another page
// follows.
type Config struct {
	Projects []Project `json:"projects"`
	Next     int64     `json:"next"`
	More     bool      `json:"more"`
}

// Usage is one project's counters for one hour.
type Usage struct {
	ProjectID   uuid.UUID `json:"project_id"`
	Hour        time.Time `json:"hour"`
	Requests    int64     `json:"requests"`
	EgressBytes int64     `json:"egress_bytes"`
}

// Log is one request.
type Log struct {
	ProjectID uuid.UUID  `json:"project_id"`
	At        time.Time  `json:"at"`
	RequestID string     `json:"request_id"`
	Method    string     `json:"method"`
	Path      string     `json:"path"`
	Status    int        `json:"status"`
	LatencyMs int        `json:"latency_ms"`
	Role      string     `json:"role,omitempty"`
	UserID    *uuid.UUID `json:"user_id,omitempty"`
	KeyID     *uuid.UUID `json:"key_id,omitempty"`
	IP        string     `json:"ip,omitempty"`
	BytesOut  int64      `json:"bytes_out"`
}

// Report is what an edge sends every few seconds. BatchID makes a retried
// report count once.
type Report struct {
	BatchID  string      `json:"batch_id"`
	Edge     string      `json:"edge"`
	At       time.Time   `json:"at"`
	Usage    []Usage     `json:"usage,omitempty"`
	Logs     []Log       `json:"logs,omitempty"`
	KeysUsed []uuid.UUID `json:"keys_used,omitempty"`
	// ActiveUsers is who used auth since the last report, once each.
	ActiveUsers []ActiveUser `json:"active_users,omitempty"`
}

// Wake asks for a paused project to be resumed.
type Wake struct {
	Ref string `json:"ref"`
}

// Client is pgdock-edge's side.
type Client struct {
	URL    string // pgdock-server's base URL
	Secret string
	HTTP   *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Signed is what the signature covers: method, path with query, body.
func Signed(method, requestURI string, body []byte) []byte {
	b := make([]byte, 0, len(method)+len(requestURI)+len(body)+2)
	b = append(b, method...)
	b = append(b, ' ')
	b = append(b, requestURI...)
	b = append(b, '\n')
	return append(b, body...)
}

// Verify checks a request's signature (the server's side).
func Verify(secret string, r *http.Request, body []byte, now time.Time) error {
	return signature.Verify(secret, r.Header.Get(signature.Header), Signed(r.Method, r.URL.RequestURI(), body), now, Tolerance)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(signature.Header, signature.Sign(c.Secret, time.Now(), Signed(method, req.URL.RequestURI(), body)))
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode/100 != 2 {
		return &StatusError{Status: res.StatusCode, Method: method, Path: path, Body: string(bytes.TrimSpace(raw))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Config asks for the feed from since, waiting up to wait for a change.
// Region limits it to one region's projects ("" for all).
func (c *Client) Config(ctx context.Context, region string, since int64, wait time.Duration) (Config, error) {
	q := url.Values{"since": {strconv.FormatInt(since, 10)}, "wait": {strconv.Itoa(int(wait / time.Second))}}
	if region != "" {
		q.Set("region", region)
	}
	var out Config
	return out, c.do(ctx, http.MethodGet, PathConfig+"?"+q.Encode(), nil, &out)
}

// Report sends usage and logs.
func (c *Client) Report(ctx context.Context, r Report) error {
	return c.do(ctx, http.MethodPost, PathReport, r, nil)
}

// StatusError is pgdock-server answering with a non-2xx status.
type StatusError struct {
	Status       int
	Method, Path string
	Body         string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("pgdock-server %s %s: %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// SendAuthMessage queues an auth email or code; a *StatusError with 429
// means a limit is reached (its body says which), 403 a number the
// project may not send to.
func (c *Client) SendAuthMessage(ctx context.Context, m AuthMessage) error {
	return c.do(ctx, http.MethodPost, PathAuthMessage, m, nil)
}

// AuthHook is an auth event for the project's webhooks. With Wait, it is
// the before-sign-up hook, answered with a decision.
type AuthHook struct {
	Ref     string         `json:"ref"`
	Event   string         `json:"event"` // before_signup | after_signup | after_signin
	Payload map[string]any `json:"payload"`
	Wait    bool           `json:"wait"`
}

// HookDecision is a before-sign-up hook's answer.
type HookDecision struct {
	Reject  bool   `json:"reject"`
	Message string `json:"message,omitempty"`
}

// SendAuthHook queues an event (or, with Wait, asks for a decision).
func (c *Client) SendAuthHook(ctx context.Context, h AuthHook) (HookDecision, error) {
	var d HookDecision
	if !h.Wait {
		return d, c.do(ctx, http.MethodPost, PathAuthHook, h, nil)
	}
	return d, c.do(ctx, http.MethodPost, PathAuthHook, h, &d)
}

// Wake asks for a project to be resumed.
func (c *Client) Wake(ctx context.Context, ref string) error {
	return c.do(ctx, http.MethodPost, PathWake, Wake{Ref: ref}, nil)
}
