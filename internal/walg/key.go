// Package walg holds what the control plane and agents share about WAL-G:
// its encryption key and its environment.
package walg

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// keyEpoch fixes the key's creation time, so the same backup key always
// yields the same OpenPGP key.
var keyEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// PGPKey derives WAL-G's OpenPGP private key (armored) from the backup key.
// WAL-G encrypts base backups and WAL with it (WALG_PGP_KEY). Deriving it,
// rather than storing a second secret, means the backup key alone restores
// every backup, logical or physical.
func PGPKey(backupKey []byte) (string, error) {
	return deriveKey(backupKey, "pgdock wal-g openpgp v1", "PGDock WAL-G", "wal-g@pgdock.invalid")
}

// ProjectPGPKey derives a project's OpenPGP backup key (armored, private)
// from its backup_keys secret (V2 s6). Its logical backups are OpenPGP
// messages to this key and, on the dedicated tier, WAL-G encrypts base
// backups and WAL with it, so the downloaded key alone restores them with
// standard tools (gpg, pg_restore, wal-g).
func ProjectPGPKey(secret []byte) (string, error) {
	return deriveKey(secret, "pgdock project backup openpgp v1", "PGDock project backup", "backups@pgdock.invalid")
}

func deriveKey(secret []byte, info, name, email string) (string, error) {
	seed, err := hkdf.Key(sha256.New, secret, nil, info, 1<<12)
	if err != nil {
		return "", err
	}
	cfg := &packet.Config{
		Rand:      bytes.NewReader(seed),
		Time:      func() time.Time { return keyEpoch },
		Algorithm: packet.PubKeyAlgoEdDSA,
		Curve:     packet.Curve25519,
	}
	e, err := openpgp.NewEntity(name, "", email, cfg)
	if err != nil {
		return "", fmt.Errorf("derive wal-g key: %w", err)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		return "", err
	}
	if err := e.SerializePrivateWithoutSigning(w, nil); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// PublicKey returns the armored public half of an armored private key.
func PublicKey(armoredPrivate string) (string, error) {
	el, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armoredPrivate))
	if err != nil || len(el) != 1 {
		return "", fmt.Errorf("read openpgp key: %w", errors.Join(err, errNotOneKey(len(el))))
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		return "", err
	}
	if err := el[0].Serialize(w); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Fingerprint is an armored key's OpenPGP fingerprint (upper-case hex), as
// gpg --list-keys shows it.
func Fingerprint(armored string) (string, error) {
	el, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil || len(el) != 1 {
		return "", fmt.Errorf("read openpgp key: %w", errors.Join(err, errNotOneKey(len(el))))
	}
	return strings.ToUpper(hex.EncodeToString(el[0].PrimaryKey.Fingerprint)), nil
}

func errNotOneKey(n int) error {
	if n == 1 {
		return nil
	}
	return fmt.Errorf("want one key, got %d", n)
}
