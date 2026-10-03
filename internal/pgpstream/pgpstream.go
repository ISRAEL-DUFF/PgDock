// Package pgpstream encrypts and decrypts backup objects as standard
// OpenPGP messages (RFC 4880 SEIPD with MDC), so a project's backups open
// with gpg and its downloaded key alone (V2 s6).
package pgpstream

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func readKey(armored string) (openpgp.EntityList, error) {
	el, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil {
		return nil, fmt.Errorf("read openpgp key: %w", err)
	}
	if len(el) != 1 {
		return nil, fmt.Errorf("read openpgp key: want one key, got %d", len(el))
	}
	return el, nil
}

// config keeps messages readable by every OpenPGP implementation: AES-256,
// no AEAD (v1 SEIPD with MDC), no compression (pg_dump -Fc compresses).
var config = &packet.Config{DefaultCipher: packet.CipherAES256, DefaultCompressionAlgo: packet.CompressionNone}

// Encrypt returns a writer that encrypts to the armored public (or
// private) key and writes the binary OpenPGP message to w. Close it to
// finish the message; it does not close w.
func Encrypt(w io.Writer, armoredKey string) (io.WriteCloser, error) {
	el, err := readKey(armoredKey)
	if err != nil {
		return nil, err
	}
	return openpgp.Encrypt(w, el, nil, &openpgp.FileHints{IsBinary: true}, config)
}

// Decrypt returns the plaintext of the OpenPGP message in r, decrypted with
// the armored private key. The integrity check runs at the end: a reader
// that reaches io.EOF without error read an intact message.
func Decrypt(r io.Reader, armoredPrivate string) (io.Reader, error) {
	el, err := readKey(armoredPrivate)
	if err != nil {
		return nil, err
	}
	md, err := openpgp.ReadMessage(r, el, nil, config)
	if err != nil {
		return nil, fmt.Errorf("open openpgp message: %w", err)
	}
	if !md.IsEncrypted {
		return nil, errors.New("the object is not an encrypted OpenPGP message")
	}
	return md.UnverifiedBody, nil
}
