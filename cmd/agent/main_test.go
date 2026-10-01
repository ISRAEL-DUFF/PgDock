package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/israel-duff/pgdock/internal/backupfmt"
)

func TestDecrypt(t *testing.T) {
	dir := t.TempDir()
	bk := make([]byte, 32)
	_, _ = rand.Read(bk)
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("# comment\n"+backupfmt.EncodeKey(bk)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain := bytes.Repeat([]byte("PGDMP archive bytes "), 10000)
	fk, wrapped, err := backupfmt.NewFileKey(bk)
	if err != nil {
		t.Fatal(err)
	}
	var enc bytes.Buffer
	w, err := backupfmt.NewWriter(&enc, fk, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "x.dump.enc")
	if err := os.WriteFile(in, enc.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "x.dump")
	if err := run([]string{"decrypt", "--key-file", keyFile, "--in", in, "--out", out}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, plain) {
		t.Fatal("decrypted bytes differ")
	}

	// The wrong key fails and leaves no partial output.
	_, _ = rand.Read(bk)
	_ = os.WriteFile(keyFile, []byte(backupfmt.EncodeKey(bk)), 0o600)
	out2 := filepath.Join(dir, "y.dump")
	if err := run([]string{"decrypt", "--key-file", keyFile, "--in", in, "--out", out2}); err == nil {
		t.Fatal("decrypted with the wrong key")
	}
	if _, err := os.Stat(out2); !os.IsNotExist(err) {
		t.Fatal("left a partial output behind")
	}
}
