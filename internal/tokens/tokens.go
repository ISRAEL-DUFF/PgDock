// Package tokens issues and checks API tokens for the CLI, CI and scripts
// (V2 §7.2), and runs the device login that issues them to the CLI.
//
// A token is "pgd_" followed by 32 random bytes in base62. It is shown
// once; the database keeps its SHA-256 (tokens are high-entropy, so a slow
// hash adds nothing). Each token acts in exactly one organisation, with
// additive scopes, an optional project restriction, and a required expiry.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
)

// Scopes (V2 §7.2). They are additive: write includes read, and admin
// requires write.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

// Prefix starts every token, so secret scanners can find leaked ones.
const Prefix = "pgd_"

const (
	// DefaultExpiry is a new token's lifetime when none is given.
	DefaultExpiry = 90 * 24 * time.Hour
	// MaxExpiry is the longest a token may live; the platform admin may
	// lower it (settings key tokens.max_days).
	MaxExpiry = 365 * 24 * time.Hour
	// ExpiryWarning is how long before expiry the owner is emailed.
	ExpiryWarning = 7 * 24 * time.Hour

	settingsKey = "tokens.settings"
	displayLen  = len(Prefix) + 6
)

// Errors.
var (
	ErrInvalid = errors.New("invalid token request")
	// ErrUnauthenticated: the token is unknown, revoked, expired, or its
	// user can no longer sign in.
	ErrUnauthenticated = errors.New("the API token is not valid")
	// ErrSuspended: the token's organisation is suspended (V2 §10.8).
	ErrSuspended = errors.New("the organisation is suspended; its tokens are disabled")
	ErrNotFound  = errors.New("not found")
)

// Config tunes the service.
type Config struct {
	Now       func() time.Time
	PublicURL string
}

// Service manages tokens and device logins.
type Service struct {
	db      *pgxpool.Pool
	keyring *crypto.Keyring
	mail    *mail.Service
	cfg     Config
	log     *slog.Logger
}

// New returns a Service. mail may be nil (no emails are sent).
func New(db *pgxpool.Pool, keyring *crypto.Keyring, m *mail.Service, cfg Config, log *slog.Logger) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{db: db, keyring: keyring, mail: m, cfg: cfg, log: log}
}

// Token is an authenticated API token.
type Token struct {
	ID     uuid.UUID
	UserID uuid.UUID
	OrgID  uuid.UUID
	Name   string
	Scopes []string
	// Projects restricts the token to these projects; nil means every
	// project its user can access in the organisation.
	Projects  []uuid.UUID
	ExpiresAt time.Time

	Email        string
	UserName     string
	PlatformRole string
}

// Has reports whether the token carries scope (write includes read; admin
// includes both).
func (t Token) Has(scope string) bool { return HasScope(t.Scopes, scope) }

// HasScope reports whether scopes grant scope.
func HasScope(scopes []string, scope string) bool {
	rank := map[string]int{ScopeRead: 1, ScopeWrite: 2, ScopeAdmin: 3}
	for _, s := range scopes {
		if rank[s] >= rank[scope] {
			return true
		}
	}
	return false
}

// AllowsProject reports whether the token may act on project id.
func (t Token) AllowsProject(id uuid.UUID) bool {
	return t.Projects == nil || slices.Contains(t.Projects, id)
}

