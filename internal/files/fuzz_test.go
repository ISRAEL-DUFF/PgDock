package files

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// FuzzSignedURL (V4-M37 security review): a signed URL's token is good
// only for the project, kind, bucket and path it was made for, before its
// expiry, under that project's secret; no change to it verifies.
func FuzzSignedURL(f *testing.F) {
	secretA, secretB := []byte("project-a-storage-secret-0123456789"), []byte("project-b-storage-secret-0123456789")
	now := time.Unix(1_800_000_000, 0)
	f.Add("download", "aaaaaaa1", "avatars", "u1/me.png", int64(60), "", "", "", 0, byte(0))
	f.Add("upload", "aaaaaaa1", "docs", "a/b.txt", int64(3600), "bbbbbbb2", "", "", 5, byte('A'))
	f.Add("download", "aaaaaaa1", "avatars", "u1/me.png", int64(60), "", "avatars2", "u1/me.png", -1, byte(0))
	f.Fuzz(func(t *testing.T, kind, ref, bucket, path string, ttl int64, otherRef, otherBucket, otherPath string, flip int, b byte) {
		for _, s := range []string{kind, ref, bucket, path, otherRef, otherBucket, otherPath} {
			if !utf8.ValidString(s) {
				return // the edge refuses such names before signing (ValidPath)
			}
		}
		tok := Sign(secretA, Token{Kind: kind, Ref: ref, Bucket: bucket, Path: path, Exp: now.Unix() + ttl})
		_, ok := Verify(secretA, tok, kind, ref, bucket, path, now)
		if ok != (ttl > 0) {
			t.Fatalf("own token (ttl %d): %v", ttl, ok)
		}
		if _, ok := Verify(secretB, tok, kind, ref, bucket, path, now); ok {
			t.Fatal("verified under another project's secret")
		}
		if otherRef != ref {
			if _, ok := Verify(secretA, tok, kind, otherRef, bucket, path, now); ok {
				t.Fatalf("verified for project %q, made for %q", otherRef, ref)
			}
		}
		if otherBucket != bucket || otherPath != path {
			if _, ok := Verify(secretA, tok, kind, ref, otherBucket, otherPath, now); ok {
				t.Fatalf("verified for %q/%q, made for %q/%q", otherBucket, otherPath, bucket, path)
			}
		}
		if kind != "upload" {
			if _, ok := Verify(secretA, tok, "upload", ref, bucket, path, now); ok {
				t.Fatalf("a %q token verified as an upload", kind)
			}
		}
		if flip >= 0 && flip < len(tok) && tok[flip] != b {
			mut := tok[:flip] + string([]byte{b}) + tok[flip+1:]
			if got, ok := Verify(secretA, mut, kind, ref, bucket, path, now); ok && !sameToken(mut, tok) {
				t.Fatalf("a changed token verified: %q -> %+v", mut, got)
			}
		}
	})
}

// sameToken: base64 without padding can spell the last byte two ways; a
// change there that decodes the same is the same token.
func sameToken(a, b string) bool {
	pa, sa, _ := strings.Cut(a, ".")
	pb, sb, _ := strings.Cut(b, ".")
	return pa == pb && len(sa) == len(sb) && sa[:len(sa)-1] == sb[:len(sb)-1]
}
