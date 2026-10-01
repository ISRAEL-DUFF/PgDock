// Package provision implements the shared-tier project flows (spec §6):
// create, rotate password, and delete, each run as an operation with
// idempotent steps and compensating rollback.
package provision

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxNameLen = 64
	maxSlugLen = 40 // slug_xxxx_owner stays well under Postgres's 63-byte limit
	suffixLen  = 4
)

// ErrInvalid wraps validation failures that are the caller's fault.
var ErrInvalid = errors.New("invalid request")

// ValidateName checks a project's display name.
func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return "", fmt.Errorf("%w: name must be at most %d characters", ErrInvalid, maxNameLen)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: name must not contain control characters", ErrInvalid)
		}
	}
	return name, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a display name into a lowercase identifier made of
// [a-z0-9_] that starts with a letter, e.g. "My Blog!" -> "my_blog".
func Slugify(name string) (string, error) {
	s := nonSlug.ReplaceAllString(strings.ToLower(name), "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "", fmt.Errorf("%w: name must contain at least one ASCII letter or digit", ErrInvalid)
	}
	if s[0] >= '0' && s[0] <= '9' {
		s = "p_" + s
	}
	if len(s) > maxSlugLen {
		s = strings.TrimRight(s[:maxSlugLen], "_")
	}
	return s, nil
}

const suffixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// randomSuffix returns n random characters from [a-z0-9].
func randomSuffix(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		// 256 % 36 != 0, but the slight bias is irrelevant for a uniqueness suffix.
		b[i] = suffixAlphabet[int(b[i])%len(suffixAlphabet)]
	}
	return string(b), nil
}

// Names returns the database and owner role names for a slug, e.g.
// blog_k2f9 and blog_k2f9_owner.
func Names(slug string) (dbName, ownerRole string, err error) {
	suffix, err := randomSuffix(suffixLen)
	if err != nil {
		return "", "", err
	}
	dbName = slug + "_" + suffix
	return dbName, dbName + "_owner", nil
}

// GeneratePassword returns a password from 32 random bytes, URL-safe so it
// can sit in a connection string without escaping.
func GeneratePassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
