package billing_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/billing/iswallet"
	"github.com/israel-duff/pgdock/internal/store"
)

// payWorld is a walletWorld whose service counts in naira, like the older
// suites (newService's price book), and which can reach the wallet provider
// directly as well as through the service.
type payWorld struct {
	*walletWorld
	prov *iswallet.Client
}

// newPayWorld is newWalletWorld for the suites that count in naira.
func newPayWorld(t *testing.T) *payWorld {
	t.Helper()
	s, db, clk, sent := newService(t)
	ww := &walletWorld{s: s, db: db, clk: clk, sent: sent, events: make(chan string, 64)}
	ww.fake = iswallet.NewFake("isw-key", "isw-secret-0123456789")
	ww.fake.Now = clk.Now
	s.SetDunning(noSuspend{}, nil)
	srv := httptest.NewServer(ww.fake)
	t.Cleanup(srv.Close)
	p := iswallet.New(iswallet.Config{BaseURL: srv.URL, APIKey: "isw-key", WebhookSecret: "isw-secret-0123456789", Now: clk.Now})
	s.SetWallets(p, "pgdock:test")
	hook := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ev, err := p.ParseWebhook(r.Context(), r)
		if err != nil {
			ww.events <- "error: " + err.Error()
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		out, err := s.HandleEvent(context.Background(), p.Name(), ev)
		if err != nil {
			out += ": " + err.Error()
		}
		ww.events <- ev.Kind + " " + out
	}))
	t.Cleanup(hook.Close)
	ww.fake.WebhookURL = hook.URL
	return &payWorld{walletWorld: ww, prov: p}
}

func (w *payWorld) noEvent(t *testing.T) {
	t.Helper()
	select {
	case e := <-w.events:
		t.Fatalf("unexpected webhook: %s", e)
	case <-time.After(300 * time.Millisecond):
	}
}

// window is a re-query window around the test clock.
func (w *payWorld) window() (from, to time.Time) {
	return w.clk.Now().Add(-time.Hour), w.clk.Now().Add(time.Hour)
}

