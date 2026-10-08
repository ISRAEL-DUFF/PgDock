package auth

import (
	"time"

	"github.com/israel-duff/pgdock/internal/totp"
)

// GenerateTOTPSecret returns a new 160-bit secret, base32-encoded.
func GenerateTOTPSecret() (string, error) { return totp.GenerateSecret() }

// TOTPURI returns the otpauth:// URI an authenticator app scans.
func TOTPURI(secret, account, issuer string) string { return totp.URI(secret, account, issuer) }

// TOTPCode returns the code for secret at time t.
func TOTPCode(secret string, t time.Time) (string, error) { return totp.Code(secret, t) }

// VerifyTOTP checks code against secret at now, allowing one step of
// drift. It returns the matched time step, which callers must persist and
// require to increase so a code cannot be replayed.
func VerifyTOTP(secret, code string, now time.Time) (step int64, ok bool) {
	return totp.Verify(secret, code, now)
}
