package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	pgmail "github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
)

// Account email lifetimes (V2 §3.1, §3.6).
const (
	VerifyTTL = 48 * time.Hour
	ResetTTL  = time.Hour
)

// ---- Recovery codes ----------------------------------------------------------

// RecoveryCodeCount is how many single-use codes an account gets (V2 §3.1).
const RecoveryCodeCount = 10

var codeEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func recoveryAAD(userID uuid.UUID) []byte { return []byte("users.recovery_codes:" + userID.String()) }

// normalizeRecoveryCode lower-cases a code and drops separators.
func normalizeRecoveryCode(c string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

// looksLikeRecoveryCode tells a recovery code (10 characters, usually
// "xxxxx-xxxxx") from a 6-digit TOTP code.
func looksLikeRecoveryCode(c string) bool { return len(normalizeRecoveryCode(c)) == 10 }

func hashRecoveryCode(c string) string {
	sum := sha256.Sum256([]byte("pgdock-recovery:" + normalizeRecoveryCode(c)))
	return hex.EncodeToString(sum[:])
}

// newRecoveryCodes returns fresh codes and the sealed blob of their hashes.
func (s *Service) newRecoveryCodes(userID uuid.UUID) ([]string, []byte, error) {
	codes := make([]string, RecoveryCodeCount)
	hashes := make([]string, RecoveryCodeCount)
	for i := range codes {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		raw := strings.ToLower(codeEncoding.EncodeToString(b))[:10]
		codes[i] = raw[:5] + "-" + raw[5:]
		hashes[i] = hashRecoveryCode(raw)
	}
	blob, err := s.sealCodes(userID, hashes)
	return codes, blob, err
}

func (s *Service) sealCodes(userID uuid.UUID, hashes []string) ([]byte, error) {
	b, err := json.Marshal(hashes)
	if err != nil {
		return nil, err
	}
	return s.keyring.Encrypt(b, recoveryAAD(userID))
}

func (s *Service) openCodes(u store.User) ([]string, error) {
	if len(u.RecoveryCodes) == 0 {
		return nil, nil
	}
	b, err := s.keyring.Decrypt(u.RecoveryCodes, recoveryAAD(u.ID))
	if err != nil {
		return nil, fmt.Errorf("recovery codes: %w", err)
	}
	var hashes []string
	return hashes, json.Unmarshal(b, &hashes)
}

// useRecoveryCode consumes code if it is one of u's unused codes.
func (s *Service) useRecoveryCode(ctx context.Context, u store.User, code string) (bool, error) {
	var used bool
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, u.ID); err != nil {
			return err
		}
		fresh, err := q.GetUser(ctx, u.ID)
		if err != nil {
			return err
		}
		hashes, err := s.openCodes(fresh)
		if err != nil {
			return err
		}
		want := hashRecoveryCode(code)
		for i, h := range hashes {
			if subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1 {
				hashes = append(hashes[:i], hashes[i+1:]...)
				blob, err := s.sealCodes(u.ID, hashes)
				if err != nil {
					return err
				}
				used = true
				return q.SetRecoveryCodes(ctx, store.SetRecoveryCodesParams{ID: u.ID, RecoveryCodes: blob})
			}
		}
		return nil
	})
	if used {
		s.log.Info("recovery code used", "user_id", u.ID)
	}
	return used, err
}

// RecoveryCodesLeft returns how many unused recovery codes a user has.
func (s *Service) RecoveryCodesLeft(ctx context.Context, userID uuid.UUID) (int, error) {
	u, err := store.New(s.db).GetUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	hashes, err := s.openCodes(u)
	return len(hashes), err
}

// RegenerateRecoveryCodes replaces a user's codes and returns the new ones.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID uuid.UUID) ([]string, error) {
	codes, blob, err := s.newRecoveryCodes(userID)
	if err != nil {
		return nil, err
	}
	return codes, store.New(s.db).SetRecoveryCodes(ctx, store.SetRecoveryCodesParams{ID: userID, RecoveryCodes: blob})
}

// ---- Signup -------------------------------------------------------------------

