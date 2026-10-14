package billing

import (
	"context"
	"io"
)

// DocStore keeps billing documents: proofs of payment and WHT credit notes.
// The server backs it with the platform's backup storage.
type DocStore interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// SetDocStore configures where billing documents are kept.
func (s *Service) SetDocStore(d DocStore) { s.docs = d }
