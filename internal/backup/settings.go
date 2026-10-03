package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/backupfmt"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
)

// Settings errors.
var (
	ErrNoStorage   = errors.New("backup storage is not configured (Settings → Storage)")
	ErrNoBackupKey = errors.New("no backup encryption key yet (Settings → Backup key)")
	ErrKeyExists   = errors.New("a backup key already exists")
	ErrWrongKey    = errors.New("that does not match the backup key")
)

func credsAAD(id uuid.UUID) []byte { return []byte("storage_targets.credentials:" + id.String()) }

type creds struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
	PathStyle bool   `json:"path_style"`
}

// StorageTarget returns the platform default storage target with its
// credentials.
func (s *Service) StorageTarget(ctx context.Context) (uuid.UUID, storage.Target, error) {
	row, err := store.New(s.db).GetDefaultStorageTarget(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, storage.Target{}, ErrNoStorage
	}
	if err != nil {
		return uuid.Nil, storage.Target{}, err
	}
	t, err := s.openTarget(row)
	return row.ID, t, err
}

// TestStorage runs the live write/read/delete test against t (spec §8.2).
func (s *Service) TestStorage(ctx context.Context, t storage.Target) ([]storage.TestStep, bool, error) {
	c, err := storage.New(t)
	if err != nil {
		return nil, false, err
	}
	steps, ok := c.LiveTest(ctx)
	return steps, ok, nil
}

// SaveStorage stores t as the platform default target after a passing
// live test (the setup wizard and Settings → Storage). An empty secret key
// keeps the stored one (so the UI never needs to read it).
func (s *Service) SaveStorage(ctx context.Context, t storage.Target) ([]storage.TestStep, bool, error) {
	in := TargetInput{
		Name: "default", Endpoint: t.Endpoint, Region: t.Region, Bucket: t.Bucket, Prefix: t.Prefix,
		AccessKey: t.AccessKey, SecretKey: t.SecretKey, PathStyle: t.PathStyle, IsDefault: true,
	}
	var id *uuid.UUID
	if cur, err := store.New(s.db).GetDefaultStorageTarget(ctx); err == nil {
		id, in.Name = &cur.ID, cur.Name
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	_, steps, err := s.SaveTarget(ctx, nil, id, in, nil)
	if errors.Is(err, ErrTargetTest) {
		return steps, false, nil
	}
	if err != nil && steps == nil {
		return nil, false, err
	}
	return steps, err == nil, err
}

// ---- Backup key --------------------------------------------------------------

const (
	keyBackupKey = "backup_key"
	backupKeyAAD = "settings.backup_key"
)

type storedKey struct {
	Sealed      []byte     `json:"sealed"`
	Fingerprint string     `json:"fingerprint"`
	CreatedAt   time.Time  `json:"created_at"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
}

// KeyInfo describes the backup key without revealing it.
type KeyInfo struct {
	Exists      bool
	Fingerprint string
	CreatedAt   time.Time
	ConfirmedAt *time.Time
}

func fingerprint(k []byte) string {
	sum := sha256.Sum256(append([]byte("pgdock backup key\x00"), k...))
	return hex.EncodeToString(sum[:6])
}

// EncodeKey renders a backup key for download.
func EncodeKey(k []byte) string { return backupfmt.EncodeKey(k) }

// DecodeKey parses EncodeKey's output (or bare base64).
func DecodeKey(s string) ([]byte, error) { return backupfmt.DecodeKey(s) }

func (s *Service) loadKey(ctx context.Context, q *store.Queries) (*storedKey, error) {
	raw, err := q.GetSetting(ctx, keyBackupKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var k storedKey
	return &k, json.Unmarshal(raw, &k)
}

// KeyInfo reports whether a backup key exists and was confirmed.
func (s *Service) KeyInfo(ctx context.Context) (KeyInfo, error) {
	k, err := s.loadKey(ctx, store.New(s.db))
	if err != nil || k == nil {
		return KeyInfo{}, err
	}
	return KeyInfo{Exists: true, Fingerprint: k.Fingerprint, CreatedAt: k.CreatedAt, ConfirmedAt: k.ConfirmedAt}, nil
}

// GenerateKey creates the backup encryption key (spec §7.3) and returns it
// for the operator to download. It refuses to replace an existing key:
// that would make every earlier backup unreadable.
func (s *Service) GenerateKey(ctx context.Context) ([]byte, KeyInfo, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, KeyInfo{}, err
	}
	var info KeyInfo
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('pgdock_backup_key'))`); err != nil {
			return err
		}
		q := store.New(tx)
		if cur, err := s.loadKey(ctx, q); err != nil {
			return err
		} else if cur != nil {
			return ErrKeyExists
		}
		sealed, err := s.keyring.Encrypt(k, []byte(backupKeyAAD))
		if err != nil {
			return err
		}
		sk := storedKey{Sealed: sealed, Fingerprint: fingerprint(k), CreatedAt: time.Now().UTC()}
		b, _ := json.Marshal(sk)
		info = KeyInfo{Exists: true, Fingerprint: sk.Fingerprint, CreatedAt: sk.CreatedAt}
		return q.PutSetting(ctx, store.PutSettingParams{Key: keyBackupKey, Value: b})
	})
	return k, info, err
}

// BackupKey returns the backup key for sealing and opening objects.
func (s *Service) BackupKey(ctx context.Context) ([]byte, error) {
	k, err := s.loadKey(ctx, store.New(s.db))
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, ErrNoBackupKey
	}
	return s.keyring.Decrypt(k.Sealed, []byte(backupKeyAAD))
}

// ConfirmKey records that the operator stored the key offline: they paste
// back (or upload) the downloaded key to prove they have it. The
// fingerprint is shown in the UI, so it proves nothing and is not accepted.
func (s *Service) ConfirmKey(ctx context.Context, proof string) (KeyInfo, error) {
	k, err := s.BackupKey(ctx)
	if err != nil {
		return KeyInfo{}, err
	}
	pk, err := DecodeKey(proof)
	if err != nil || subtle.ConstantTimeCompare(pk, k) != 1 {
		return KeyInfo{}, ErrWrongKey
	}
	var info KeyInfo
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		sk, err := s.loadKey(ctx, q)
		if err != nil || sk == nil {
			return errors.Join(err, ErrNoBackupKey)
		}
		now := time.Now().UTC()
		sk.ConfirmedAt = &now
		b, _ := json.Marshal(sk)
		info = KeyInfo{Exists: true, Fingerprint: sk.Fingerprint, CreatedAt: sk.CreatedAt, ConfirmedAt: sk.ConfirmedAt}
		return q.PutSetting(ctx, store.PutSettingParams{Key: keyBackupKey, Value: b})
	})
	return info, err
}
