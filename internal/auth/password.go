// Package auth implements operator authentication (spec §7.2): argon2id
// passwords, TOTP second factor, server-side sessions, step-up
// re-authentication, and login rate limiting with lockout.
package auth

import (
	"fmt"
	"unicode/utf8"

	"github.com/israel-duff/pgdock/internal/argonpw"
)

// Argon2id parameters (RFC 9106 second recommended option, scaled to a
// small control node): 64 MiB, 3 passes, 2 lanes.
type argonParams = argonpw.Params

var defaultArgon = argonParams{MemoryKiB: 64 * 1024, Time: 3, Threads: 2, KeyLen: 32, SaltLen: 16}

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

func hashWith(pw string, p argonParams) (string, error) { return argonpw.Hash(pw, p) }

// VerifyPassword reports whether pw matches a hash from HashPassword, in
// constant time with respect to the hash.
func VerifyPassword(pw, encoded string) (bool, error) { return argonpw.Verify(pw, encoded) }

// dummyHash is verified against for unknown emails, so a login for a
// missing account costs the same as one with a wrong password.
var dummyHash = func() string {
	h, err := HashPassword("pgdock-dummy-password-for-timing")
	if err != nil {
		panic(err)
	}
	return h
}()
