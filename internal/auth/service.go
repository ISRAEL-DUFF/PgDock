package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/store"
)

// Errors returned to the API layer. Messages are safe to show.
var (
	ErrInvalidCredentials = errors.New("invalid email, password, or code")
	ErrLocked             = errors.New("too many failed attempts; try again later")
	ErrRateLimited        = errors.New("too many attempts from this address; slow down")
	ErrChallengeExpired   = errors.New("this sign-in has expired; start again")
	ErrNoSession          = errors.New("not signed in")
	ErrSetupDone          = errors.New("setup has already been completed")
	ErrBadSetupCode       = errors.New("the setup code is wrong (see the pgdock-server log)")
	ErrInvalidEmail       = errors.New("enter a valid email address")
)

// Config tunes the service.
type Config struct {
	// IdleTimeout ends sessions unused this long (spec: 12h).
	IdleTimeout time.Duration
	// MaxLifetime ends sessions this old regardless of use.
	MaxLifetime time.Duration
	// ReauthWindow is how long a step-up authentication stays valid.
	ReauthWindow time.Duration
	// LockoutThreshold failures lock an account for LockoutDuration.
	LockoutThreshold int
	LockoutDuration  time.Duration
	// ChallengeTTL bounds the gap between password and TOTP.
	ChallengeTTL time.Duration
	// Issuer labels the TOTP entry in authenticator apps.
	Issuer string
	// Now is the clock (tests).
	Now func() time.Time
}

