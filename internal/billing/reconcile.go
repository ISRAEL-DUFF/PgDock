package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Reconciliation compares the ledger's payments with a provider's
// transactions for a period (V3 §3.3: nightly, flagging differences).
type Reconciliation struct {
	Provider    string       `json:"provider"`
	From        time.Time    `json:"from"`
	To          time.Time    `json:"to"`
	Matched     int          `json:"matched"`
	GrossMinor  int64        `json:"gross_minor"`
	FeesMinor   int64        `json:"fees_minor"`
	Differences []Difference `json:"differences"`
	Error       string       `json:"error,omitempty"`
	RanAt       time.Time    `json:"ran_at"`
}

// Difference is one thing that doesn't match.
type Difference struct {
	Kind        string `json:"kind"` // missing_in_pgdock, missing_at_provider, amount, fee
	ProviderRef string `json:"provider_ref"`
	Provider    int64  `json:"provider_minor"`
	PGDock      int64  `json:"pgdock_minor"`
	Note        string `json:"note,omitempty"`
}

const reconcileKey = "billing.reconciliation"

// Reconcile compares each provider's successful transactions in [from, to)
// with the payments recorded for them, and stores the result.
func (s *Service) Reconcile(ctx context.Context, from, to time.Time) ([]Reconciliation, error) {
	q := store.New(s.db)
	names := make([]string, 0, len(s.providers))
	for n := range s.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Reconciliation
	var errs []error
	for _, name := range names {
		r := Reconciliation{Provider: name, From: from, To: to, Differences: []Difference{}, RanAt: s.Now()}
		txs, err := s.providers[name].ListTransactions(ctx, from, to)
		if err != nil {
			r.Error = err.Error()
			out = append(out, r)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		seen := map[string]bool{}
		for _, tx := range txs {
			if tx.Status != TxSucceeded {
				continue
			}
			seen[tx.ProviderRef] = true
			p, err := q.GetPaymentByProviderRef(ctx, store.GetPaymentByProviderRefParams{Provider: name, ProviderRef: tx.ProviderRef})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				r.Differences = append(r.Differences, Difference{Kind: "missing_in_pgdock", ProviderRef: tx.ProviderRef, Provider: tx.AmountMinor})
				continue
			case err != nil:
				return out, err
			}
			switch {
			case p.AmountMinor != tx.AmountMinor:
				r.Differences = append(r.Differences, Difference{Kind: "amount", ProviderRef: tx.ProviderRef, Provider: tx.AmountMinor, PGDock: p.AmountMinor})
			case p.FeeMinor != tx.FeeMinor:
				r.Differences = append(r.Differences, Difference{Kind: "fee", ProviderRef: tx.ProviderRef, Provider: tx.FeeMinor, PGDock: p.FeeMinor})
			default:
				r.Matched++
				r.GrossMinor += p.AmountMinor
				r.FeesMinor += p.FeeMinor
			}
		}
		recorded, err := q.ListPayments(ctx, store.ListPaymentsParams{Provider: &name, FromTs: from, ToTs: to, Lim: 100000})
		if err != nil {
			return out, err
		}
		for _, p := range recorded {
			if !seen[p.ProviderRef] {
				r.Differences = append(r.Differences, Difference{Kind: "missing_at_provider", ProviderRef: p.ProviderRef, PGDock: p.AmountMinor})
			}
		}
		out = append(out, r)
		if len(r.Differences) > 0 {
			s.log.Warn("billing: reconciliation differences", "provider", name, "from", from, "differences", len(r.Differences))
		}
	}
	raw, _ := json.Marshal(out)
	if err := q.PutSetting(ctx, store.PutSettingParams{Key: reconcileKey, Value: raw}); err != nil {
		errs = append(errs, err)
	}
	return out, errors.Join(errs...)
}

// LastReconciliation is the latest stored result.
func (s *Service) LastReconciliation(ctx context.Context) ([]Reconciliation, error) {
	raw, err := store.New(s.db).GetSetting(ctx, reconcileKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Reconciliation
	err = json.Unmarshal(raw, &out)
	return out, err
}
