package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// customer is orgID as providers see it.
func (s *Service) customer(ctx context.Context, provider string, orgID uuid.UUID) (Customer, error) {
	q := store.New(s.db)
	a, err := s.Account(ctx, orgID)
	if err != nil {
		return Customer{}, err
	}
	o, err := q.GetOrg(ctx, orgID)
	if err != nil {
		return Customer{}, err
	}
	c := Customer{OrgID: orgID, Name: o.Name}
	if a.LegalName != nil {
		c.Name = *a.LegalName
	}
	if to, err := q.BillingRecipients(ctx, orgID); err == nil && len(to) > 0 {
		c.Email = to[0]
	}
	var ids map[string]string
	_ = json.Unmarshal(a.ProviderCustomers, &ids)
	if id := ids[provider]; id != "" {
		c.ProviderID = id
		return c, nil
	}
	p, ok := s.providers[provider]
	if !ok {
		return c, fmt.Errorf("%w: payment provider %s isn't configured", ErrInvalid, provider)
	}
	id, err := p.CreateCustomer(ctx, c)
	if err != nil {
		return c, err
	}
	if id != "" {
		c.ProviderID = id
		if err := q.SetProviderCustomer(ctx, store.SetProviderCustomerParams{OrgID: orgID, Provider: provider, CustomerID: id}); err != nil {
			return c, err
		}
	}
	return c, nil
}

// ---- Starting payments --------------------------------------------------------

// CheckoutStart asks for a hosted payment.
type CheckoutStart struct {
	OrgID       uuid.UUID
	Channel     string // card, wallet, stablecoin
	Purpose     string // invoice, topup, card_setup
	InvoiceID   *uuid.UUID
	AmountMinor int64 // for a top-up; an invoice pays what is outstanding
	// MandateLimitMinor asks the wallet for a recurring mandate too.
	MandateLimitMinor int64
	RedirectURL       string
}

// StartCheckout creates a payment intent and the provider's hosted page.
func (s *Service) StartCheckout(ctx context.Context, c CheckoutStart) (store.PaymentIntent, error) {
	provider := ""
	switch c.Channel {
	case ChannelCard:
		provider = s.routing.Cards
	case ChannelWallet:
		provider = s.routing.Wallet
	case ChannelStablecoin:
		provider = s.routing.Wallet
		set, err := s.Settings(ctx)
		if err != nil {
			return store.PaymentIntent{}, err
		}
		if !set.Stablecoin {
			return store.PaymentIntent{}, invalid("stablecoin top-ups are off")
		}
		if c.Purpose != PurposeTopup {
			return store.PaymentIntent{}, invalid("stablecoin is for prepaid top-ups only (V3 §3.4.5)")
		}
	default:
		return store.PaymentIntent{}, invalid("channel is card, wallet or stablecoin")
	}
	p, ok := s.providers[provider]
	if provider == "" || !ok {
		return store.PaymentIntent{}, invalid("%s payments aren't available", c.Channel)
	}
	amount := c.AmountMinor
	desc := "PGDock top-up"
	switch c.Purpose {
	case PurposeInvoice:
		if c.InvoiceID == nil {
			return store.PaymentIntent{}, invalid("which invoice?")
		}
		q := store.New(s.db)
		inv, err := q.GetOrgInvoice(ctx, store.GetOrgInvoiceParams{ID: *c.InvoiceID, OrgID: c.OrgID})
		if errors.Is(err, pgx.ErrNoRows) {
			return store.PaymentIntent{}, ErrNotFound
		}
		if err != nil {
			return store.PaymentIntent{}, err
		}
		if inv.Status != StatusIssued && inv.Status != StatusPartiallyPaid {
			return store.PaymentIntent{}, fmt.Errorf("%w: invoice %s is %s", ErrConflict, deref(inv.Number), inv.Status)
		}
		if amount, err = outstanding(ctx, q, inv); err != nil {
			return store.PaymentIntent{}, err
		}
		desc = "PGDock invoice " + deref(inv.Number)
	case PurposeTopup:
		if amount < 100_00 {
			return store.PaymentIntent{}, invalid("a top-up is at least ₦100")
		}
	case PurposeCardSetup:
		amount = 100_00 // a ₦100 charge saves the card, and is kept as credit
		desc = "PGDock: save a card (₦100, kept as credit)"
	default:
		return store.PaymentIntent{}, invalid("purpose is invoice, topup or card_setup")
	}
	if amount <= 0 {
		return store.PaymentIntent{}, invalid("nothing to pay")
	}
	cust, err := s.customer(ctx, provider, c.OrgID)
	if err != nil {
		return store.PaymentIntent{}, err
	}
	q := store.New(s.db)
	intent, err := q.InsertPaymentIntent(ctx, store.InsertPaymentIntentParams{
		OrgID: c.OrgID, Reference: newReference("pgd"), Provider: provider, Channel: c.Channel, Purpose: c.Purpose,
		InvoiceID: c.InvoiceID, AmountMinor: amount,
	})
	if err != nil {
		return intent, err
	}
	sess, err := p.Checkout(ctx, CheckoutRequest{
		Reference: intent.Reference, AmountMinor: amount, Customer: cust, Channel: c.Channel, Purpose: c.Purpose,
		Description: desc, RedirectURL: c.RedirectURL, MandateLimitMinor: c.MandateLimitMinor,
	})
	if err != nil {
		msg := err.Error()
		_ = q.FinishPaymentIntent(ctx, store.FinishPaymentIntentParams{Reference: intent.Reference, Status: "failed", Error: &msg})
		return intent, err
	}
	url := sess.URL
	if err := q.SetIntentCheckout(ctx, store.SetIntentCheckoutParams{ID: intent.ID, CheckoutUrl: &url}); err != nil {
		return intent, err
	}
	intent.CheckoutUrl = &url
	return intent, nil
}

