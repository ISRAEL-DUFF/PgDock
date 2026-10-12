package edge

import (
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"testing"
)

// A failed object-store Put used to be reported to the caller as
// "incomplete_body" whenever the body hadn't been read to its end, which
// hid every storage failure (an unreachable store, a bad credential) behind
// a message blaming the client. Only a source that stopped early is the
// client's.
func TestCountingHashTellsAShortBodyFromAStoreFailure(t *testing.T) {
	const size = 100
	mk := func(r io.Reader) *countingHash { return &countingHash{r: io.LimitReader(r, size), h: sha256.New()} }

	// The store failed at once: the body was never touched.
	if ch := mk(strings.NewReader(strings.Repeat("x", size))); ch.cutShort(size) {
		t.Error("an unread body must not look cut short")
	}

	// The client sent less than it declared.
	ch := mk(strings.NewReader("only ten b"))
	_, _ = io.ReadAll(ch)
	if !ch.cutShort(size) {
		t.Error("a body that ended early should be cut short")
	}

	// The client went away mid-body.
	ch = mk(io.MultiReader(strings.NewReader("part"), errReader{}))
	_, _ = io.ReadAll(ch)
	if !ch.cutShort(size) {
		t.Error("a read error before the end should be cut short")
	}

	// Everything was read, then the store failed: not the client's doing.
	ch = mk(strings.NewReader(strings.Repeat("x", size)))
	_, _ = io.ReadAll(ch)
	if ch.cutShort(size) {
		t.Error("a fully read body must not look cut short")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
