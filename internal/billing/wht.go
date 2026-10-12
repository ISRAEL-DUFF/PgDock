package billing

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// MaxDocumentBytes is the largest proof of payment or WHT credit note.
const MaxDocumentBytes = 10 << 20

var documentTypes = map[string]bool{"application/pdf": true, "image/png": true, "image/jpeg": true}

// readDocument reads an upload, refusing what isn't a PDF or an image.
func readDocument(r io.Reader) ([]byte, string, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxDocumentBytes {
		return nil, "", invalid("documents are at most 10 MB")
	}
	kind := http.DetectContentType(b)
	if !documentTypes[kind] {
		return nil, "", invalid("documents are PDF, PNG or JPEG (this is %s)", kind)
	}
	return b, kind, nil
}

func safeName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, c := range name {
		if c < 0x20 || c == '"' || c == '/' {
			continue
		}
		b.WriteRune(c)
	}
	s := strings.TrimSpace(b.String())
	if s == "" || s == "." {
		s = "document"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// UploadWHTCertificate stores the WHT credit note for an invoice whose WHT
// was deducted, and marks the WHT evidenced (V3 §3.7).
func (s *Service) UploadWHTCertificate(ctx context.Context, orgID, invoiceID uuid.UUID, filename string, r io.Reader, by *uuid.UUID) (store.WhtCertificate, error) {
	if s.docs == nil {
		return store.WhtCertificate{}, invalid("document storage isn't configured")
	}
	q := store.New(s.db)
	inv, err := q.GetOrgInvoice(ctx, store.GetOrgInvoiceParams{ID: invoiceID, OrgID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.WhtCertificate{}, ErrNotFound
	}
	if err != nil {
		return store.WhtCertificate{}, err
	}
	if inv.WhtDeductedMinor == 0 {
		return store.WhtCertificate{}, fmt.Errorf("%w: no WHT was deducted on invoice %s", ErrConflict, deref(inv.Number))
	}
	b, _, err := readDocument(r)
	if err != nil {
		return store.WhtCertificate{}, err
	}
	name := safeName(filename)
	key := fmt.Sprintf("wht/%s/%s-%s", inv.ID, uuid.NewString()[:8], name)
	if err := s.docs.Put(ctx, key, bytes.NewReader(b)); err != nil {
		return store.WhtCertificate{}, err
	}
	var c store.WhtCertificate
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		c, err = q.InsertWHTCertificate(ctx, store.InsertWHTCertificateParams{InvoiceID: inv.ID, OrgID: orgID, ObjectKey: key, Filename: name, SizeBytes: int64(len(b)), UploadedBy: by})
		if err != nil {
			return err
		}
		_, err = q.SetWHTEvidenced(ctx, inv.ID)
		return err
	})
	return c, err
}

// OpenDocument opens a stored billing document.
func (s *Service) OpenDocument(ctx context.Context, key string) (io.ReadCloser, error) {
	if s.docs == nil {
		return nil, invalid("document storage isn't configured")
	}
	return s.docs.Get(ctx, key)
}

// StoreProof stores a proof of payment for a manual payment and returns
// its key.
func (s *Service) StoreProof(ctx context.Context, orgID uuid.UUID, filename string, r io.Reader) (string, error) {
	if s.docs == nil {
		return "", invalid("document storage isn't configured")
	}
	b, _, err := readDocument(r)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("proofs/%s/%s-%s", orgID, uuid.NewString()[:8], safeName(filename))
	return key, s.docs.Put(ctx, key, bytes.NewReader(b))
}

// OutstandingWHT is deducted WHT awaiting its credit note, oldest first.
func (s *Service) OutstandingWHT(ctx context.Context) ([]store.OutstandingWHTRow, error) {
	return store.New(s.db).OutstandingWHT(ctx)
}

// WHTReceivableCSV exports WHT receivable for tax filing: every invoice
// whose WHT was deducted, evidenced or not.
func (s *Service) WHTReceivableCSV(ctx context.Context, w io.Writer) error {
	rows, err := store.New(s.db).WHTDeducted(ctx)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"invoice", "organisation", "legal_name", "tin", "invoice_total_ngn", "wht_ngn", "paid_at", "evidenced_at", "age_days"})
	now := s.Now()
	for _, r := range rows {
		paid, ev, age := "", "", ""
		if r.PaidAt != nil {
			paid = r.PaidAt.UTC().Format(time.RFC3339)
			age = fmt.Sprint(daysSince(*r.PaidAt, now))
		}
		if r.WhtEvidencedAt != nil {
			ev = r.WhtEvidencedAt.UTC().Format(time.RFC3339)
		}
		_ = cw.Write([]string{deref(r.Number), r.OrgName, ptr(r.LegalName), ptr(r.Tin), money(r.TotalMinor), money(r.WhtDeductedMinor), paid, ev, age})
	}
	cw.Flush()
	return cw.Error()
}

func money(kobo int64) string {
	sign := ""
	if kobo < 0 {
		sign, kobo = "-", -kobo
	}
	return fmt.Sprintf("%s%d.%02d", sign, kobo/100, kobo%100)
}
