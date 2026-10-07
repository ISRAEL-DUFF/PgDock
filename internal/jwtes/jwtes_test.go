package jwtes

import (
	"crypto/ecdsa"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	der, jwk, err := Generate("k1")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := jwk.Public()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]*ecdsa.PublicKey{"k1": pub}
	now := time.Unix(1_800_000_000, 0)
	tok, err := Sign(der, "k1", map[string]any{"sub": "u1", "aud": "abcdefgh", "exp": now.Add(time.Hour).Unix(), "role": "user"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(tok, keys, "abcdefgh", now)
	if err != nil || c["sub"] != "u1" {
		t.Fatalf("verify: %v %v", c, err)
	}
	if _, err := Verify(tok, keys, "otherref", now); !errors.Is(err, ErrAudience) {
		t.Fatalf("other audience: %v", err)
	}
	if _, err := Verify(tok, keys, "abcdefgh", now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := Verify(tok, map[string]*ecdsa.PublicKey{}, "abcdefgh", now); !errors.Is(err, ErrKey) {
		t.Fatalf("unknown key: %v", err)
	}
	// Tampered claims.
	parts := strings.Split(tok, ".")
	forged, _ := Sign(der, "k1", map[string]any{"sub": "u2", "aud": "abcdefgh", "exp": now.Add(time.Hour).Unix()})
	fp := strings.Split(forged, ".")
	if _, err := Verify(parts[0]+"."+fp[1]+"."+parts[2], keys, "abcdefgh", now); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered: %v", err)
	}
	// Another key with the same kid.
	der2, _, _ := Generate("k1")
	other, _ := Sign(der2, "k1", map[string]any{"sub": "u1", "aud": "abcdefgh", "exp": now.Add(time.Hour).Unix()})
	if _, err := Verify(other, keys, "abcdefgh", now); !errors.Is(err, ErrSignature) {
		t.Fatalf("another key: %v", err)
	}
	if _, err := Verify("a.b", keys, "abcdefgh", now); !errors.Is(err, ErrMalformed) {
		t.Fatalf("malformed: %v", err)
	}
}
