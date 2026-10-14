package files

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPathsAndBuckets(t *testing.T) {
	for _, p := range []string{"a", "a/b.png", "folder/sub/ünïcode file (1).txt"} {
		if !ValidPath(p) {
			t.Errorf("%q should be valid", p)
		}
	}
	for _, p := range []string{"", "/a", "a/", "a//b", "../a", "a/../b", "a/./b", "a\x00b", "a\nb", strings.Repeat("x", MaxPath+1), "\xff"} {
		if ValidPath(p) {
			t.Errorf("%q should be invalid", p)
		}
	}
	for _, b := range []string{"avatars", "my-bucket.v2", "a"} {
		if !ValidBucket(b) {
			t.Errorf("bucket %q should be valid", b)
		}
	}
	for _, b := range []string{"", "Avatars", "-x", "public", "sign", uuid.NewString(), strings.Repeat("a", 64), "a/b"} {
		if ValidBucket(b) {
			t.Errorf("bucket %q should be invalid", b)
		}
	}
	if got := EscapePath("a b/c?d#e"); got != "a%20b/c%3Fd%23e" {
		t.Errorf("escape: %s", got)
	}
	if got := LikePrefix(`50%_off\`); got != `50\%\_off\\%` {
		t.Errorf("like: %s", got)
	}
}

func TestDetectMIME(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	for _, tc := range []struct {
		declared string
		head     []byte
		want     string
		err      error
	}{
		{"image/png", png, "image/png", nil},
		{"", png, "image/png", nil},
		{"application/octet-stream", []byte("hello"), "text/plain", nil},
		{"image/png", []byte("<html><script>x</script></html>"), "", ErrMIMEMismatch},
		{"image/jpeg", png, "image/jpeg", nil}, // both images: the declared type stands
		{"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "image/svg+xml", nil},
		{"video/mp4", []byte("plain text"), "", ErrMIMEMismatch},
		{"text/plain; charset=utf-8", []byte("hello"), "text/plain", nil},
		{"not a type", []byte("x"), "text/plain", nil},
		{"image/png", nil, "image/png", nil},
	} {
		got, err := DetectMIME(tc.declared, tc.head)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("DetectMIME(%q): %q %v, want %q %v", tc.declared, got, err, tc.want, tc.err)
		}
	}
	if !MIMEAllowed(nil, "x/y") || !MIMEAllowed([]string{"image/*"}, "image/png") || MIMEAllowed([]string{"image/*"}, "text/plain") ||
		!MIMEAllowed([]string{"text/plain"}, "text/plain") {
		t.Error("MIMEAllowed")
	}
}

func TestTokens(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	tok := Sign(secret, Token{Kind: TokenGet, Ref: "abcdefgh", Bucket: "b", Path: "p/q.png", Exp: now.Add(time.Minute).Unix(),
		Transform: &Transform{Width: 10}})
	got, ok := Verify(secret, tok, TokenGet, "abcdefgh", "b", "p/q.png", now)
	if !ok || got.Transform == nil || got.Transform.Width != 10 {
		t.Fatalf("verify: %+v %v", got, ok)
	}
	for name, bad := range map[string]func() bool{
		"other kind":   func() bool { _, ok := Verify(secret, tok, TokenPut, "abcdefgh", "b", "p/q.png", now); return ok },
		"other ref":    func() bool { _, ok := Verify(secret, tok, TokenGet, "zzzzzzzz", "b", "p/q.png", now); return ok },
		"other path":   func() bool { _, ok := Verify(secret, tok, TokenGet, "abcdefgh", "b", "p/other.png", now); return ok },
		"other secret": func() bool { _, ok := Verify([]byte("x"), tok, TokenGet, "abcdefgh", "b", "p/q.png", now); return ok },
		"expired": func() bool {
			_, ok := Verify(secret, tok, TokenGet, "abcdefgh", "b", "p/q.png", now.Add(time.Hour))
			return ok
		},
		"garbage": func() bool { _, ok := Verify(secret, "nope", TokenGet, "abcdefgh", "b", "p/q.png", now); return ok },
		"tampered": func() bool {
			_, ok := Verify(secret, "x"+tok[1:], TokenGet, "abcdefgh", "b", "p/q.png", now)
			return ok
		},
	} {
		if bad() {
			t.Errorf("%s verified", name)
		}
	}
}