// NormalizeScopes checks a requested scope set and returns it in order
// with its implied scopes (admin requires write; write includes read).
func NormalizeScopes(in []string) ([]string, error) {
	set := map[string]bool{}
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case ScopeRead, ScopeWrite, ScopeAdmin:
			set[s] = true
		case "":
		default:
			return nil, fmt.Errorf("%w: unknown scope %q (use read, write, admin)", ErrInvalid, s)
		}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("%w: choose at least one scope", ErrInvalid)
	}
	if set[ScopeAdmin] && !set[ScopeWrite] {
		return nil, fmt.Errorf("%w: the admin scope requires write", ErrInvalid)
	}
	out := []string{ScopeRead}
	for _, s := range []string{ScopeWrite, ScopeAdmin} {
		if set[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// Settings are the platform-wide token rules.
type Settings struct {
	// MaxDays is the longest expiry the platform allows (1–365).
	MaxDays int `json:"max_days"`
}

// GetSettings returns the platform's token settings.
func (s *Service) GetSettings(ctx context.Context) (Settings, error) {
	st := Settings{MaxDays: int(MaxExpiry / (24 * time.Hour))}
	raw, err := store.New(s.db).GetSetting(ctx, settingsKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, err
	}
	return st, nil
}

// PutSettings stores the platform's token settings.
func (s *Service) PutSettings(ctx context.Context, st Settings) error {
	if st.MaxDays < 1 || st.MaxDays > int(MaxExpiry/(24*time.Hour)) {
		return fmt.Errorf("%w: the maximum expiry is 1 to 365 days", ErrInvalid)
	}
	raw, _ := json.Marshal(st)
	return store.New(s.db).PutSetting(ctx, store.PutSettingParams{Key: settingsKey, Value: raw})
}

// CreateParams describes a new token. The caller has checked that the
// user may act in OrgID and on every one of Projects.
type CreateParams struct {
	UserID   uuid.UUID
	OrgID    uuid.UUID
	Name     string
	Scopes   []string
	Projects []uuid.UUID // nil: unrestricted
	// ExpiresIn defaults to DefaultExpiry (capped by the platform maximum).
	ExpiresIn time.Duration
	Via       string // ui, device, api
}

// Created is a new token: Secret is shown once.
type Created struct {
	Secret string
	Row    store.ApiToken
}

// Create issues a token and emails its owner.
func (s *Service) Create(ctx context.Context, p CreateParams) (Created, error) {
	return s.create(ctx, store.New(s.db), p)
}

func (s *Service) create(ctx context.Context, q *store.Queries, p CreateParams) (Created, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 100 {
		return Created{}, fmt.Errorf("%w: give the token a name of up to 100 characters", ErrInvalid)
	}
	scopes, err := NormalizeScopes(p.Scopes)
	if err != nil {
		return Created{}, err
	}
	st, err := s.GetSettings(ctx)
	if err != nil {
		return Created{}, err
	}
	maxExp := time.Duration(st.MaxDays) * 24 * time.Hour
	exp := p.ExpiresIn
	if exp == 0 {
		exp = min(DefaultExpiry, maxExp)
	}
	if exp < time.Hour || exp > maxExp {
		return Created{}, fmt.Errorf("%w: a token must expire within 1 hour to %d days", ErrInvalid, st.MaxDays)
	}
	if p.Projects != nil && len(p.Projects) == 0 {
		return Created{}, fmt.Errorf("%w: a project restriction needs at least one project", ErrInvalid)
	}
	via := p.Via
	if via == "" {
		via = "ui"
	}
	secret, err := newSecret()
	if err != nil {
		return Created{}, err
	}
	row, err := q.InsertAPIToken(ctx, store.InsertAPITokenParams{
		UserID: p.UserID, OrgID: p.OrgID, Name: name, TokenHash: Hash(secret), Prefix: secret[:displayLen],
		Scopes: scopes, ProjectIds: p.Projects, ExpiresAt: s.cfg.Now().Add(exp), CreatedVia: via,
	})
	if err != nil {
		return Created{}, err
	}
	s.notifyCreated(ctx, q, row)
	return Created{Secret: secret, Row: row}, nil
}

// Authenticate resolves a bearer token. It records the last use (at most
// once a minute).
func (s *Service) Authenticate(ctx context.Context, secret string, ip *netip.Addr) (Token, error) {
	if !strings.HasPrefix(secret, Prefix) || len(secret) < displayLen+10 || len(secret) > 100 {
		return Token{}, ErrUnauthenticated
	}
	q := store.New(s.db)
	row, err := q.GetTokenForAuth(ctx, Hash(secret))
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, ErrUnauthenticated
	}
	if err != nil {
		return Token{}, err
	}
	now := s.cfg.Now()
	if row.RevokedAt != nil || !now.Before(row.ExpiresAt) || row.DisabledAt != nil || row.ApprovedAt == nil || row.EmailVerifiedAt == nil {
		return Token{}, ErrUnauthenticated
	}
	switch row.OrgStatus {
	case "suspended":
		return Token{}, ErrSuspended
	case "deleted":
		return Token{}, ErrUnauthenticated
	}
	if row.LastUsedAt == nil || now.Sub(*row.LastUsedAt) > time.Minute {
		if err := q.TouchAPIToken(ctx, store.TouchAPITokenParams{ID: row.ID, OrgID: row.OrgID, Now: now, StaleBefore: now.Add(-time.Minute), Ip: ip}); err != nil {
			return Token{}, err
		}
	}
	t := Token{
		ID: row.ID, UserID: row.UserID, OrgID: row.OrgID, Name: row.Name, Scopes: row.Scopes, Projects: row.ProjectIds,
		ExpiresAt: row.ExpiresAt, Email: row.Email, PlatformRole: row.PlatformRole,
	}
	if row.UserName != nil {
		t.UserName = *row.UserName
	}
	return t, nil
}

// RevokeOwn revokes one of userID's tokens.
func (s *Service) RevokeOwn(ctx context.Context, userID, id uuid.UUID) error {
	q := store.New(s.db)
	t, err := q.GetUserToken(ctx, store.GetUserTokenParams{ID: id, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = q.RevokeToken(ctx, store.RevokeTokenParams{ID: t.ID, OrgID: t.OrgID, RevokedBy: &userID})
	return err
}

// RevokeInOrg revokes a token scoped to orgID (org owners and admins).
func (s *Service) RevokeInOrg(ctx context.Context, orgID, id, by uuid.UUID) error {
	q := store.New(s.db)
	if _, err := q.GetOrgToken(ctx, store.GetOrgTokenParams{ID: id, OrgID: orgID}); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	_, err := q.RevokeToken(ctx, store.RevokeTokenParams{ID: id, OrgID: orgID, RevokedBy: &by})
	return err
}

// Hash is the stored digest of a token.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// newSecret returns "pgd_" + 32 random bytes in base62 (43 characters).
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := new(big.Int).SetBytes(b)
	base := big.NewInt(62)
	var out []byte
	mod := new(big.Int)
	for range 43 {
		n.DivMod(n, base, mod)
		out = append(out, base62[mod.Int64()])
	}
	return Prefix + string(out), nil
}

// ---- Email ------------------------------------------------------------------

func (s *Service) send(ctx context.Context, to, subject, body string) {
	if s.mail == nil || to == "" {
		return
	}
	if err := s.mail.Send(ctx, mail.Message{To: []string{to}, Subject: subject, Body: body}); err != nil && !errors.Is(err, mail.ErrNotConfigured) {
		s.log.Warn("send token email", "subject", subject, "err", err)
	}
}

func (s *Service) link(path string) string { return strings.TrimRight(s.cfg.PublicURL, "/") + path }

func (s *Service) notifyCreated(ctx context.Context, q *store.Queries, t store.ApiToken) {
	u, err := q.GetUser(ctx, t.UserID)
	if err != nil {
		return
	}
	s.send(ctx, u.Email, "New PGDock API token: "+t.Name,
		fmt.Sprintf("An API token named %q (%s…) was created for your account, with scopes %s. It expires %s.\n\n"+
			"If this wasn't you, revoke it now: %s\n",
			t.Name, t.Prefix, strings.Join(t.Scopes, ", "), t.ExpiresAt.UTC().Format("2 January 2006"), s.link("/account#tokens")))
}

// Sweep emails owners of tokens expiring within a week and clears expired
// device logins.
func (s *Service) Sweep(ctx context.Context) error {
	q := store.New(s.db)
	now := s.cfg.Now()
	rows, err := q.TokensExpiringSoon(ctx, store.TokensExpiringSoonParams{Now: now, Before: now.Add(ExpiryWarning)})
	if err != nil {
		return err
	}
	for _, t := range rows {
		s.send(ctx, t.Email, "Your PGDock API token expires soon: "+t.Name,
			fmt.Sprintf("Your API token %q (%s…) for %s expires %s. Create a new one before then: %s\n",
				t.Name, t.Prefix, t.OrgName, t.ExpiresAt.UTC().Format("2 January 2006 15:04 MST"), s.link("/account#tokens")))
		if err := q.MarkTokenExpiryNotified(ctx, store.MarkTokenExpiryNotifiedParams{ID: t.ID, OrgID: t.OrgID}); err != nil {
			return err
		}
	}
	_, err = q.DeleteExpiredDeviceRequests(ctx)
	return err
}

// Run sweeps every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("token sweep", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