// Signup modes (V2 §3.1).
const (
	SignupInviteOnly = "invite_only"
	SignupApproval   = "approval"
	SignupOpen       = "open"
)

const signupSettingsKey = "signup"

// SignupPolicy is the platform's signup setting.
type SignupPolicy struct {
	Mode    string   `json:"mode"`
	Domains []string `json:"domains,omitempty"` // allow-list; empty = any
}

// SignupPolicy returns the current policy (invite-only by default).
func (s *Service) SignupPolicy(ctx context.Context) (SignupPolicy, error) {
	p := SignupPolicy{Mode: SignupInviteOnly}
	raw, err := store.New(s.db).GetSetting(ctx, signupSettingsKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, err
	}
	return p, nil
}

// SetSignupPolicy validates and stores p.
func (s *Service) SetSignupPolicy(ctx context.Context, p SignupPolicy) (SignupPolicy, error) {
	switch p.Mode {
	case SignupInviteOnly, SignupApproval, SignupOpen:
	default:
		return p, fmt.Errorf("signup mode must be %s, %s, or %s", SignupInviteOnly, SignupApproval, SignupOpen)
	}
	clean := p.Domains[:0]
	for _, d := range p.Domains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
		if d == "" {
			continue
		}
		if strings.ContainsAny(d, " @/") || !strings.Contains(d, ".") {
			return p, fmt.Errorf("%q is not a domain", d)
		}
		clean = append(clean, d)
	}
	p.Domains = clean
	b, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	return p, store.New(s.db).PutSetting(ctx, store.PutSettingParams{Key: signupSettingsKey, Value: b})
}

func (p SignupPolicy) allows(email string) bool {
	if len(p.Domains) == 0 {
		return true
	}
	domain := strings.ToLower(email[strings.LastIndex(email, "@")+1:])
	for _, d := range p.Domains {
		if domain == d {
			return true
		}
	}
	return false
}

// SignupParams is a self-service signup.
type SignupParams struct {
	Email, Password, Name string
	// TermsVersion is the terms version the user accepted; it must be the
	// current one.
	TermsVersion int
	IP           *netip.Addr
}

// Signup creates an unverified account (inactive until approved, in
// approval mode) and emails a verification link. An address that already
// has an account gets a notice by email instead, so the response does not
// reveal which addresses are registered.
func (s *Service) Signup(ctx context.Context, p SignupParams) error {
	policy, err := s.SignupPolicy(ctx)
	if err != nil {
		return err
	}
	if policy.Mode == SignupInviteOnly {
		return ErrSignupClosed
	}
	email, err := NormalizeEmail(p.Email)
	if err != nil {
		return err
	}
	if !policy.allows(email) {
		return ErrDomainNotAllowed
	}
	if err := CheckPasswordPolicy(p.Password); err != nil {
		return err
	}
	name := strings.TrimSpace(p.Name)
	if len(name) > 100 {
		return errors.New("name must be at most 100 characters")
	}
	if err := s.checkTermsVersion(ctx, p.TermsVersion); err != nil {
		return err
	}
	q := store.New(s.db)
	if existing, err := q.GetUserByEmail(ctx, email); err == nil {
		return s.send(ctx, existing.Email, "Your PGDock account",
			"Someone (hopefully you) tried to sign up to PGDock with this address, which already has an account.\n\n"+
				"Sign in at "+s.cfg.PublicURL+"/login, or reset your password at "+s.cfg.PublicURL+"/reset-password.\n\n"+
				"If this wasn't you, you can ignore this message.")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	hash, err := HashPassword(p.Password)
	if err != nil {
		return err
	}
	var u store.User
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		qt := store.New(tx)
		var approved *time.Time
		if policy.Mode == SignupOpen {
			now := s.cfg.Now()
			approved = &now
		}
		var namePtr *string
		if name != "" {
			namePtr = &name
		}
		var err error
		u, err = qt.InsertUser(ctx, store.InsertUserParams{
			Email: email, PasswordHash: hash, Name: namePtr, PlatformRole: RoleUser, ApprovedAt: approved,
		})
		if err != nil {
			return err
		}
		if err := s.userCreated(ctx, tx, u); err != nil {
			return err
		}
		return qt.AcceptTerms(ctx, store.AcceptTermsParams{UserID: u.ID, Version: int32(p.TermsVersion), Ip: p.IP})
	})
	if err != nil {
		return err
	}
	return s.SendVerification(ctx, u)
}

