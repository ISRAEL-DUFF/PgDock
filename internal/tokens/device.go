package tokens

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Device login (V2 §7.1): the CLI asks for a code, the user approves it in
// the browser (already signed in with two factors) choosing the
// organisation and scopes, and the CLI collects the token by polling.

const (
	// DeviceExpiry is how long a device code waits for approval.
	DeviceExpiry = 10 * time.Minute
	// PollInterval is how often the CLI may poll.
	PollInterval = 5 * time.Second
	// userCodeAlphabet avoids vowels (no words) and look-alike characters.
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
)

// Device login outcomes when polling.
var (
	ErrPending  = errors.New("authorization_pending")
	ErrSlowDown = errors.New("slow_down")
	ErrDenied   = errors.New("access_denied")
	ErrExpired  = errors.New("expired_token")
)

// DeviceStart is a new device login.
type DeviceStart struct {
	DeviceCode string
	UserCode   string
	ExpiresAt  time.Time
	Interval   time.Duration
}

// StartDevice begins a device login for a CLI calling itself clientName.
func (s *Service) StartDevice(ctx context.Context, clientName string, scopes []string) (DeviceStart, error) {
	if len(scopes) == 0 {
		scopes = []string{ScopeRead, ScopeWrite}
	}
	scopes, err := NormalizeScopes(scopes)
	if err != nil {
		return DeviceStart{}, err
	}
	clientName = strings.TrimSpace(clientName)
	if len(clientName) > 100 {
		clientName = clientName[:100]
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return DeviceStart{}, err
	}
	device := base64.RawURLEncoding.EncodeToString(b)
	exp := s.cfg.Now().Add(DeviceExpiry)
	q := store.New(s.db)
	for range 5 {
		code, err := newUserCode()
		if err != nil {
			return DeviceStart{}, err
		}
		err = q.InsertDeviceRequest(ctx, store.InsertDeviceRequestParams{
			DeviceCodeHash: Hash(device), UserCode: code, ClientName: clientName, RequestedScopes: scopes, ExpiresAt: exp,
		})
		if err == nil {
			return DeviceStart{DeviceCode: device, UserCode: code, ExpiresAt: exp, Interval: PollInterval}, nil
		}
		if !strings.Contains(err.Error(), "user_code") {
			return DeviceStart{}, err
		}
	}
	return DeviceStart{}, errors.New("could not allocate a device code")
}

// NormalizeUserCode upper-cases a code and restores its dash.
func NormalizeUserCode(code string) string {
	c := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	if len(c) == 8 {
		return c[:4] + "-" + c[4:]
	}
	return c
}

