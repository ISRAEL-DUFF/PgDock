package services

import (
	"crypto/rand"
	"math/big"
	"strings"

	"github.com/israel-duff/pgdock/internal/edgeapi"
)

// Key kinds and prefixes (V4 §2.2).
const (
	KindPublishable = edgeapi.KindPublishable
	KindSecret      = edgeapi.KindSecret

	PrefixPublishable = "pgd_pub_"
	PrefixSecret      = "pgd_sec_"
)

const (
	refFirst = "abcdefghijkmnpqrstuvwxyz"         // a DNS label starts with a letter
	refRest  = "abcdefghijkmnpqrstuvwxyz23456789" // no 0/o, 1/l
	keyChars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
)

func randomFrom(alphabet string, n int) (string, error) {
	var b strings.Builder
	n0 := big.NewInt(int64(len(alphabet)))
	for i := 0; i < n; i++ {
		v, err := rand.Int(rand.Reader, n0)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[v.Int64()])
	}
	return b.String(), nil
}

// NewRef is a project reference: 8 characters, the first a letter.
func NewRef() (string, error) {
	first, err := randomFrom(refFirst, 1)
	if err != nil {
		return "", err
	}
	rest, err := randomFrom(refRest, 7)
	return first + rest, err
}

// GenerateKey makes a key of kind: the key itself, its hash, and the prefix
// shown in lists.
func GenerateKey(kind string) (key, hash, prefix string, err error) {
	p, n := PrefixPublishable, 32
	if kind == KindSecret {
		p, n = PrefixSecret, 40
	}
	body, err := randomFrom(keyChars, n)
	if err != nil {
		return "", "", "", err
	}
	key = p + body
	return key, HashKey(key), key[:len(p)+6], nil
}

// HashKey is how keys are stored and looked up: hex SHA-256.
func HashKey(key string) string { return edgeapi.HashKey(key) }
