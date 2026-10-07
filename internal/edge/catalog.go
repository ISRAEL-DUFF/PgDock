package edge

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/datacat"
)

// The data API's view of a project's schema (V4 §3.1): the tables, views
// and their columns, keys and foreign keys in the exposed schemas, read
// from the catalog. It is cached per project and re-read when a cheap
// fingerprint of the catalog changes (checked at most every
// fingerprintEvery, inside a request's transaction).
const fingerprintEvery = 2 * time.Second

// catalogState is a project's cached catalog.
type catalogState struct {
	mu      sync.Mutex
	cat     *Catalog
	checked time.Time
	shapes  map[string]float64 // normalised query -> estimated cost
}

// catalog returns p's catalog, re-reading it in tx when it may be stale.
func (e *Edge) catalog(ctx context.Context, p *project, tx pgx.Tx) (*Catalog, *catalogState, error) {
	st := p.catalog
	st.mu.Lock()
	defer st.mu.Unlock()
	schemas := p.exposed()
	fresh := st.cat != nil && time.Since(st.checked) < fingerprintEvery && sameStrings(st.cat.Schemas, schemas)
	if fresh {
		return st.cat, st, nil
	}
	var fp string
	if err := tx.QueryRow(ctx, datacat.FingerprintSQL, schemas).Scan(&fp); err != nil {
		return nil, nil, err
	}
	st.checked = time.Now()
	if st.cat != nil && st.cat.Fingerprint == fp && sameStrings(st.cat.Schemas, schemas) {
		return st.cat, st, nil
	}
	cat, err := datacat.Introspect(ctx, tx, schemas)
	if err != nil {
		return nil, nil, err
	}
	cat.Fingerprint = fp
	st.cat, st.shapes = cat, map[string]float64{}
	return cat, st, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
