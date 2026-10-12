// Package argonpw hashes passwords with argon2id in the PHC string format
// and verifies them in constant time. Operator accounts (spec §7.2) and
// project users (V4 §4.1) use it, with their own parameters; a hash
// carries its parameters, so either verifies the other's.
package argonpw

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id's cost parameters.
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
	KeyLen    uint32
	SaltLen   int
}

// Hash returns pw's hash:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
func Hash(pw string, p Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, p.KeyLen)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads, b64(salt), b64(key)), nil
}

// Verify reports whether pw matches encoded, in constant time with
// respect to the hash.
func Verify(pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("argonpw: not an argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("argonpw: unsupported argon2 version")
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return false, errors.New("argonpw: bad argon2 parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("argonpw: bad argon2 salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("argonpw: bad argon2 hash")
	}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