// CreateVerifiedUser creates an account whose address is already proven
// (an accepted invitation sent to it), inside tx. It has no authenticator
// yet: the first sign-in enrols one.
func (s *Service) CreateVerifiedUser(ctx context.Context, tx pgx.Tx, email, password, name string, termsVersion int, ip *netip.Addr) (store.User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return store.User{}, err
	}
	if err := CheckPasswordPolicy(password); err != nil {
		return store.User{}, err
	}
	if err := s.checkTermsVersion(ctx, termsVersion); err != nil {
		return store.User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return store.User{}, err
	}
	name = strings.TrimSpace(name)
	var namePtr *string
	if name != "" {
		namePtr = &name
	}
	now := s.cfg.Now()
	q := store.New(tx)
	u, err := q.InsertUser(ctx, store.InsertUserParams{
		Email: email, PasswordHash: hash, Name: namePtr, PlatformRole: RoleUser, EmailVerifiedAt: &now, ApprovedAt: &now,
	})
	if err != nil {
		return store.User{}, err
	}
	if err := s.userCreated(ctx, tx, u); err != nil {
		return store.User{}, err
	}
	return u, q.AcceptTerms(ctx, store.AcceptTermsParams{UserID: u.ID, Version: int32(termsVersion), Ip: ip})
}

func (s *Service) send(ctx context.Context, to, subject, body string) error {
	if s.mailer == nil {
		return pgmail.ErrNotConfigured
	}
	return s.mailer.Send(ctx, pgmail.Message{To: []string{to}, Subject: subject, Body: body})
}

// ---- Email verification and password reset -------------------------------

func (s *Service) emailToken(ctx context.Context, userID uuid.UUID, purpose string, ttl time.Duration) (string, error) {
	token, digest, err := newToken()
	if err != nil {
		return "", err
	}
	q := store.New(s.db)
	if err := q.InvalidateEmailTokens(ctx, store.InvalidateEmailTokensParams{UserID: userID, Purpose: purpose}); err != nil {
		return "", err
	}
	return token, q.InsertEmailToken(ctx, store.InsertEmailTokenParams{
		TokenHash: digest, UserID: userID, Purpose: purpose, ExpiresAt: s.cfg.Now().Add(ttl),
	})
}

// SendVerification emails u a link that verifies their address.
func (s *Service) SendVerification(ctx context.Context, u store.User) error {
	token, err := s.emailToken(ctx, u.ID, "verify", VerifyTTL)
	if err != nil {
		return err
	}
	return s.send(ctx, u.Email, "Confirm your email for PGDock",
		"Confirm this address to finish creating your PGDock account:\n\n"+
			s.cfg.PublicURL+"/verify-email?token="+token+"\n\n"+
			"The link works for 48 hours. If you didn't sign up, you can ignore this message.")
}

// ResendVerification sends a new link to an unverified address; it says
// nothing about whether the address has an account.
func (s *Service) ResendVerification(ctx context.Context, email string) error {
	u, err := store.New(s.db).GetUserByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.EmailVerifiedAt != nil || u.DisabledAt != nil {
		return nil
	}
	return s.SendVerification(ctx, u)
}

// VerifyEmail consumes a verification link and returns its user.
func (s *Service) VerifyEmail(ctx context.Context, token string) (store.User, error) {
	q := store.New(s.db)
	id, err := q.ConsumeEmailToken(ctx, store.ConsumeEmailTokenParams{TokenHash: hashToken(token), Purpose: "verify"})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.User{}, ErrTokenInvalid
	}
	if err != nil {
		return store.User{}, err
	}
	if err := q.MarkEmailVerified(ctx, id); err != nil {
		return store.User{}, err
	}
	return q.GetUser(ctx, id)
}

