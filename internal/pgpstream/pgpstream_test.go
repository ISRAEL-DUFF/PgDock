package pgpstream_test

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/israel-duff/pgdock/internal/pgpstream"
	"github.com/israel-duff/pgdock/internal/walg"
)

func key(t *testing.T) (priv, pub string) {
	t.Helper()
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	priv, err := walg.ProjectPGPKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	pub, err = walg.PublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func encrypt(t *testing.T, pub string, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := pgpstream.Encrypt(&buf, pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRoundTrip(t *testing.T) {
	priv, pub := key(t)
	plain := make([]byte, 3<<20)
	_, _ = rand.Read(plain)
	r, err := pgpstream.Decrypt(bytes.NewReader(encrypt(t, pub, plain)), priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: err %v, equal %v", err, bytes.Equal(got, plain))
	}
}

func TestTamperedFails(t *testing.T) {
	priv, pub := key(t)
	msg := encrypt(t, pub, bytes.Repeat([]byte("pgdock"), 10000))
	msg[len(msg)-40] ^= 1
	r, err := pgpstream.Decrypt(bytes.NewReader(msg), priv)
	if err == nil {
		_, err = io.ReadAll(r)
	}
	if err == nil {
		t.Fatal("a tampered message decrypted without error")
	}
}

func TestWrongKeyFails(t *testing.T) {
	_, pub := key(t)
	other, _ := key(t)
	if _, err := pgpstream.Decrypt(bytes.NewReader(encrypt(t, pub, []byte("x"))), other); err == nil {
		t.Fatal("decrypted with the wrong key")
	}
}

// TestGPGDecrypts is the "standard tools" half of V2 s6: gpg, given only the
// derived private key, opens a message the agent would write.
func TestGPGDecrypts(t *testing.T) {
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("gpg not installed")
	}
	priv, pub := key(t)
	plain := []byte("PGDMP pretend archive\n")
	dir := t.TempDir()
	home := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "key.asc")
	msgFile := filepath.Join(dir, "backup.dump.gpg")
	_ = os.WriteFile(keyFile, []byte("README text before the key is ignored by gpg\n\n"+priv), 0o600)
	_ = os.WriteFile(msgFile, encrypt(t, pub, plain), 0o600)
	run := func(args ...string) []byte {
		cmd := exec.Command(gpg, append([]string{"--homedir", home, "--batch", "--quiet"}, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("gpg %v: %v\n%s", args, err, stderr.String())
		}
		return out
	}
	run("--import", keyFile)
	if got := run("--decrypt", msgFile); !bytes.Equal(got, plain) {
		t.Fatalf("gpg decrypted %q", got)
	}
}
