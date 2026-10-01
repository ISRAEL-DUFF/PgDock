// Package auth implements operator authentication (spec §7.2): argon2id
// passwords, TOTP second factor, server-side sessions, step-up
// re-authentication, and login rate limiting with lockout.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (RFC 9106 second recommended option, scaled to a
// small control node): 64 MiB, 3 passes, 2 lanes.
type argonParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
	keyLen    uint32
	saltLen   int
}

var defaultArgon = argonParams{memoryKiB: 64 * 1024, time: 3, threads: 2, keyLen: 32, saltLen: 16}

// Password length limits. The upper bound caps hashing cost.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 256
)

// ErrWeakPassword is returned for passwords outside the length limits.
var ErrWeakPassword = fmt.Errorf("password must be %d to %d characters", MinPasswordLen, MaxPasswordLen)

// CheckPasswordPolicy validates a new password.
func CheckPasswordPolicy(pw string) error {
	if n := utf8.RuneCountInString(pw); n < MinPasswordLen || len(pw) > MaxPasswordLen {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns a PHC-format argon2id hash:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
func HashPassword(pw string) (string, error) {
	return hashWith(pw, defaultArgon)
}

func hashWith(pw string, p argonParams) (string, error) {
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, p.time, p.memoryKiB, p.threads, p.keyLen)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memoryKiB, p.time, p.threads, b64(salt), b64(key)), nil
}

// VerifyPassword reports whether pw matches a hash from HashPassword, in
// constant time with respect to the hash.
func VerifyPassword(pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("auth: not an argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("auth: unsupported argon2 version")
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.time, &p.threads); err != nil {
		return false, errors.New("auth: bad argon2 parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("auth: bad argon2 salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("auth: bad argon2 hash")
	}
	got := argon2.IDKey([]byte(pw), salt, p.time, p.memoryKiB, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against for unknown emails, so a login for a
// missing account costs the same as one with a wrong password.
var dummyHash = func() string {
	h, err := HashPassword("pgdock-dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return h
}()
