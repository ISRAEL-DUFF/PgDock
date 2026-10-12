package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Event outcomes.
const (
	OutcomePosted    = "posted"
	OutcomeDuplicate = "duplicate"
	OutcomeUnmatched = "unmatched" // verified money with no org to attribute it to
	OutcomeRejected  = "rejected"  // failed re-verification
	OutcomeFailed    = "failed"    // an error while processing; retried by re-query
	OutcomeIgnored   = "ignored"
)

func newReference(prefix string) string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// EnsureVirtualAccount returns orgID's transfer account, issuing it from the
// wallet provider if the org has none yet (V3 §3.4.3). The org needs a
// wallet first: the account is the wallet's.
func (s *Service) EnsureVirtualAccount(ctx context.Context, orgID uuid.UUID) (store.VirtualAccount, error) {
	if s.wallets == nil {
		return store.VirtualAccount{}, invalid("bank transfer accounts aren't available")
	}
	q := store.New(s.db)
	have, err := q.ListVirtualAccounts(ctx, orgID)
	if err != nil {
		return store.VirtualAccount{}, err
	}
	name := s.wallets.Name()
	for _, v := range have {
		if v.Provider == name {
			return v, nil
		}
	}
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return store.VirtualAccount{}, err
	}
	wid := walletID(a)
	if wid == "" {
		return store.VirtualAccount{}, fmt.Errorf("%w: %w", ErrInvalid, ErrWalletSetup)
	}
	va, err := s.wallets.IssueVirtualAccount(ctx, Customer{OrgID: orgID, ProviderID: wid})
	if err != nil {
		s.log.Warn("virtual account", "provider", name, "org", orgID, "err", err)
		return store.VirtualAccount{}, err
	}
	var ref *string
	if va.ProviderRef != "" {
		ref = &va.ProviderRef
	}
	return q.InsertVirtualAccount(ctx, store.InsertVirtualAccountParams{
		OrgID: orgID, Provider: name, AccountNumber: va.AccountNumber, BankName: va.BankName, AccountName: va.AccountName, ProviderRef: ref,
	})
}

// ---- Events ------------------------------------------------------------------------