func (c *Config) setDefaults() {
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 12 * time.Hour
	}
	if c.MaxLifetime <= 0 {
		c.MaxLifetime = 7 * 24 * time.Hour
	}
	if c.ReauthWindow <= 0 {
		c.ReauthWindow = 10 * time.Minute
	}
	if c.LockoutThreshold <= 0 {
		c.LockoutThreshold = 5
	}
	if c.LockoutDuration <= 0 {
		c.LockoutDuration = 15 * time.Minute
	}
	if c.ChallengeTTL <= 0 {
		c.ChallengeTTL = 5 * time.Minute
	}
	if c.Issuer == "" {
		c.Issuer = "PGDock"
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Service authenticates operators.
type Service struct {
	db        *pgxpool.Pool
	keyring   *crypto.Keyring
	cfg       Config
	log       *slog.Logger
	limiter   *Limiter
	setupCode string
}

// NewService returns a Service. setupCode guards the first-run wizard: it
// is printed to the server log, so only someone with access to the host can
// claim a freshly installed control plane.
func NewService(db *pgxpool.Pool, keyring *crypto.Keyring, cfg Config, setupCode string, log *slog.Logger) *Service {
	cfg.setDefaults()
	return &Service{db: db, keyring: keyring, cfg: cfg, log: log, limiter: NewLimiter(10, 5*time.Minute), setupCode: setupCode}
}

// Session is an authenticated browser session.
type Session struct {
	ID         string // hashed id, as stored
	OperatorID uuid.UUID
	Email      string
	Role       string
	ReauthAt   *time.Time
	CreatedAt  time.Time
}

// RecentlyReauthenticated reports whether a step-up auth is still valid.
func (s *Service) RecentlyReauthenticated(sess Session) bool {
	return sess.ReauthAt != nil && s.cfg.Now().Sub(*sess.ReauthAt) < s.cfg.ReauthWindow
}

// ReauthWindow returns how long a step-up auth lasts.
func (s *Service) ReauthWindow() time.Duration { return s.cfg.ReauthWindow }

// IdleTimeout returns the session idle timeout.
func (s *Service) IdleTimeout() time.Duration { return s.cfg.IdleTimeout }

// Allow applies the per-address rate limit to an auth attempt.
func (s *Service) Allow(ip string) error {
	if !s.limiter.Allow(ip) {
		return ErrRateLimited
	}
	return nil
}

func totpAAD(operatorID uuid.UUID) []byte {
	return []byte("operators.totp_secret:" + operatorID.String())
}

func normalizeEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || len(email) > 254 {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// ---- First-run setup -------------------------------------------------------

// SetupNeeded reports whether no operator exists yet.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	n, err := store.New(s.db).CountOperators(ctx)
	return n == 0, err
}

// Enrollment is returned by BeginSetup: the TOTP secret to enrol and a
// token that CompleteSetup exchanges, with a valid code, for the account.
type Enrollment struct {
	Token   string
	Secret  string
	URI     string
	Expires time.Time
}

type setupPayload struct {
	Email        string `json:"email"`
	PasswordHash string `json:"password_hash"`
	TOTPSecret   string `json:"totp_secret"`
}

// BeginSetup validates the owner's email and password and prepares TOTP
// enrolment. Nothing is created until CompleteSetup proves the authenticator
// works ("TOTP is enforced at first login").
func (s *Service) BeginSetup(ctx context.Context, setupCode, email, password string) (Enrollment, error) {
	if setupCode != s.setupCode {
		return Enrollment{}, ErrBadSetupCode
	}
	needed, err := s.SetupNeeded(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	if !needed {
		return Enrollment{}, ErrSetupDone
	}
	email, err = normalizeEmail(email)
	if err != nil {
		return Enrollment{}, err
	}
	if err := CheckPasswordPolicy(password); err != nil {
		return Enrollment{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Enrollment{}, err
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return Enrollment{}, err
	}
	token, digest, err := newToken()
	if err != nil {
		return Enrollment{}, err
	}
	payload, err := json.Marshal(setupPayload{Email: email, PasswordHash: hash, TOTPSecret: secret})
	if err != nil {
		return Enrollment{}, err
	}
	sealed, err := s.keyring.Encrypt(payload, []byte("auth_challenges.setup:"+digest))
	if err != nil {
		return Enrollment{}, err
	}
	expires := s.cfg.Now().Add(15 * time.Minute)
	if err := store.New(s.db).InsertChallenge(ctx, store.InsertChallengeParams{
		ID: digest, Kind: "setup", Payload: sealed, ExpiresAt: expires,
	}); err != nil {
		return Enrollment{}, err
	}
	return Enrollment{Token: token, Secret: secret, URI: TOTPURI(secret, email, s.cfg.Issuer), Expires: expires}, nil
}

// CompleteSetup creates the owner once code proves the authenticator is
// enrolled, and signs them in.
func (s *Service) CompleteSetup(ctx context.Context, token, code string, ip *netip.Addr, ua string) (string, Session, error) {
	digest := hashToken(token)
	var sessionToken string
	var sess Session
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		// Serialize setup so two browsers cannot both become the owner.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('pgdock_setup'))`); err != nil {
			return err
		}
		if n, err := q.CountOperators(ctx); err != nil {
			return err
		} else if n > 0 {
			return ErrSetupDone
		}
		ch, err := q.GetChallenge(ctx, store.GetChallengeParams{ID: digest, Kind: "setup"})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrChallengeExpired
		}
		if err != nil {
			return err
		}
		plain, err := s.keyring.Decrypt(ch.Payload, []byte("auth_challenges.setup:"+digest))
		if err != nil {
			return err
		}
		var p setupPayload
		if err := json.Unmarshal(plain, &p); err != nil {
			return err
		}
		step, ok := VerifyTOTP(p.TOTPSecret, code, s.cfg.Now())
		if !ok {
			return ErrInvalidCredentials
		}

		// The secret is re-sealed with the operator's ID as associated data.
		id := uuid.New()
		sealed, err := s.keyring.Encrypt([]byte(p.TOTPSecret), totpAAD(id))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operators (id, email, password_hash, totp_secret, role, totp_last_step)
			VALUES ($1, $2, $3, $4, 'owner', $5)`, id, p.Email, p.PasswordHash, sealed, step); err != nil {
			return err
		}
		if err := q.DeleteChallenge(ctx, digest); err != nil {
			return err
		}
		sessionToken, sess, err = s.newSession(ctx, q, id, ip, ua)
		if err != nil {
			return err
		}
		sess.Email, sess.Role = p.Email, "owner"
		return nil
	})
	if errors.Is(err, ErrInvalidCredentials) {
		// Count failures against the enrolment so it cannot be brute-forced.
		if n, berr := store.New(s.db).BumpChallengeAttempts(ctx, digest); berr == nil && n >= 5 {
			_ = store.New(s.db).DeleteChallenge(ctx, digest)
			return "", Session{}, ErrChallengeExpired
		}
	}
	return sessionToken, sess, err
}

// ---- Login -----------------------------------------------------------------

// Login checks email and password and returns a short-lived challenge token
// for the TOTP step. Unknown emails take the same time as wrong passwords.
func (s *Service) Login(ctx context.Context, email, password string) (string, uuid.UUID, error) {
	q := store.New(s.db)
	op, err := q.GetOperatorByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, pgx.ErrNoRows) {
		_, _ = VerifyPassword(password, dummyHash)
		return "", uuid.Nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", uuid.Nil, err
	}
	if op.DisabledAt != nil {
		_, _ = VerifyPassword(password, dummyHash)
		return "", op.ID, ErrInvalidCredentials
	}
	if op.LockedUntil != nil && op.LockedUntil.After(s.cfg.Now()) {
		return "", op.ID, ErrLocked
	}
	ok, err := VerifyPassword(password, op.PasswordHash)
	if err != nil {
		return "", op.ID, err
	}
	if !ok {
		return "", op.ID, s.recordFailure(ctx, op.ID)
	}
	token, digest, err := newToken()
	if err != nil {
		return "", op.ID, err
	}
	if err := q.InsertChallenge(ctx, store.InsertChallengeParams{
		ID: digest, Kind: "login", OperatorID: &op.ID, ExpiresAt: s.cfg.Now().Add(s.cfg.ChallengeTTL),
	}); err != nil {
		return "", op.ID, err
	}
	return token, op.ID, nil
}

// CompleteLogin checks the TOTP code for a challenge and opens a session.
func (s *Service) CompleteLogin(ctx context.Context, challenge, code string, ip *netip.Addr, ua string) (string, Session, error) {
	q := store.New(s.db)
	digest := hashToken(challenge)
	ch, err := q.GetChallenge(ctx, store.GetChallengeParams{ID: digest, Kind: "login"})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && ch.OperatorID == nil) {
		return "", Session{}, ErrChallengeExpired
	}
	if err != nil {
		return "", Session{}, err
	}
	op, err := q.GetOperator(ctx, *ch.OperatorID)
	if err != nil {
		return "", Session{}, err
	}
	if op.LockedUntil != nil && op.LockedUntil.After(s.cfg.Now()) {
		return "", Session{}, ErrLocked
	}
	if err := s.checkTOTP(ctx, op, code); err != nil {
		if n, berr := q.BumpChallengeAttempts(ctx, digest); berr == nil && n >= 3 {
			_ = q.DeleteChallenge(ctx, digest)
		}
		return "", Session{}, err
	}
	if err := q.DeleteChallenge(ctx, digest); err != nil {
		return "", Session{}, err
	}
	if err := q.ResetLoginFailures(ctx, op.ID); err != nil {
		return "", Session{}, err
	}
	token, sess, err := s.newSession(ctx, q, op.ID, ip, ua)
	sess.Email, sess.Role = op.Email, op.Role
	return token, sess, err
}

// checkTOTP verifies a code and consumes its time step.
func (s *Service) checkTOTP(ctx context.Context, op store.Operator, code string) error {
	if len(op.TotpSecret) == 0 {
		return ErrInvalidCredentials
	}
	secret, err := s.keyring.Decrypt(op.TotpSecret, totpAAD(op.ID))
	if err != nil {
		return fmt.Errorf("decrypt TOTP secret: %w", err)
	}
	step, ok := VerifyTOTP(string(secret), code, s.cfg.Now())
	if !ok {
		return s.recordFailure(ctx, op.ID)
	}
	n, err := store.New(s.db).AdvanceTOTPStep(ctx, store.AdvanceTOTPStepParams{ID: op.ID, Step: step})
	if err != nil {
		return err
	}
	if n == 0 { // replayed code
		return s.recordFailure(ctx, op.ID)
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, id uuid.UUID) error {
	row, err := store.New(s.db).RecordLoginFailure(ctx, store.RecordLoginFailureParams{
		ID: id, Threshold: int32(s.cfg.LockoutThreshold), LockSeconds: int32(s.cfg.LockoutDuration.Seconds()),
	})
	if err != nil {
		return err
	}
	if row.LockedUntil != nil && row.LockedUntil.After(s.cfg.Now()) {
		s.log.Warn("operator locked out after repeated failures", "operator_id", id, "until", row.LockedUntil)
		return ErrLocked
	}
	return ErrInvalidCredentials
}

// ---- Sessions --------------------------------------------------------------

func (s *Service) newSession(ctx context.Context, q *store.Queries, operatorID uuid.UUID, ip *netip.Addr, ua string) (string, Session, error) {
	token, digest, err := newToken()
	if err != nil {
		return "", Session{}, err
	}
	if len(ua) > 512 {
		ua = ua[:512]
	}
	var uaPtr *string
	if ua != "" {
		uaPtr = &ua
	}
	now := s.cfg.Now()
	if err := q.InsertSession(ctx, store.InsertSessionParams{ID: digest, OperatorID: operatorID, Ip: ip, UserAgent: uaPtr, Now: now}); err != nil {
		return "", Session{}, err
	}
	return token, Session{ID: digest, OperatorID: operatorID, ReauthAt: &now, CreatedAt: now}, nil
}

// Authenticate resolves a session token, enforcing the idle timeout and
// maximum lifetime, and refreshes last-seen (at most once a minute).
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	q := store.New(s.db)
	digest := hashToken(token)
	row, err := q.GetSession(ctx, digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	now := s.cfg.Now()
	if row.DisabledAt != nil || now.Sub(row.LastSeenAt) > s.cfg.IdleTimeout || now.Sub(row.CreatedAt) > s.cfg.MaxLifetime {
		_ = q.DeleteSession(ctx, digest)
		return Session{}, ErrNoSession
	}
	if now.Sub(row.LastSeenAt) > time.Minute {
		if err := q.TouchSession(ctx, store.TouchSessionParams{ID: digest, Now: now}); err != nil {
			return Session{}, err
		}
	}
	return Session{
		ID: digest, OperatorID: row.OperatorID, Email: row.Email, Role: row.Role,
		ReauthAt: row.ReauthAt, CreatedAt: row.CreatedAt,
	}, nil
}

// Logout ends a session.
func (s *Service) Logout(ctx context.Context, sess Session) error {
	return store.New(s.db).DeleteSession(ctx, sess.ID)
}

// Reauthenticate performs step-up auth (password + TOTP) for destructive
// actions (spec §7.2).
func (s *Service) Reauthenticate(ctx context.Context, sess Session, password, code string) error {
	q := store.New(s.db)
	op, err := q.GetOperator(ctx, sess.OperatorID)
	if err != nil {
		return err
	}
	if op.LockedUntil != nil && op.LockedUntil.After(s.cfg.Now()) {
		return ErrLocked
	}
	ok, err := VerifyPassword(password, op.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return s.recordFailure(ctx, op.ID)
	}
	if err := s.checkTOTP(ctx, op, code); err != nil {
		return err
	}
	if err := q.ResetLoginFailures(ctx, op.ID); err != nil {
		return err
	}
	return q.SetSessionReauth(ctx, store.SetSessionReauthParams{ID: sess.ID, Now: s.cfg.Now()})
}

// Sweep deletes expired sessions and challenges. Run it periodically.
func (s *Service) Sweep(ctx context.Context) error {
	q := store.New(s.db)
	now := s.cfg.Now()
	if err := q.DeleteIdleSessions(ctx, store.DeleteIdleSessionsParams{
		IdleBefore: now.Add(-s.cfg.IdleTimeout), CreatedBefore: now.Add(-s.cfg.MaxLifetime),
	}); err != nil {
		return err
	}
	return q.DeleteExpiredChallenges(ctx)
}
