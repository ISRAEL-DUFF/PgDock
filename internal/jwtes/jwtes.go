// Package jwtes signs and verifies ES256 JSON Web Tokens and handles their
// public keys as JWKs (V4 §4.4). It holds no state: pgdock-server signs
// with a project's private key, pgdock-edge verifies with the public keys
// it was sent.
package jwtes

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// JWK is a P-256 public key.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
}

// Generate makes a P-256 key, returning its PKCS#8 encoding and its JWK.
func Generate(kid string) (der []byte, jwk JWK, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, JWK{}, err
	}
	if der, err = x509.MarshalPKCS8PrivateKey(k); err != nil {
		return nil, JWK{}, err
	}
	return der, PublicJWK(&k.PublicKey, kid), nil
}

// PublicJWK is pub as a JWK.
func PublicJWK(pub *ecdsa.PublicKey, kid string) JWK {
	x, y := make([]byte, 32), make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	return JWK{Kty: "EC", Crv: "P-256", X: b64.EncodeToString(x), Y: b64.EncodeToString(y), Kid: kid, Alg: "ES256", Use: "sig"}
}

// Public decodes a JWK.
func (j JWK) Public() (*ecdsa.PublicKey, error) {
	if j.Kty != "EC" || j.Crv != "P-256" {
		return nil, errors.New("jwtes: not a P-256 key")
	}
	x, err := b64.DecodeString(j.X)
	if err != nil {
		return nil, err
	}
	y, err := b64.DecodeString(j.Y)
	if err != nil {
		return nil, err
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	if !elliptic.P256().IsOnCurve(pub.X, pub.Y) { //nolint:staticcheck // the point check is the point
		return nil, errors.New("jwtes: point not on the curve")
	}
	return pub, nil
}

// ParsePEM reads a PEM PKCS#8 P-256 private key (an Apple .p8 key) and
// returns its DER, for Sign.
func ParsePEM(s string) ([]byte, error) {
	b, _ := pem.Decode([]byte(strings.TrimSpace(s)))
	if b == nil {
		return nil, errors.New("jwtes: not a PEM key")
	}
	key, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("jwtes: %w", err)
	}
	if k, ok := key.(*ecdsa.PrivateKey); !ok || k.Curve != elliptic.P256() {
		return nil, errors.New("jwtes: not a P-256 key")
	}
	return b.Bytes, nil
}

// Sign returns a compact JWT of claims signed with the PKCS#8 key der.
func Sign(der []byte, kid string, claims map[string]any) (string, error) {
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return "", err
	}
	k, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("jwtes: not an ECDSA key")
	}
	h, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": kid})
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64.EncodeToString(sig), nil
}

// Errors from Verify.
var (
	ErrMalformed = errors.New("malformed token")
	ErrKey       = errors.New("token signed with an unknown key")
	ErrSignature = errors.New("token signature does not verify")
	ErrExpired   = errors.New("token expired")
	ErrAudience  = errors.New("token is for another project")
)

// Verify checks token's signature against keys (by kid), its expiry and
// not-before at now (with a minute's leeway), and that its aud is aud.
// It returns the claims.
func Verify(token string, keys map[string]*ecdsa.PublicKey, aud string, now time.Time) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, ErrMalformed
	}
	var h struct{ Alg, Kid string }
	if json.Unmarshal(hb, &h) != nil || h.Alg != "ES256" {
		return nil, ErrMalformed
	}
	pub := keys[h.Kid]
	if pub == nil {
		return nil, ErrKey
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, ErrMalformed
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, ErrSignature
	}
	cb, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}
	var claims map[string]any
	if json.Unmarshal(cb, &claims) != nil {
		return nil, ErrMalformed
	}
	const leeway = time.Minute
	exp, ok := claims["exp"].(float64)
	if !ok || now.After(time.Unix(int64(exp), 0).Add(leeway)) {
		return nil, ErrExpired
	}
	if nbf, ok := claims["nbf"].(float64); ok && now.Add(leeway).Before(time.Unix(int64(nbf), 0)) {
		return nil, fmt.Errorf("%w: not valid yet", ErrExpired)
	}
	if !audMatches(claims["aud"], aud) {
		return nil, ErrAudience
	}
	return claims, nil
}

func audMatches(v any, aud string) bool {
	switch a := v.(type) {
	case string:
		return a == aud
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && s == aud {
				return true
			}
		}
	}
	return false
}
