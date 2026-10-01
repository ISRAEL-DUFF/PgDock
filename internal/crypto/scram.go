package crypto

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// SCRAMIterations matches PostgreSQL's default scram_iterations.
const SCRAMIterations = 4096

const scramSaltLen = 16

// SCRAMVerifier returns a PostgreSQL SCRAM-SHA-256 verifier for password
// with a fresh random salt, in the form stored in pg_authid.rolpassword:
//
//	SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>
//
// Setting it with ALTER ROLE ... PASSWORD '<verifier>' and writing the same
// string to PgBouncer's auth_file is what makes SCRAM passthrough work: the
// pooler can authenticate to the backend with the client's proof, and no
// plaintext password is stored anywhere (spec §5.2).
func SCRAMVerifier(password string) (string, error) {
	salt := make([]byte, scramSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return SCRAMVerifierWithSalt(password, salt, SCRAMIterations)
}

// SCRAMVerifierWithSalt is SCRAMVerifier with a caller-chosen salt and
// iteration count. Passwords must be ASCII; PGDock only hashes passwords it
// generates, so SASLprep normalization is not needed.
func SCRAMVerifierWithSalt(password string, salt []byte, iterations int) (string, error) {
	for i := 0; i < len(password); i++ {
		if password[i] < 0x20 || password[i] > 0x7e {
			return "", fmt.Errorf("crypto: SCRAM password must be printable ASCII")
		}
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")

	b64 := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$" + strconv.Itoa(iterations) + ":" + b64(salt) +
		"$" + b64(storedKey[:]) + ":" + b64(serverKey), nil
}

// IsSCRAMVerifier reports whether s looks like a SCRAM-SHA-256 verifier.
func IsSCRAMVerifier(s string) bool {
	rest, ok := strings.CutPrefix(s, "SCRAM-SHA-256$")
	if !ok {
		return false
	}
	params, keys, ok := strings.Cut(rest, "$")
	if !ok {
		return false
	}
	iter, salt, ok := strings.Cut(params, ":")
	if !ok {
		return false
	}
	if n, err := strconv.Atoi(iter); err != nil || n < 1 {
		return false
	}
	stored, server, ok := strings.Cut(keys, ":")
	if !ok {
		return false
	}
	for _, part := range []string{salt, stored, server} {
		if _, err := base64.StdEncoding.DecodeString(part); err != nil {
			return false
		}
	}
	return true
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}