// ChargeOutcome is what an automatic charge did.
type ChargeOutcome struct {
	Intent    store.PaymentIntent
	Succeeded bool
	// Unavailable is a provider outage: not the customer's failure.
	Unavailable bool
	Message     string
}

// ChargeMethod charges a saved card or mandate for amount (an invoice, or
// an auto top-up when invoice is nil).
func (s *Service) ChargeMethod(ctx context.Context, m store.PaymentMethod, invoiceID *uuid.UUID, amount int64) (ChargeOutcome, error) {
	p, ok := s.providers[m.Provider]
	if !ok {
		return ChargeOutcome{}, fmt.Errorf("payment provider %s isn't configured", m.Provider)
	}
	channel, purpose := ChannelSavedCard, PurposeInvoice
	if m.Kind == "mandate" {
		channel = ChannelMandate
	}
	if invoiceID == nil {
		purpose = PurposeTopup
	}
	token := ""
	if m.Kind == "card" {
		if s.seal == nil || m.TokenSealed == nil {
			return ChargeOutcome{}, errors.New("the card's token can't be read")
		}
		raw, err := s.seal.Decrypt(m.TokenSealed, []byte("card:"+m.OrgID.String()))
		if err != nil {
			return ChargeOutcome{}, err
		}
		token = string(raw)
	} else if m.ProviderRef != nil {
		token = *m.ProviderRef
	}
	cust, err := s.customer(ctx, m.Provider, m.OrgID)
	if err != nil {
		return ChargeOutcome{}, err
	}
	q := store.New(s.db)
	mid := m.ID
	intent, err := q.InsertPaymentIntent(ctx, store.InsertPaymentIntentParams{
		OrgID: m.OrgID, Reference: newReference("pgd"), Provider: m.Provider, Channel: channel, Purpose: purpose,
		InvoiceID: invoiceID, MethodID: &mid, AmountMinor: amount, Automatic: true,
	})
	if err != nil {
		return ChargeOutcome{}, err
	}
	out := ChargeOutcome{Intent: intent}
	tx, err := p.ChargeSaved(ctx, ChargeRequest{Reference: intent.Reference, AmountMinor: amount, Customer: cust, Token: token, Description: "PGDock"})
	switch {
	case errors.Is(err, ErrUnavailable):
		out.Unavailable, out.Message = true, err.Error()
		msg := "provider unavailable: " + err.Error()
		_ = q.FinishPaymentIntent(ctx, store.FinishPaymentIntentParams{Reference: intent.Reference, Status: "failed", Error: &msg})
		return out, nil
	case err != nil && !errors.Is(err, ErrDeclined):
		return out, err
	case err != nil || tx.Status == TxFailed:
		out.Message = tx.Message
		if out.Message == "" && err != nil {
			out.Message = err.Error()
		}
		msg := "declined: " + out.Message
		_ = q.FinishPaymentIntent(ctx, store.FinishPaymentIntentParams{Reference: intent.Reference, Status: "failed", Error: &msg})
		return out, nil
	case tx.Status == TxPending:
		return out, nil // the webhook or the re-query settles it
	}
	// Succeeded: re-verify, then settle like any other payment.
	if err := s.settleVerified(ctx, p, tx.ProviderRef, &intent); err != nil {
		return out, err
	}
	out.Succeeded = true
	return out, nil
}

