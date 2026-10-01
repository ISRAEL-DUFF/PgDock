package walg

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

func TestPGPKeyDeterministicAndUsable(t *testing.T) {
	bk := bytes.Repeat([]byte{7}, 32)
	a, err := PGPKey(bk)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := PGPKey(bk)
	if a != b {
		t.Fatal("the same backup key must yield the same OpenPGP key")
	}
	other, _ := PGPKey(bytes.Repeat([]byte{8}, 32))
	if other == a {
		t.Fatal("different backup keys must yield different OpenPGP keys")
	}
	ents, err := openpgp.ReadArmoredKeyRing(strings.NewReader(a))
	if err != nil {
		t.Fatal(err)
	}
	var ct bytes.Buffer
	w, err := openpgp.Encrypt(&ct, ents, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("wal segment"))
	_ = w.Close()
	md, err := openpgp.ReadMessage(&ct, ents, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(md.UnverifiedBody)
	if string(got) != "wal segment" {
		t.Fatalf("round trip: %q", got)
	}
}
