// Package backupfmt is PGDock's encrypted backup object format (spec §6.3:
// backups are encrypted on the client before upload).
//
// An object is self-describing, so it can be restored with nothing but the
// backup key, even if the metadata DB is lost:
//
//	magic        "PGDKBK1\n"                       8 bytes
//	wrapped len  uint16 big-endian                 2 bytes
//	wrapped key  AES-256-GCM(backup key, file key) nonce(12) | sealed(32+16)
//	nonce prefix random                            8 bytes
//	chunks       AES-256-GCM(file key, plaintext)  each ChunkSize+16 bytes,
//	                                               the last may be shorter
//
// Each object gets a fresh random file key. Chunk i uses the nonce
// prefix | uint32(i); its associated data is the header plus a flag byte
// marking the final chunk, so truncation, reordering, and splicing between
// objects are all detected (the STREAM construction).
package backupfmt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	magic     = "PGDKBK1\n"
	keySize   = 32
	prefixLen = 8
	// ChunkSize is the plaintext size of every chunk but the last.
	ChunkSize = 64 * 1024
	tagSize   = 16
)

// ErrCorrupt means an object failed authentication: it was truncated,
// modified, or does not belong to this key.
var ErrCorrupt = errors.New("backupfmt: object is corrupt or was tampered with")

// ErrWrongKey means the backup key cannot unwrap this object's file key.
var ErrWrongKey = errors.New("backupfmt: the backup key does not match this object")

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("backupfmt: key must be %d bytes", keySize)
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// NewFileKey returns a random per-object key and the same key wrapped with
// backupKey, for the header (and the metadata DB, so the control plane can
// hand the plain file key to an agent without sharing the backup key).
func NewFileKey(backupKey []byte) (fileKey, wrapped []byte, err error) {
	fileKey = make([]byte, keySize)
	if _, err := rand.Read(fileKey); err != nil {
		return nil, nil, err
	}
	wrapped, err = WrapKey(backupKey, fileKey)
	return fileKey, wrapped, err
}

// WrapKey seals fileKey with backupKey.
func WrapKey(backupKey, fileKey []byte) ([]byte, error) {
	a, err := newAEAD(backupKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, fileKey, []byte(magic)), nil
}

// UnwrapKey opens a key sealed by WrapKey.
func UnwrapKey(backupKey, wrapped []byte) ([]byte, error) {
	a, err := newAEAD(backupKey)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < a.NonceSize()+keySize+tagSize {
		return nil, ErrWrongKey
	}
	k, err := a.Open(nil, wrapped[:a.NonceSize()], wrapped[a.NonceSize():], []byte(magic))
	if err != nil {
		return nil, ErrWrongKey
	}
	return k, nil
}

// Writer encrypts a stream into the object format.
type Writer struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	prefix []byte
	buf    []byte
	n      uint32
	closed bool
	err    error
}

// NewWriter starts an object on w, sealed with fileKey; wrapped (from
// NewFileKey) goes into the header.
func NewWriter(w io.Writer, fileKey, wrapped []byte) (*Writer, error) {
	a, err := newAEAD(fileKey)
	if err != nil {
		return nil, err
	}
	if len(wrapped) > 0xffff {
		return nil, errors.New("backupfmt: wrapped key too long")
	}
	prefix := make([]byte, prefixLen)
	if _, err := rand.Read(prefix); err != nil {
		return nil, err
	}
	var h bytes.Buffer
	h.WriteString(magic)
	_ = binary.Write(&h, binary.BigEndian, uint16(len(wrapped)))
	h.Write(wrapped)
	h.Write(prefix)
	if _, err := w.Write(h.Bytes()); err != nil {
		return nil, err
	}
	return &Writer{w: w, aead: a, header: h.Bytes(), prefix: prefix, buf: make([]byte, 0, ChunkSize)}, nil
}

func (z *Writer) nonce(i uint32) []byte {
	n := make([]byte, 12)
	copy(n, z.prefix)
	binary.BigEndian.PutUint32(n[8:], i)
	return n
}

func aad(header []byte, final bool) []byte {
	out := make([]byte, len(header)+1)
	copy(out, header)
	if final {
		out[len(header)] = 1
	}
	return out
}

func (z *Writer) flush(final bool) error {
	if z.n == ^uint32(0) {
		return errors.New("backupfmt: object too large")
	}
	sealed := z.aead.Seal(nil, z.nonce(z.n), z.buf, aad(z.header, final))
	z.n++
	z.buf = z.buf[:0]
	_, err := z.w.Write(sealed)
	return err
}

