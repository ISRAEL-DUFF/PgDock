package backup

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/israel-duff/pgdock/internal/backupfmt"
	"github.com/israel-duff/pgdock/internal/pgpstream"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
)

// ErrNotExportable: only a project's finished logical backups (nightly,
// manual, final, safety) are pg_dump archives.
var ErrNotExportable = errors.New("only a project's finished logical backups can be downloaded")

// Export writes backup b to w as the plain pg_dump archive it holds,
// decrypting it on the way (V2 §10.10 data export).
func (s *Service) Export(ctx context.Context, b store.Backup, w io.Writer) error {
	if b.ProjectID == nil || b.Status != "succeeded" || (b.Kind != Logical && b.Kind != Final && b.Kind != Safety) {
		return ErrNotExportable
	}
	d, err := s.opening(ctx, b)
	if err != nil {
		return err
	}
	c, err := storage.New(d.Storage)
	if err != nil {
		return err
	}
	body, err := c.Download(ctx, b.ObjectKey)
	if err != nil {
		return err
	}
	defer body.Close()
	var r io.Reader
	if d.PGPPrivateKey != "" {
		r, err = pgpstream.Decrypt(body, d.PGPPrivateKey)
	} else {
		r, err = backupfmt.NewReader(body, d.FileKey)
	}
	if err != nil {
		return fmt.Errorf("open backup %s: %w", b.ID, err)
	}
	_, err = io.Copy(w, r)
	return err
}