// RequestPasswordReset emails a reset link (valid one hour) if the address
// has an account; the caller learns nothing either way.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	u, err := store.New(s.db).GetUserByEmail(ctx, strings.TrimSpace(email))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.DisabledAt != nil {
		return nil
	}
	token, err := s.emailToken(ctx, u.ID, "reset", ResetTTL)
	if err != nil {
		return err
	}
	return s.send(ctx, u.Email, "Reset your PGDock password",
		"Choose a new password for your PGDock account:\n\n"+
			s.cfg.PublicURL+"/reset-password?token="+token+"\n\n"+
			"The link works for one hour, once. Resetting signs you out everywhere.\n"+
			"If you didn't ask for this, you can ignore this message.")
}

// ResetPassword sets a new password from a reset link and ends every
// session of the account. A reset also proves the address.
func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	if err := CheckPasswordPolicy(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		id, err := q.ConsumeEmailToken(ctx, store.ConsumeEmailTokenParams{TokenHash: hashToken(token), Purpose: "reset"})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: id, PasswordHash: hash}); err != nil {
			return err
		}
		if err := q.MarkEmailVerified(ctx, id); err != nil {
			return err
		}
		return q.DeleteUserSessions(ctx, store.DeleteUserSessionsParams{UserID: id})
	})
}