// Write encrypts p.
func (z *Writer) Write(p []byte) (int, error) {
	if z.err != nil {
		return 0, z.err
	}
	if z.closed {
		return 0, errors.New("backupfmt: write after close")
	}
	total := len(p)
	for len(p) > 0 {
		// A full buffer is flushed only when more data follows, so the
		// final chunk is always the one Close writes.
		if len(z.buf) == ChunkSize {
			if z.err = z.flush(false); z.err != nil {
				return total - len(p), z.err
			}
		}
		n := copy(z.buf[len(z.buf):ChunkSize], p)
		z.buf = z.buf[:len(z.buf)+n]
		p = p[n:]
	}
	return total, nil
}

// Close writes the final chunk. It does not close the underlying writer.
func (z *Writer) Close() error {
	if z.closed {
		return z.err
	}
	z.closed = true
	if z.err != nil {
		return z.err
	}
	z.err = z.flush(true)
	return z.err
}

// ReadHeader reads an object's header and returns its wrapped file key,
// for unwrapping with the backup key.
func ReadHeader(r io.Reader) (wrapped []byte, header []byte, err error) {
	m := make([]byte, len(magic))
	if _, err := io.ReadFull(r, m); err != nil || string(m) != magic {
		return nil, nil, errors.New("backupfmt: not a PGDock backup object")
	}
	var wl uint16
	if err := binary.Read(r, binary.BigEndian, &wl); err != nil {
		return nil, nil, ErrCorrupt
	}
	wrapped = make([]byte, wl)
	if _, err := io.ReadFull(r, wrapped); err != nil {
		return nil, nil, ErrCorrupt
	}
	prefix := make([]byte, prefixLen)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, nil, ErrCorrupt
	}
	var h bytes.Buffer
	h.WriteString(magic)
	_ = binary.Write(&h, binary.BigEndian, wl)
	h.Write(wrapped)
	h.Write(prefix)
	return wrapped, h.Bytes(), nil
}

// Reader decrypts an object. Read returns ErrCorrupt as soon as any chunk
// fails to authenticate, and at EOF if the final chunk is missing.
type Reader struct {
	r      io.Reader
	aead   cipher.AEAD
	header []byte
	prefix []byte
	n      uint32
	plain  []byte
	next   []byte // ciphertext read ahead, to know which chunk is last
	done   bool
	err    error
}

// NewReader decrypts an object from r with fileKey. Use OpenWithBackupKey
// when only the backup key is known.
func NewReader(r io.Reader, fileKey []byte) (*Reader, error) {
	_, header, err := ReadHeader(r)
	if err != nil {
		return nil, err
	}
	return newReader(r, fileKey, header)
}

// OpenWithBackupKey decrypts an object using the backup key alone.
func OpenWithBackupKey(r io.Reader, backupKey []byte) (*Reader, error) {
	wrapped, header, err := ReadHeader(r)
	if err != nil {
		return nil, err
	}
	fileKey, err := UnwrapKey(backupKey, wrapped)
	if err != nil {
		return nil, err
	}
	return newReader(r, fileKey, header)
}

func newReader(r io.Reader, fileKey, header []byte) (*Reader, error) {
	a, err := newAEAD(fileKey)
	if err != nil {
		return nil, err
	}
	z := &Reader{r: r, aead: a, header: header, prefix: header[len(header)-prefixLen:]}
	z.next, z.err = z.readChunk()
	return z, nil
}

func (z *Reader) readChunk() ([]byte, error) {
	buf := make([]byte, ChunkSize+tagSize)
	n, err := io.ReadFull(z.r, buf)
	switch {
	case err == nil, errors.Is(err, io.ErrUnexpectedEOF):
		return buf[:n], nil
	case errors.Is(err, io.EOF):
		return nil, nil
	default:
		return nil, err
	}
}

func (z *Reader) Read(p []byte) (int, error) {
	for len(z.plain) == 0 {
		if z.err != nil {
			return 0, z.err
		}
		if z.done {
			return 0, io.EOF
		}
		cur := z.next
		if cur == nil {
			z.err = ErrCorrupt // stream ended without its final chunk
			return 0, z.err
		}
		// Peek at the next chunk to know whether cur is the last.
		var next []byte
		if len(cur) == ChunkSize+tagSize {
			next, z.err = z.readChunk()
			if z.err != nil {
				return 0, z.err
			}
		}
		final := next == nil
		nonce := make([]byte, 12)
		copy(nonce, z.prefix)
		binary.BigEndian.PutUint32(nonce[8:], z.n)
		pt, err := z.aead.Open(nil, nonce, cur, aad(z.header, final))
		if err != nil {
			z.err = ErrCorrupt
			return 0, z.err
		}
		z.n++
		z.plain, z.next, z.done = pt, next, final
	}
	n := copy(p, z.plain)
	z.plain = z.plain[n:]
	return n, nil
}