// DeviceRequest is a pending device login, for the approval page.
type DeviceRequest struct {
	UserCode   string
	ClientName string
	Scopes     []string
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

// LookupDevice finds a pending device login by its user code.
func (s *Service) LookupDevice(ctx context.Context, userCode string) (DeviceRequest, error) {
	r, err := s.pendingDevice(ctx, userCode)
	if err != nil {
		return DeviceRequest{}, err
	}
	return DeviceRequest{UserCode: r.UserCode, ClientName: r.ClientName, Scopes: r.RequestedScopes, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt}, nil
}

func (s *Service) pendingDevice(ctx context.Context, userCode string) (store.DeviceAuthRequest, error) {
	r, err := store.New(s.db).GetDeviceRequestByCode(ctx, NormalizeUserCode(userCode))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if r.ApprovedBy != nil || r.DeniedAt != nil || !s.cfg.Now().Before(r.ExpiresAt) {
		return r, ErrNotFound
	}
	return r, nil
}

// ApproveDevice issues the token a pending device login asked for, as
// p.UserID in p.OrgID. The caller has checked that the user may act there
// (and on p.Projects). The scopes may be narrowed from the request, not
// widened beyond it.
func (s *Service) ApproveDevice(ctx context.Context, userCode string, p CreateParams) (store.ApiToken, error) {
	r, err := s.pendingDevice(ctx, userCode)
	if err != nil {
		return store.ApiToken{}, err
	}
	scopes, err := NormalizeScopes(p.Scopes)
	if err != nil {
		return store.ApiToken{}, err
	}
	for _, sc := range scopes {
		if !HasScope(r.RequestedScopes, sc) {
			return store.ApiToken{}, fmt.Errorf("%w: the CLI did not ask for the %s scope", ErrInvalid, sc)
		}
	}
	p.Scopes, p.Via = scopes, "device"
	if strings.TrimSpace(p.Name) == "" {
		p.Name = "CLI"
		if r.ClientName != "" {
			p.Name = r.ClientName
		}
	}
	var out store.ApiToken
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		c, err := s.create(ctx, q, p)
		if err != nil {
			return err
		}
		sealed, err := s.keyring.Encrypt([]byte(c.Secret), []byte("device:"+r.DeviceCodeHash))
		if err != nil {
			return err
		}
		n, err := q.ApproveDeviceRequest(ctx, store.ApproveDeviceRequestParams{
			DeviceCodeHash: r.DeviceCodeHash, ApprovedBy: &p.UserID, OrgID: &p.OrgID, TokenID: &c.Row.ID, SealedToken: sealed,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		out = c.Row
		return nil
	})
	return out, err
}

// DenyDevice refuses a pending device login.
func (s *Service) DenyDevice(ctx context.Context, userCode string) error {
	r, err := s.pendingDevice(ctx, userCode)
	if err != nil {
		return err
	}
	_, err = store.New(s.db).DenyDeviceRequest(ctx, r.DeviceCodeHash)
	return err
}

// DeviceToken is what an approved device login hands the CLI.
type DeviceToken struct {
	Secret string
	Row    store.ApiToken
}

// PollDevice is the CLI's poll: ErrPending until approved, then the token
// exactly once.
func (s *Service) PollDevice(ctx context.Context, deviceCode string) (DeviceToken, error) {
	q := store.New(s.db)
	digest := Hash(deviceCode)
	r, err := q.GetDeviceRequest(ctx, digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeviceToken{}, ErrExpired
	}
	if err != nil {
		return DeviceToken{}, err
	}
	now := s.cfg.Now()
	switch {
	case r.DeniedAt != nil:
		_ = q.DeleteDeviceRequest(ctx, digest)
		return DeviceToken{}, ErrDenied
	case r.SealedToken != nil:
		secret, err := s.keyring.Decrypt(r.SealedToken, []byte("device:"+digest))
		if err != nil {
			return DeviceToken{}, err
		}
		if err := q.DeleteDeviceRequest(ctx, digest); err != nil {
			return DeviceToken{}, err
		}
		row, err := q.GetUserToken(ctx, store.GetUserTokenParams{ID: *r.TokenID, UserID: *r.ApprovedBy})
		if err != nil {
			return DeviceToken{}, err
		}
		return DeviceToken{Secret: string(secret), Row: row}, nil
	case !now.Before(r.ExpiresAt):
		_ = q.DeleteDeviceRequest(ctx, digest)
		return DeviceToken{}, ErrExpired
	case r.LastPolledAt != nil && now.Sub(*r.LastPolledAt) < PollInterval/2:
		_ = q.PollDeviceRequest(ctx, store.PollDeviceRequestParams{DeviceCodeHash: digest, Now: now})
		return DeviceToken{}, ErrSlowDown
	}
	if err := q.PollDeviceRequest(ctx, store.PollDeviceRequestParams{DeviceCodeHash: digest, Now: now}); err != nil {
		return DeviceToken{}, err
	}
	return DeviceToken{}, ErrPending
}

func newUserCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 0, 9)
	for i, c := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, userCodeAlphabet[int(c)%len(userCodeAlphabet)])
	}
	return string(out), nil
}