// ChangePassword sets a new password after checking the current one, and
// ends the user's other sessions.
func (s *Service) ChangePassword(ctx context.Context, sess Session, current, next string) error {
	q := store.New(s.db)
	u, err := q.GetUser(ctx, sess.UserID)
	if err != nil {
		return err
	}
	ok, err := VerifyPassword(current, u.PasswordHash)
	if err != nil {
		return err
	}
	if !ok {
		return s.recordFailure(ctx, u.ID)
	}
	if err := CheckPasswordPolicy(next); err != nil {
		return err
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := q.SetUserPassword(ctx, store.SetUserPasswordParams{ID: u.ID, PasswordHash: hash}); err != nil {
		return err
	}
	return q.DeleteUserSessions(ctx, store.DeleteUserSessionsParams{UserID: u.ID, Keep: &sess.ID})
}

// ---- Terms -----------------------------------------------------------------------

// CurrentTerms returns the latest published terms (version 0 when none).
func (s *Service) CurrentTerms(ctx context.Context) (store.TermsVersion, error) {
	t, err := store.New(s.db).LatestTerms(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.TermsVersion{}, nil
	}
	return t, err
}

func (s *Service) checkTermsVersion(ctx context.Context, v int) error {
	cur, err := s.CurrentTerms(ctx)
	if err != nil {
		return err
	}
	if cur.Version != 0 && int32(v) != cur.Version {
		return ErrTermsNotAccepted
	}
	return nil
}

func (s *Service) acceptCurrentTerms(ctx context.Context, q *store.Queries, userID uuid.UUID, ip *netip.Addr) error {
	cur, err := q.LatestTerms(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return q.AcceptTerms(ctx, store.AcceptTermsParams{UserID: userID, Version: cur.Version, Ip: ip})
}

// TermsOutstanding reports whether userID still has to accept the current
// terms, and which version that is.
func (s *Service) TermsOutstanding(ctx context.Context, userID uuid.UUID) (bool, int32, error) {
	cur, err := s.CurrentTerms(ctx)
	if err != nil || cur.Version == 0 {
		return false, 0, err
	}
	accepted, err := store.New(s.db).AcceptedTermsVersion(ctx, userID)
	if err != nil {
		return false, 0, err
	}
	return accepted < cur.Version, cur.Version, nil
}

// AcceptTerms records userID's acceptance of version (the current one).
func (s *Service) AcceptTerms(ctx context.Context, userID uuid.UUID, version int, ip *netip.Addr) error {
	if err := s.checkTermsVersion(ctx, version); err != nil {
		return err
	}
	return store.New(s.db).AcceptTerms(ctx, store.AcceptTermsParams{UserID: userID, Version: int32(version), Ip: ip})
}

// EnsureTerms publishes the default terms and privacy notice as version 1
// when none exist.
func (s *Service) EnsureTerms(ctx context.Context) error {
	cur, err := s.CurrentTerms(ctx)
	if err != nil || cur.Version != 0 {
		return err
	}
	_, err = store.New(s.db).InsertTerms(ctx, store.InsertTermsParams{TermsMd: DefaultTerms, PrivacyMd: DefaultPrivacy})
	return err
}

// PublishTerms publishes a new version; every user accepts it at their
// next request.
func (s *Service) PublishTerms(ctx context.Context, terms, privacy string, by uuid.UUID) (store.TermsVersion, error) {
	terms, privacy = strings.TrimSpace(terms), strings.TrimSpace(privacy)
	if terms == "" || privacy == "" {
		return store.TermsVersion{}, errors.New("both the terms of use and the privacy notice are required")
	}
	if len(terms) > 200_000 || len(privacy) > 200_000 {
		return store.TermsVersion{}, errors.New("terms text is too long")
	}
	return store.New(s.db).InsertTerms(ctx, store.InsertTermsParams{TermsMd: terms, PrivacyMd: privacy, PublishedBy: &by})
}

// ---- Account administration ---------------------------------------------------

// ApproveUser activates an account waiting in approval mode and tells them.
func (s *Service) ApproveUser(ctx context.Context, id uuid.UUID) (store.User, error) {
	u, err := store.New(s.db).ApproveUser(ctx, id)
	if err != nil {
		return u, err
	}
	if err := s.send(ctx, u.Email, "Your PGDock account is approved",
		"Your PGDock account is ready. Sign in at "+s.cfg.PublicURL+"/login"); err != nil {
		s.log.Warn("approval email", "user_id", id, "err", err)
	}
	return u, nil
}

// SetDisabled disables (or re-enables) an account. Disabling ends every
// session at once and drops the user's database logins.
func (s *Service) SetDisabled(ctx context.Context, id uuid.UUID, disabled bool) (store.User, error) {
	q := store.New(s.db)
	u, err := q.SetUserDisabled(ctx, store.SetUserDisabledParams{ID: id, Disabled: disabled})
	if err != nil {
		return u, err
	}
	if disabled {
		if err := q.DeleteUserSessions(ctx, store.DeleteUserSessionsParams{UserID: id}); err != nil {
			return u, err
		}
		if s.hooks.UserDisabled != nil {
			if err := s.hooks.UserDisabled(ctx, id); err != nil {
				return u, err
			}
		}
	}
	return u, nil
}

// ResetTOTP removes a user's authenticator and recovery codes (after the
// platform admin has confirmed who they are out of band). Their sessions
// end, the next sign-in enrols a new authenticator, and they are emailed.
func (s *Service) ResetTOTP(ctx context.Context, id uuid.UUID) error {
	q := store.New(s.db)
	u, err := q.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if err := q.ResetUserTOTP(ctx, id); err != nil {
		return err
	}
	if err := q.DeleteUserSessions(ctx, store.DeleteUserSessionsParams{UserID: id}); err != nil {
		return err
	}
	if err := s.send(ctx, u.Email, "Your PGDock two-factor authentication was reset",
		"The platform admin reset two-factor authentication on your PGDock account. "+
			"Your next sign-in will ask you to set up an authenticator app again.\n\n"+
			"If you didn't ask for this, contact the platform admin now."); err != nil {
		s.log.Warn("2FA reset email", "user_id", id, "err", err)
	}
	return nil
}

// ListSessions returns a user's active sessions.
func (s *Service) ListSessions(ctx context.Context, userID uuid.UUID) ([]store.ListUserSessionsRow, error) {
	return store.New(s.db).ListUserSessions(ctx, userID)
}

// RevokeSession ends one of the user's sessions.
func (s *Service) RevokeSession(ctx context.Context, userID uuid.UUID, id string) (bool, error) {
	n, err := store.New(s.db).DeleteUserSession(ctx, store.DeleteUserSessionParams{UserID: userID, ID: id})
	return n > 0, err
}