// HandleEvent records a provider event and acts on it (V3 §3.4.8): it is
// stored once (duplicates are no-ops), and money is posted only after the
// provider's verify endpoint confirms it.
func (s *Service) HandleEvent(ctx context.Context, provider string, ev Event) (string, error) {
	q := store.New(s.db)
	raw := ev.Raw
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var pref, cur *string
	var amt *int64
	if ev.ProviderRef != "" {
		pref = &ev.ProviderRef
	}
	if ev.Currency != "" {
		cur = &ev.Currency
	}
	if ev.AmountMinor != 0 {
		amt = &ev.AmountMinor
	}
	row, err := q.InsertPaymentEvent(ctx, store.InsertPaymentEventParams{
		Provider: provider, ProviderEventID: ev.ID, Kind: ev.Kind, ProviderRef: pref, AmountMinor: amt, Currency: cur, Payload: raw,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OutcomeDuplicate, nil
	}
	if err != nil {
		return "", err
	}
	outcome, perr := s.processEvent(ctx, provider, ev)
	var msg *string
	if perr != nil {
		m := perr.Error()
		msg = &m
		if outcome == "" {
			outcome = OutcomeFailed
		}
	}
	if err := q.FinishPaymentEvent(ctx, store.FinishPaymentEventParams{ID: row.ID, Outcome: &outcome, Error: msg}); err != nil {
		return outcome, err
	}
	return outcome, perr
}

func (s *Service) processEvent(ctx context.Context, provider string, ev Event) (string, error) {
	p, ok := s.providers[provider]
	if !ok {
		return OutcomeRejected, fmt.Errorf("provider %s isn't configured", provider)
	}
	if ev.Kind != EventPaymentSucceeded && ev.Kind != EventTransferReceived {
		return OutcomeIgnored, nil
	}
	if err := s.settleVerified(ctx, p, ev.ProviderRef); err != nil {
		var um unmatchedError
		if errors.As(err, &um) {
			return OutcomeUnmatched, nil
		}
		var rj rejectedError
		if errors.As(err, &rj) {
			return OutcomeRejected, err
		}
		return OutcomeFailed, err
	}
	return OutcomePosted, nil
}

type unmatchedError struct{ ref string }

func (e unmatchedError) Error() string { return "no organisation for transaction " + e.ref }

type rejectedError struct{ why string }

func (e rejectedError) Error() string { return "verification failed: " + e.why }

// settleVerified looks providerRef up at the provider and, if it is a
// successful naira payment, records it for the org whose virtual account it
// was paid into.
func (s *Service) settleVerified(ctx context.Context, p PaymentProvider, providerRef string) error {
	if providerRef == "" {
		return rejectedError{"no transaction reference"}
	}
	tx, err := p.Verify(ctx, providerRef)
	if err != nil {
		return err
	}
	switch {
	case tx.Status != TxSucceeded:
		return rejectedError{fmt.Sprintf("the provider says %s", tx.Status)}
	case !strings.EqualFold(tx.Currency, "NGN"):
		return rejectedError{"currency " + tx.Currency}
	case tx.AmountMinor <= 0:
		return rejectedError{"no amount"}
	case tx.AccountNumber == "":
		return unmatchedError{tx.ProviderRef}
	}
	// Only the provider's own record says whose money it is: a transfer into
	// a virtual account goes to that account's organisation, whatever the
	// event claimed (M27 review).
	va, err := store.New(s.db).VirtualAccountByNumber(ctx, store.VirtualAccountByNumberParams{Provider: p.Name(), AccountNumber: tx.AccountNumber})
	if err != nil {
		return unmatchedError{tx.ProviderRef}
	}
	_, err = s.RecordPayment(ctx, PaymentIn{OrgID: va.OrgID, Provider: p.Name(), Channel: ChannelTransfer, ProviderRef: tx.ProviderRef,
		AmountMinor: tx.AmountMinor, FeeMinor: tx.FeeMinor, ReceivedAt: tx.At})
	return err
}

// ---- Missed events ---------------------------------------------------------------

// Requery asks each provider for its transactions in [from, to) and posts
// any successful one not yet recorded, and resolves wallet debits still pending
// (V3 §3.4.8: hourly for recent ones, nightly for the previous day).
func (s *Service) Requery(ctx context.Context, from, to time.Time) (int, error) {
	q := store.New(s.db)
	n := 0
	var errs []error
	for name, p := range s.providers {
		txs, err := p.ListTransactions(ctx, from, to)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		for _, tx := range txs {
			if tx.Status != TxSucceeded {
				continue
			}
			if _, err := q.GetPaymentByProviderRef(ctx, store.GetPaymentByProviderRefParams{Provider: name, ProviderRef: tx.ProviderRef}); err == nil {
				continue
			}
			kind := EventPaymentSucceeded
			if tx.Channel == ChannelTransfer {
				kind = EventTransferReceived
			}
			out, err := s.HandleEvent(ctx, name, Event{ID: "requery:" + tx.ProviderRef, Kind: kind, ProviderRef: tx.ProviderRef,
				AmountMinor: tx.AmountMinor, Currency: tx.Currency})
			if err != nil {
				errs = append(errs, err)
			}
			if out == OutcomePosted {
				n++
			}
		}
	}
	if s.wallets == nil {
		return n, errors.Join(errs...)
	}
	// A debit of a wallet that got no answer: its key says what happened.
	pending, err := q.PendingIntents(ctx, store.PendingIntentsParams{Provider: s.wallets.Name(), Since: from})
	if err != nil {
		return n, errors.Join(append(errs, err)...)
	}
	for _, in := range pending {
		if _, err := s.resolveWalletIntent(ctx, in); err != nil {
			errs = append(errs, err)
		}
	}
	return n, errors.Join(errs...)
}

// AttributeEvent settles an unmatched event's money for orgID (the admin,
// after identifying the sender). The transaction is verified again first.
func (s *Service) AttributeEvent(ctx context.Context, eventID int64, orgID uuid.UUID, by *uuid.UUID) (Settlement, error) {
	q := store.New(s.db)
	ev, err := q.GetPaymentEvent(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settlement{}, ErrNotFound
	}
	if err != nil {
		return Settlement{}, err
	}
	if ev.Outcome == nil || *ev.Outcome != OutcomeUnmatched || ev.ProviderRef == nil {
		return Settlement{}, fmt.Errorf("%w: only unmatched events can be attributed", ErrConflict)
	}
	p, ok := s.providers[ev.Provider]
	if !ok {
		return Settlement{}, fmt.Errorf("provider %s isn't configured", ev.Provider)
	}
	tx, err := p.Verify(ctx, *ev.ProviderRef)
	if err != nil {
		return Settlement{}, err
	}
	if tx.Status != TxSucceeded || !strings.EqualFold(tx.Currency, "NGN") {
		return Settlement{}, fmt.Errorf("%w: the provider says %s %s", ErrConflict, tx.Status, tx.Currency)
	}
	st, err := s.RecordPayment(ctx, PaymentIn{OrgID: orgID, Provider: ev.Provider, Channel: ChannelTransfer, ProviderRef: tx.ProviderRef,
		AmountMinor: tx.AmountMinor, FeeMinor: tx.FeeMinor, ReceivedAt: tx.At, RecordedBy: by, Note: "attributed by the platform admin"})
	if err != nil {
		return st, err
	}
	outcome := OutcomePosted
	return st, q.FinishPaymentEvent(ctx, store.FinishPaymentEventParams{ID: ev.ID, Outcome: &outcome, OrgID: &orgID})
}