// EnsureVirtualAccount returns orgID's transfer account, issuing one from
// the primary provider, or the fallback when the primary can't (V3 §3.4.3).
func (s *Service) EnsureVirtualAccount(ctx context.Context, orgID uuid.UUID) (store.VirtualAccount, error) {
	q := store.New(s.db)
	have, err := q.ListVirtualAccounts(ctx, orgID)
	if err != nil {
		return store.VirtualAccount{}, err
	}
	if len(have) > 0 {
		for _, v := range have {
			if v.Provider == s.routing.VAPrimary {
				return v, nil
			}
		}
		return have[0], nil
	}
	var errs []error
	for _, name := range []string{s.routing.VAPrimary, s.routing.VAFallback} {
		p, ok := s.providers[name]
		if name == "" || !ok || !p.Capabilities().VirtualAccounts {
			continue
		}
		cust, err := s.customer(ctx, name, orgID)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		va, err := p.IssueVirtualAccount(ctx, cust)
		if err != nil {
			s.log.Warn("virtual account", "provider", name, "org", orgID, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		var ref *string
		if va.ProviderRef != "" {
			ref = &va.ProviderRef
		}
		return q.InsertVirtualAccount(ctx, store.InsertVirtualAccountParams{
			OrgID: orgID, Provider: name, AccountNumber: va.AccountNumber, BankName: va.BankName, AccountName: va.AccountName, ProviderRef: ref,
		})
	}
	if len(errs) == 0 {
		return store.VirtualAccount{}, invalid("bank transfer accounts aren't available")
	}
	return store.VirtualAccount{}, errors.Join(errs...)
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
	var ref, pref, cur *string
	var amt *int64
	if ev.Reference != "" {
		ref = &ev.Reference
	}
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
		Provider: provider, ProviderEventID: ev.ID, Kind: ev.Kind, Reference: ref, ProviderRef: pref, AmountMinor: amt, Currency: cur, Payload: raw,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OutcomeDuplicate, nil
	}
	if err != nil {
		return "", err
	}
	outcome, org, perr := s.processEvent(ctx, provider, ev)
	var msg *string
	if perr != nil {
		m := perr.Error()
		msg = &m
		if outcome == "" {
			outcome = OutcomeFailed
		}
	}
	if err := q.FinishPaymentEvent(ctx, store.FinishPaymentEventParams{ID: row.ID, Outcome: &outcome, Error: msg, OrgID: org}); err != nil {
		return outcome, err
	}
	return outcome, perr
}

func (s *Service) processEvent(ctx context.Context, provider string, ev Event) (string, *uuid.UUID, error) {
	p, ok := s.providers[provider]
	if !ok {
		return OutcomeRejected, nil, fmt.Errorf("provider %s isn't configured", provider)
	}
	q := store.New(s.db)
	switch ev.Kind {
	case EventPaymentSucceeded, EventTransferReceived:
		var intent *store.PaymentIntent
		if ev.Reference != "" {
			if in, err := q.GetPaymentIntentByRef(ctx, ev.Reference); err == nil {
				intent = &in
			}
		}
		ref := ev.ProviderRef
		if ref == "" && intent != nil {
			tx, err := p.VerifyReference(ctx, intent.Reference)
			if err != nil {
				return OutcomeFailed, nil, err
			}
			ref = tx.ProviderRef
		}
		if err := s.settleVerified(ctx, p, ref, intent); err != nil {
			var um unmatchedError
			if errors.As(err, &um) {
				return OutcomeUnmatched, nil, nil
			}
			var rj rejectedError
			if errors.As(err, &rj) {
				return OutcomeRejected, nil, err
			}
			return OutcomeFailed, nil, err
		}
		if intent != nil {
			return OutcomePosted, &intent.OrgID, nil
		}
		return OutcomePosted, nil, nil
	case EventPaymentFailed:
		if ev.Reference == "" {
			return OutcomeIgnored, nil, nil
		}
		intent, err := q.GetPaymentIntentByRef(ctx, ev.Reference)
		if err != nil {
			return OutcomeIgnored, nil, nil
		}
		// Re-verify: a failure event for a payment that in fact succeeded
		// must not count against the customer.
		if tx, err := p.VerifyReference(ctx, ev.Reference); err == nil && tx.Status == TxSucceeded {
			return s.processEvent(ctx, provider, Event{Kind: EventPaymentSucceeded, Reference: ev.Reference, ProviderRef: tx.ProviderRef})
		}
		msg := "declined"
		if err := q.FinishPaymentIntent(ctx, store.FinishPaymentIntentParams{Reference: intent.Reference, Status: "failed", Error: &msg}); err != nil {
			return OutcomeFailed, &intent.OrgID, err
		}
		if intent.Automatic && s.onChargeFailed != nil {
			s.onChargeFailed(ctx, intent.OrgID)
		}
		return OutcomePosted, &intent.OrgID, nil
	case EventRefundCompleted:
		pr := ev.ProviderRef
		r, err := q.GetRefundByProviderRef(ctx, &pr)
		if err != nil {
			return OutcomeIgnored, nil, nil
		}
		if r.Status == "completed" {
			return OutcomeDuplicate, nil, nil
		}
		if _, err := s.completeRefund(ctx, r.ID, ev.ProviderRef); err != nil {
			return OutcomeFailed, nil, err
		}
		return OutcomePosted, nil, nil
	case EventMandateRevoked:
		m, err := q.RevokeMandateByRef(ctx, store.RevokeMandateByRefParams{Provider: provider, ProviderRef: &ev.MandateID})
		if err != nil {
			return OutcomeIgnored, nil, nil
		}
		s.notifyOrg(ctx, m.OrgID, "billing.mandate_revoked", "PGDock: your iSpend mandate was revoked",
			fmt.Sprintf("The iSpend wallet mandate %s's invoices were charged to has been revoked, so PGDock can't take automatic payments from it.\n\n"+
				"Add a card or a new mandate, or pay by transfer: %s/org/billing?org=%s\n", s.orgName(ctx, m.OrgID), s.publicURL, m.OrgID))
		return OutcomePosted, &m.OrgID, nil
	}
	return OutcomeIgnored, nil, nil
}

type unmatchedError struct{ ref string }

func (e unmatchedError) Error() string { return "no organisation for transaction " + e.ref }

type rejectedError struct{ why string }

func (e rejectedError) Error() string { return "verification failed: " + e.why }

// settleVerified looks providerRef up at the provider and, if it is a
// successful naira payment, records it for the org it belongs to: the one
// whose intent it completes, or whose virtual account it was paid into.
func (s *Service) settleVerified(ctx context.Context, p PaymentProvider, providerRef string, intent *store.PaymentIntent) error {
	if providerRef == "" {
		return rejectedError{"no transaction reference"}
	}
	tx, err := p.Verify(ctx, providerRef)
	if err != nil {
		return err
	}
	q := store.New(s.db)
	if intent == nil && tx.Reference != "" {
		if in, err := q.GetPaymentIntentByRef(ctx, tx.Reference); err == nil {
			intent = &in
		}
	}
	switch {
	case tx.Status != TxSucceeded:
		return rejectedError{fmt.Sprintf("the provider says %s", tx.Status)}
	case !strings.EqualFold(tx.Currency, "NGN"):
		return rejectedError{"currency " + tx.Currency}
	case tx.AmountMinor <= 0:
		return rejectedError{"no amount"}
	case intent != nil && tx.Reference != "" && tx.Reference != intent.Reference:
		return rejectedError{"the reference doesn't match"}
	case intent != nil && tx.Channel != ChannelTransfer && tx.AmountMinor < intent.AmountMinor:
		return rejectedError{fmt.Sprintf("paid %d, expected %d", tx.AmountMinor, intent.AmountMinor)}
	}
	in := PaymentIn{Provider: p.Name(), ProviderRef: tx.ProviderRef, AmountMinor: tx.AmountMinor, FeeMinor: tx.FeeMinor,
		Channel: tx.Channel, ReceivedAt: tx.At, FXQuote: tx.FXQuote}
	if in.Channel == "" {
		in.Channel = ChannelCard
	}
	switch {
	case intent != nil:
		in.OrgID, in.Reference, in.Channel = intent.OrgID, intent.Reference, intent.Channel
		in.InvoiceID = intent.InvoiceID
		in.Topup = intent.Purpose == PurposeTopup || intent.Purpose == PurposeCardSetup
	case tx.AccountNumber != "":
		va, err := q.VirtualAccountByNumber(ctx, store.VirtualAccountByNumberParams{Provider: p.Name(), AccountNumber: tx.AccountNumber})
		if err != nil {
			return unmatchedError{tx.ProviderRef}
		}
		in.OrgID, in.Channel = va.OrgID, ChannelTransfer
	default:
		return unmatchedError{tx.ProviderRef}
	}
	if in.Channel == ChannelStablecoin {
		in.Topup = true
	}
	if _, err := s.RecordPayment(ctx, in); err != nil {
		return err
	}
	if intent != nil {
		if err := q.FinishPaymentIntent(ctx, store.FinishPaymentIntentParams{Reference: intent.Reference, Status: "succeeded"}); err != nil {
			return err
		}
		if err := s.saveMethod(ctx, p.Name(), intent.OrgID, tx); err != nil {
			s.log.Warn("saving a payment method", "org", intent.OrgID, "err", err)
		}
	}
	return nil
}

// saveMethod keeps a card's token (sealed) or a wallet mandate for later
// charges.
func (s *Service) saveMethod(ctx context.Context, provider string, orgID uuid.UUID, tx Transaction) error {
	q := store.New(s.db)
	switch {
	case tx.Card != nil && tx.Card.Token != "" && s.seal != nil:
		sealed, err := s.seal.Encrypt([]byte(tx.Card.Token), []byte("card:"+orgID.String()))
		if err != nil {
			return err
		}
		ref := "card:" + tx.Card.Last4 + ":" + fmt.Sprint(tx.Card.ExpMonth, "/", tx.Card.ExpYear) + ":" + orgID.String()
		em, ey := int32(tx.Card.ExpMonth), int32(tx.Card.ExpYear)
		_, err = q.UpsertPaymentMethod(ctx, store.UpsertPaymentMethodParams{
			OrgID: orgID, Provider: provider, Kind: "card", TokenSealed: sealed, ProviderRef: &ref,
			Brand: &tx.Card.Brand, Last4: &tx.Card.Last4, ExpMonth: &em, ExpYear: &ey,
		})
		return err
	case tx.MandateID != "":
		var limit *int64
		if tx.MandateLimit > 0 {
			limit = &tx.MandateLimit
		}
		brand := "iSpend wallet"
		_, err := q.UpsertPaymentMethod(ctx, store.UpsertPaymentMethodParams{
			OrgID: orgID, Provider: provider, Kind: "mandate", ProviderRef: &tx.MandateID, Brand: &brand, LimitMinor: limit,
		})
		return err
	}
	return nil
}

// ---- Missed events ---------------------------------------------------------------

// Requery asks each provider for its transactions in [from, to) and posts
// any successful one not yet recorded, and settles intents still pending
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
			out, err := s.HandleEvent(ctx, name, Event{ID: "requery:" + tx.ProviderRef, Kind: kind, ProviderRef: tx.ProviderRef, Reference: tx.Reference,
				AmountMinor: tx.AmountMinor, Currency: tx.Currency})
			if err != nil {
				errs = append(errs, err)
			}
			if out == OutcomePosted {
				n++
			}
		}
	}
	pending, err := q.PendingIntents(ctx, from)
	if err != nil {
		return n, errors.Join(append(errs, err)...)
	}
	for _, in := range pending {
		p, ok := s.providers[in.Provider]
		if !ok {
			continue
		}
		tx, err := p.VerifyReference(ctx, in.Reference)
		if err != nil {
			continue
		}
		switch tx.Status {
		case TxSucceeded:
			in := in
			if err := s.settleVerified(ctx, p, tx.ProviderRef, &in); err != nil {
				errs = append(errs, err)
			} else {
				n++
			}
		case TxFailed:
			msg := "failed (found by re-query)"
			_ = q.FinishPaymentIntent(ctx, store.FinishPaymentIntentParams{Reference: in.Reference, Status: "failed", Error: &msg})
		}
	}
	return n, errors.Join(errs...)
}
