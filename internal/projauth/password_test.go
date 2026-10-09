package projauth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestBcryptPasswords(t *testing.T) {
	h, err := bcrypt.GenerateFromPassword([]byte("correct-horse-1"), 10)
	if err != nil {
		t.Fatal(err)
	}
	hs := string(h)
	u := &User{passwordHash: &hs}
	if !CheckPassword(u, "correct-horse-1") || CheckPassword(u, "wrong") {
		t.Fatal("a bcrypt hash isn't checked")
	}
	if !NeedsRehash(u) {
		t.Fatal("a bcrypt hash should be upgraded")
	}
	a, _ := HashPassword("x")
	if NeedsRehash(&User{passwordHash: &a}) || !strings.HasPrefix(a, "$argon2id$") {
		t.Fatal("an argon2id hash needs no rehash")
	}
	// A $2y$ hash (PHP's prefix) checks the same.
	y := "$2y$" + hs[4:]
	if !CheckPassword(&User{passwordHash: &y}, "correct-horse-1") {
		t.Fatal("$2y$ hash")
	}
}
