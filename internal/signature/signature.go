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
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a header value against body, refusing timestamps more than
// tolerance away from now (replays).
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return errors.New("malformed signature header")
	}
	if d := now.Sub(time.Unix(n, 0)); d > tolerance || d < -tolerance {
		return errors.New("signature timestamp outside the tolerance")
	}
	want := Sign(secret, time.Unix(n, 0), body)
	if !hmac.Equal([]byte(want), []byte("t="+ts+",v1="+sig)) {
		return errors.New("signature mismatch")
	}
	return nil
}
