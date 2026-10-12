// Package legal keeps PGDock's legal documents (V3 §7.3) that
// organisations accept: the service level agreement, the data processing
// agreement, and an organisation's own order form. Each is versioned; an
// owner accepts the version in effect for the organisation, and the
// acceptance is recorded. The terms, privacy notice and acceptable use
// policy are the users' own, in the auth package.
package legal

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/store"
)

// Kinds.
const (
	SLA       = "sla"
	DPA       = "dpa"
	OrderForm = "order_form"
)

// Errors.
var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("not found")
	ErrStale    = errors.New("a newer version is in effect")
)

// Service keeps the documents.
type Service struct{ db *pgxpool.Pool }

// New returns the service.
func New(db *pgxpool.Pool) *Service { return &Service{db: db} }

// EnsureDefaults publishes the default SLA and DPA as version 1 when there
// are none.
func (s *Service) EnsureDefaults(ctx context.Context) error {
	for kind, d := range map[string][2]string{SLA: {"Service level agreement", DefaultSLA}, DPA: {"Data processing agreement", DefaultDPA}} {
		if _, err := store.New(s.db).CurrentLegalDocument(ctx, kind); errors.Is(err, pgx.ErrNoRows) {
			if _, err := s.Publish(ctx, kind, nil, d[0], d[1], nil); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

// Publish publishes a new version of kind (an order form for org).
func (s *Service) Publish(ctx context.Context, kind string, org *uuid.UUID, title, body string, by *uuid.UUID) (store.LegalDocument, error) {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	switch {
	case kind != SLA && kind != DPA && kind != OrderForm:
		return store.LegalDocument{}, fmt.Errorf("%w: kind is sla, dpa or order_form", ErrInvalid)
	case (kind == OrderForm) != (org != nil):
		return store.LegalDocument{}, fmt.Errorf("%w: an order form, and only an order form, belongs to an organisation", ErrInvalid)
	case title == "" || body == "":
		return store.LegalDocument{}, fmt.Errorf("%w: a title and a text are required", ErrInvalid)
	case len(title) > 200 || len(body) > 200_000:
		return store.LegalDocument{}, fmt.Errorf("%w: the document is too long", ErrInvalid)
	}
	return store.New(s.db).InsertLegalDocument(ctx, store.InsertLegalDocumentParams{Kind: kind, OrgID: org, Title: title, BodyMd: body, PublishedBy: by})
}

// Current is the platform-wide document of kind in effect.
func (s *Service) Current(ctx context.Context, kind string) (store.LegalDocument, error) {
	d, err := store.New(s.db).CurrentLegalDocument(ctx, kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// OrgDocument is a document an organisation accepts, with its acceptance.
type OrgDocument struct {
	Doc        store.LegalDocument
	AcceptedAt *store.OrgLegalAcceptanceRow
}

// ForOrg returns the documents in effect for org: the SLA, the DPA, and
// its order form if it has one, each with org's acceptance.
func (s *Service) ForOrg(ctx context.Context, org uuid.UUID) ([]OrgDocument, error) {
	q := store.New(s.db)
	var docs []store.LegalDocument
	for _, k := range []string{SLA, DPA} {
		d, err := q.CurrentLegalDocument(ctx, k)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	if of, err := q.CurrentOrderForm(ctx, &org); err == nil {
		docs = append(docs, of)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out := make([]OrgDocument, 0, len(docs))
	for _, d := range docs {
		od := OrgDocument{Doc: d}
		if a, err := q.OrgLegalAcceptance(ctx, store.OrgLegalAcceptanceParams{DocumentID: d.ID, OrgID: org}); err == nil {
			od.AcceptedAt = &a
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		out = append(out, od)
	}
	return out, nil
}

// Outstanding reports whether org has documents in effect it hasn't
// accepted.
func (s *Service) Outstanding(ctx context.Context, org uuid.UUID) (bool, error) {
	docs, err := s.ForOrg(ctx, org)
	if err != nil {
		return false, err
	}
	for _, d := range docs {
		if d.AcceptedAt == nil {
			return true, nil
		}
	}
	return false, nil
}

// Accept records org's acceptance of document id, which must be the
// version in effect for it.
func (s *Service) Accept(ctx context.Context, org, id uuid.UUID, by *uuid.UUID, ip *netip.Addr) error {
	q := store.New(s.db)
	d, err := q.GetLegalDocument(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && d.OrgID != nil && *d.OrgID != org) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	cur, err := s.currentFor(ctx, d.Kind, org)
	if err != nil {
		return err
	}
	if cur.ID != d.ID {
		return fmt.Errorf("%w: accept version %d of the %s", ErrStale, cur.Version, cur.Title)
	}
	return q.AcceptLegalDocument(ctx, store.AcceptLegalDocumentParams{DocumentID: id, OrgID: org, UserID: by, Ip: ip})
}

// AcceptAll records org's acceptance of every document in effect for it.
func (s *Service) AcceptAll(ctx context.Context, org uuid.UUID, by *uuid.UUID, ip *netip.Addr) error {
	docs, err := s.ForOrg(ctx, org)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if d.AcceptedAt == nil {
			if err := s.Accept(ctx, org, d.Doc.ID, by, ip); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) currentFor(ctx context.Context, kind string, org uuid.UUID) (store.LegalDocument, error) {
	if kind == OrderForm {
		return store.New(s.db).CurrentOrderForm(ctx, &org)
	}
	return store.New(s.db).CurrentLegalDocument(ctx, kind)
}