// stray opens a wallet and bank account at the provider that PGDock has no
// record of, and returns the account number.
func (w *payWorld) stray(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	wl, err := w.prov.CreateWallet(ctx, billing.WalletRequest{OwnerRef: "pgdock:test:org:" + uuid.NewString(), Name: "Stray Ltd", Email: "a@stray.example",
		BVN: "22222222222", Tier: "TIER_2", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	va, err := w.prov.IssueVirtualAccount(ctx, billing.Customer{OrgID: uuid.New(), ProviderID: wl.ID})
	if err != nil {
		t.Fatal(err)
	}
	return va.AccountNumber
}

// A bank transfer into an org's virtual account lands in its wallet, not on
// its invoices; the same webhook again changes nothing.
func TestDepositsAreHeldAndDuplicatesAreNoOps(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	org, wid := w.walletOrg(t, "acme", 0, false)
	inv := issuedProInvoice(t, w.s, w.clk, org)
	va, err := w.s.EnsureTransferAccount(ctx, org)
	if err != nil || va.Provider != billing.ProviderISwallet || va.AccountNumber == "" {
		t.Fatalf("virtual account: %+v %v", va, err)
	}
	if again, _ := w.s.EnsureVirtualAccount(ctx, org); again.ID != va.ID {
		t.Error("a second account was issued")
	}

	receivable := balance(t, w.db, &org, billing.AccReceivable)
	if _, _, _, err := w.fake.Deposit(va.AccountNumber, 100_000_00); err != nil {
		t.Fatal(err)
	}
	if e := w.event(t); e != "transfer.received posted" {
		t.Fatalf("deposit: %s", e)
	}
	const credited = 100_000_00 - 1_400_00
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusIssued || got.PaidMinor != 0 {
		t.Errorf("a deposit settled the invoice: %s, paid %d", got.Status, got.PaidMinor)
	}
	if b := balance(t, w.db, &org, billing.AccReceivable); b != receivable {
		t.Errorf("receivable moved: %d → %d", receivable, b)
	}
	if got := w.held(t, org, billing.AccWalletBalance); got != credited || got != w.fake.WalletBalance(wid) {
		t.Errorf("the books hold %d for the wallet, the provider %d, want %d", got, w.fake.WalletBalance(wid), credited)
	}

	w.fake.RedeliverLast()
	if e := w.event(t); e != "transfer.received duplicate" {
		t.Fatalf("redelivery: %s", e)
	}
	if got := w.held(t, org, billing.AccWalletBalance); got != credited {
		t.Errorf("wallet balance moved on a duplicate: %d", got)
	}
	mustCheck(t, w.db)
}

// Money paid into an account PGDock doesn't know is kept apart until the
// platform admin says whose it is.
func TestUnmatchedDepositIsAttributedByHand(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	org, _ := w.walletOrg(t, "acme", 0, false)

	if _, _, _, err := w.fake.Deposit(w.stray(t), 30_000_00); err != nil {
		t.Fatal(err)
	}
	if e := w.event(t); e != "transfer.received unmatched" {
		t.Fatalf("deposit to an unknown account: %s", e)
	}
	if pays, _ := q.ListOrgPayments(ctx, store.ListOrgPaymentsParams{OrgID: org, Lim: 10}); len(pays) != 0 {
		t.Fatalf("unmatched money was posted: %+v", pays)
	}
	events, err := q.UnmatchedPaymentEvents(ctx)
	if err != nil || len(events) != 1 {
		t.Fatalf("unmatched events: %+v %v", events, err)
	}

	st, err := w.s.AttributeEvent(ctx, events[0].ID, org, nil)
	if err != nil || st.Payment.OrgID != org || st.Payment.AmountMinor != 30_000_00-420_00 || st.Payment.Provider != billing.ProviderISwallet {
		t.Fatalf("attribute: %+v %v", st, err)
	}
	if got := w.held(t, org, billing.AccWalletBalance); got != st.Payment.AmountMinor {
		t.Errorf("the org's wallet balance %d", got)
	}
	if ev, _ := q.GetPaymentEvent(ctx, events[0].ID); ev.Outcome == nil || *ev.Outcome != billing.OutcomePosted || ev.OrgID == nil || *ev.OrgID != org {
		t.Errorf("the event after attribution: %+v", ev)
	}
	if _, err := w.s.AttributeEvent(ctx, events[0].ID, org, nil); !errors.Is(err, billing.ErrConflict) {
		t.Errorf("attributing twice: %v", err)
	}
	mustCheck(t, w.db)
}

func TestWebhookAuthenticationAndForgedEvents(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	org, wid := w.walletOrg(t, "acme", 0, false)
	other, _ := w.walletOrg(t, "globex", 0, false)

	// Forged, unsigned and replayed webhooks are refused.
	body := `{"id":"e1","event_type":"wallet.credit.posted","data":{}}`
	signed := func(ts int64, sig string) *http.Request {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("X-iSpend-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-iSpend-Signature", sig)
		return req
	}
	now := w.clk.Now().Unix()
	old := w.clk.Now().Add(-10 * time.Minute).Unix()
	for name, req := range map[string]*http.Request{
		"no headers":   httptest.NewRequest("POST", "/", strings.NewReader(body)),
		"wrong sig":    signed(now, "sha256=00"),
		"a replay":     signed(old, iswallet.Sign("isw-secret-0123456789", old, []byte(body))),
		"wrong secret": signed(now, iswallet.Sign("another-secret-0123456", now, []byte(body))),
	} {
		if _, err := w.prov.ParseWebhook(ctx, req); !errors.Is(err, billing.ErrUnauthenticated) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An event about anything but a deposit is acknowledged and ignored.
	other1 := `{"id":"e2","event_type":"wallet.frozen","data":{}}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(other1))
	req.Header.Set("X-iSpend-Timestamp", strconv.FormatInt(now, 10))
	req.Header.Set("X-iSpend-Signature", iswallet.Sign("isw-secret-0123456789", now, []byte(other1)))
	if _, err := w.prov.ParseWebhook(ctx, req); !errors.Is(err, billing.ErrIgnoredEvent) {
		t.Errorf("a wallet.frozen event: %v", err)
	}

	// An authentic-looking event for a transaction the provider doesn't know
	// posts nothing.
	out, _ := w.s.HandleEvent(ctx, billing.ProviderISwallet, billing.Event{ID: "forged", Kind: billing.EventTransferReceived, ProviderRef: wid + ":txn_nope", AmountMinor: 5_000_000})
	if out == billing.OutcomePosted {
		t.Error("an unverifiable transfer was posted")
	}
	if out, _ := w.s.HandleEvent(ctx, "nobody", billing.Event{ID: "x", Kind: billing.EventTransferReceived}); out != billing.OutcomeRejected {
		t.Errorf("an event for an unknown provider: %s", out)
	}

	// From the M27 security review: an event that names a real transaction
	// but lies about the amount (only possible with a leaked secret) posts
	// what the provider says, to the org whose account it was paid into.
	va, err := w.s.EnsureTransferAccount(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	w.fake.DropWebhooks(1) // the genuine notification is lost
	wallet, txn, credited, err := w.fake.Deposit(va.AccountNumber, 50_000_00)
	if err != nil {
		t.Fatal(err)
	}
	out, err = w.s.HandleEvent(ctx, billing.ProviderISwallet, billing.Event{ID: "forged-1", Kind: billing.EventTransferReceived,
		ProviderRef: wallet + billing.RefSep + txn, AmountMinor: 900_000_000, Currency: "NGN"})
	if err != nil || out != billing.OutcomePosted {
		t.Fatalf("event: %s %v", out, err)
	}
	if got := w.held(t, org, billing.AccWalletBalance); got != credited {
		t.Errorf("the org was credited %d, the provider says %d", got, credited)
	}
	if got := w.held(t, other, billing.AccWalletBalance); got != 0 {
		t.Errorf("another org was credited %d", got)
	}
	mustCheck(t, w.db)
}
