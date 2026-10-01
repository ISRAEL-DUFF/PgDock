// Package walg holds what the control plane and agents share about WAL-G:
// its encryption key and its environment.
package walg

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
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
	seed, err := hkdf.Key(sha256.New, backupKey, nil, "pgdock wal-g openpgp v1", 1<<12)
	if err != nil {
		return "", err
	}
	cfg := &packet.Config{
		Rand:      bytes.NewReader(seed),
		Time:      func() time.Time { return keyEpoch },
		Algorithm: packet.PubKeyAlgoEdDSA,
		Curve:     packet.Curve25519,
	}
	e, err := openpgp.NewEntity("PGDock WAL-G", "", "wal-g@pgdock.invalid", cfg)
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
