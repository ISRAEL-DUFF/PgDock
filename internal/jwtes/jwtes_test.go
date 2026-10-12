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

func TestJWKRoundTrip(t *testing.T) {
	for range 50 { // some keys have a coordinate with a leading zero byte
		_, jwk, err := Generate("k")
		if err != nil {
			t.Fatal(err)
		}
		pub, err := jwk.Public()
		if err != nil {
			t.Fatal(err)
		}
		if back := PublicJWK(pub, "k"); back != jwk {
			t.Fatalf("round trip: %+v != %+v", back, jwk)
		}
		// A coordinate sent without its leading zero bytes still parses.
		x, _ := b64.DecodeString(jwk.X)
		if x[0] == 0 {
			short := jwk
			short.X = b64.EncodeToString(x[1:])
			if p2, err := short.Public(); err != nil || !p2.Equal(pub) {
				t.Fatalf("short coordinate: %v", err)
			}
		}
	}
	_, jwk, _ := Generate("k")
	bad := jwk
	bad.Y = jwk.X // not on the curve
	if _, err := bad.Public(); err == nil {
		t.Fatal("an off-curve point was accepted")
	}
}
