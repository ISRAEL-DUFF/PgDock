package backupfmt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func key(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

func seal(t *testing.T, backupKey, plain []byte, chunked int) ([]byte, []byte) {
	t.Helper()
	fk, wrapped, err := NewFileKey(backupKey)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	w, err := NewWriter(&out, fk, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(plain); i += chunked {
		end := min(i+chunked, len(plain))
		if _, err := w.Write(plain[i:end]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), fk
}

func TestRoundTrip(t *testing.T) {
	bk := key(t)
	for _, size := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3*ChunkSize + 17} {
		plain := make([]byte, size)
		_, _ = rand.Read(plain)
		for _, step := range []int{1 << 20, 1000} {
			obj, fk := seal(t, bk, plain, step)
			// With the file key (what agents get)...
			r, err := NewReader(bytes.NewReader(obj), fk)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("size %d step %d: %v (got %d bytes)", size, step, err, len(got))
			}
			// ...and with only the backup key (disaster recovery).
			r, err = OpenWithBackupKey(bytes.NewReader(obj), bk)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := io.ReadAll(r); err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("backup key: %v", err)
			}
		}
	}
}

func TestTamperingDetected(t *testing.T) {
	bk := key(t)
	plain := make([]byte, 2*ChunkSize+100)
	_, _ = rand.Read(plain)
	obj, fk := seal(t, bk, plain, 1<<20)

	read := func(b []byte) error {
		r, err := NewReader(bytes.NewReader(b), fk)
		if err != nil {
			return err
		}
		_, err = io.ReadAll(r)
		return err
	}
	// Flip one byte anywhere after the header.
	for _, off := range []int{len(obj) - 1, len(obj) / 2, 80} {
		bad := bytes.Clone(obj)
		bad[off] ^= 1
		if err := read(bad); err == nil {
			t.Errorf("flip at %d undetected", off)
		}
	}
	// Truncation at a chunk boundary (drops the final chunk).
	hdr := len(obj) - (2*(ChunkSize+tagSize) + 100 + tagSize)
	if err := read(obj[:hdr+2*(ChunkSize+tagSize)]); !errors.Is(err, ErrCorrupt) {
		t.Errorf("truncation: %v", err)
	}
	// Reordering chunks.
	sw := bytes.Clone(obj)
	a := sw[hdr : hdr+ChunkSize+tagSize]
	b := bytes.Clone(sw[hdr+ChunkSize+tagSize : hdr+2*(ChunkSize+tagSize)])
	copy(sw[hdr+ChunkSize+tagSize:], a)
	copy(sw[hdr:], b)
	if err := read(sw); !errors.Is(err, ErrCorrupt) {
		t.Errorf("reorder: %v", err)
	}
}

func TestWrongKey(t *testing.T) {
	obj, _ := seal(t, key(t), []byte("data"), 10)
	if _, err := OpenWithBackupKey(bytes.NewReader(obj), key(t)); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("got %v", err)
	}
	r, _ := NewReader(bytes.NewReader(obj), key(t))
	if _, err := io.ReadAll(r); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong file key: %v", err)
	}
	if _, err := NewReader(bytes.NewReader([]byte("PGDMP plain dump")), key(t)); err == nil {
		t.Fatal("non-object accepted")
	}
}

func TestObjectsDiffer(t *testing.T) {
	bk := key(t)
	a, _ := seal(t, bk, []byte("same"), 10)
	b, _ := seal(t, bk, []byte("same"), 10)
	if bytes.Equal(a, b) {
		t.Fatal("identical plaintext produced identical objects")
	}
}
