package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	fast := argonParams{memoryKiB: 1024, time: 1, threads: 1, keyLen: 32, saltLen: 16}
	h, err := hashWith("correct horse battery", fast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("hash %q", h)
	}
	if ok, err := VerifyPassword("correct horse battery", h); !ok || err != nil {
		t.Fatalf("verify: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword("wrong horse battery", h); ok {
		t.Fatal("wrong password accepted")
	}
	if h2, _ := hashWith("correct horse battery", fast); h2 == h {
		t.Fatal("hashes are not salted")
	}
	for _, bad := range []string{"", "$bcrypt$x", "$argon2id$v=18$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=1,t=1,p=1$!!$AA"} {
		if _, err := VerifyPassword("x", bad); err == nil {
			t.Errorf("accepted malformed hash %q", bad)
		}
	}
	// The production parameters are what HashPassword uses.
	if !strings.HasPrefix(dummyHash, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("default params: %q", dummyHash)
	}
}

func TestPasswordPolicy(t *testing.T) {
	if CheckPasswordPolicy("short") == nil || CheckPasswordPolicy(strings.Repeat("x", 257)) == nil {
		t.Fatal("policy accepts bad lengths")
	}
	if err := CheckPasswordPolicy("twelve chars"); err != nil {
		t.Fatal(err)
	}
}

// RFC 6238 appendix B, SHA-1, secret "12345678901234567890", 8 digits; the
// 6-digit codes are the last six digits.
func TestTOTPVectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for unix, want := range map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	} {
		got, err := TOTPCode(secret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Errorf("T=%d: got %s, want %s (%v)", unix, got, want, err)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	code, _ := TOTPCode(secret, now)
	step, ok := VerifyTOTP(secret, code, now)
	if !ok || step != now.Unix()/30 {
		t.Fatalf("current code rejected: %v %d", ok, step)
	}
	if _, ok := VerifyTOTP(secret, code[:3]+" "+code[3:], now.Add(30*time.Second)); !ok {
		t.Fatal("one step of drift (and a space) rejected")
	}
	if _, ok := VerifyTOTP(secret, code, now.Add(90*time.Second)); ok {
		t.Fatal("stale code accepted")
	}
	if _, ok := VerifyTOTP(secret, "000000", now); ok && code != "000000" {
		t.Fatal("wrong code accepted")
	}
	uri := TOTPURI(secret, "me@example.com", "PGDock")
	if !strings.HasPrefix(uri, "otpauth://totp/PGDock:me@example.com?") || !strings.Contains(uri, "secret="+secret) {
		t.Fatalf("uri %q", uri)
	}
}

func TestToken(t *testing.T) {
	a, ha, _ := newToken()
	b, hb, _ := newToken()
	if a == b || ha == hb || len(a) != 43 || len(ha) != 64 || hashToken(a) != ha || strings.Contains(ha, a) {
		t.Fatal("bad tokens")
	}
}
