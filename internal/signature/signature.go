// Package signature is the PGDock-Signature scheme: an HMAC-SHA256 over
// "<unix time>.<body>", sent as t=<unix>,v1=<hex>. Webhooks and outbound
// job calls use it (V2 §9.1), and so do pgdock-server's pushes to the
// status service (V3 §2.6). It has no dependencies so pgdock-status can use
// it without the rest of PGDock.
package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Header is the HTTP header the signature travels in.
const Header = "PGDock-Signature"

// Sign returns the header value for body signed at t.
func Sign(secret string, t time.Time, body []byte) string {
	return SignAll([]string{secret}, t, body)
}

// SignAll signs body at t with each secret, one v1 value each in order:
// during a secret rotation's overlap, the new secret and then the old.
func SignAll(secrets []string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	out := "t=" + ts
	for _, secret := range secrets {
		out += ",v1=" + mac(secret, ts, body)
	}
	return out
}

func mac(secret, ts string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a header value against body, refusing timestamps more than
// tolerance away from now (replays). Any of the header's v1 values may
// match: during a rotation's overlap there is one per secret.
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return errors.New("malformed signature header")
	}
	if d := now.Sub(time.Unix(n, 0)); d > tolerance || d < -tolerance {
		return errors.New("signature timestamp outside the tolerance")
	}
	want := mac(secret, ts, body)
	for _, sig := range sigs {
		if hmac.Equal([]byte(want), []byte(sig)) {
			return nil
		}
	}
	return errors.New("signature mismatch")
}
