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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/billing/flutterwave"
	"github.com/israel-duff/pgdock/internal/billing/ispend"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/store"
)

type payWorld struct {
	s      *billing.Service
	db     *pgxpool.Pool
	clk    *clock
	sent   *sentMail
	flw    *flutterwave.Fake
	isp    *ispend.Fake
	flwP   *flutterwave.Provider
	ispP   *ispend.Provider
	events chan string
}

// newPayWorld runs the billing service against fake Flutterwave and iSpend
// sandboxes, whose webhooks reach the service through a receiver like the
// API's.
func newPayWorld(t *testing.T) *payWorld {
	t.Helper()
	s, db, clk, sent := newService(t)
	w := &payWorld{s: s, db: db, clk: clk, sent: sent, events: make(chan string, 64)}
	w.flw = flutterwave.NewFake("flw-secret", "flw-hash")
	w.isp = ispend.NewFake("isp-key", "isp-secret")
	flwSrv, ispSrv := httptest.NewServer(w.flw), httptest.NewServer(w.isp)
	t.Cleanup(flwSrv.Close)
	t.Cleanup(ispSrv.Close)
	w.flwP = flutterwave.New(flutterwave.Config{BaseURL: flwSrv.URL, SecretKey: "flw-secret", WebhookHash: "flw-hash", BVN: "22222222222"})
	w.ispP = ispend.New(ispend.Config{BaseURL: ispSrv.URL, APIKey: "isp-key", WebhookSecret: "isp-secret"})
	key, _ := crypto.GenerateKey()
	kr, _ := crypto.NewKeyring(key)
	s.SetPayments([]billing.PaymentProvider{w.flwP, w.ispP}, billing.Routing{
		Cards: billing.ProviderFlutterwave, VAPrimary: billing.ProviderISpend, VAFallback: billing.ProviderFlutterwave, Wallet: billing.ProviderISpend,
	}, kr)
	hook := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		p, _ := s.Provider(strings.TrimPrefix(r.URL.Path, "/"))
		ev, err := p.ParseWebhook(r.Context(), r)
		if err != nil {
			w.events <- "error: " + err.Error()
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		out, err := s.HandleEvent(context.Background(), p.Name(), ev)
		if err != nil {
			out += ": " + err.Error()
		}
		w.events <- ev.Kind + " " + out
	}))
	t.Cleanup(hook.Close)
	w.flw.WebhookURL = hook.URL + "/flutterwave"
	w.isp.WebhookURL = hook.URL + "/ispend"
	return w
}

// event waits for the next webhook to be handled.
func (w *payWorld) event(t *testing.T) string {
	t.Helper()
	select {
	case e := <-w.events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no webhook arrived")
		return ""
	}
}

func (w *payWorld) noEvent(t *testing.T) {
	t.Helper()
	select {
	case e := <-w.events:
		t.Fatalf("unexpected webhook: %s", e)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestCardCheckoutSavedChargesAndDeclines(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	org := newOrg(t, w.db, "acme")
	if _, err := w.s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	inv := issuedProInvoice(t, w.s, w.clk, org)

	// Pay the invoice on the hosted page; the card is saved.
	intent, err := w.s.StartCheckout(ctx, billing.CheckoutStart{OrgID: org, Channel: billing.ChannelCard, Purpose: billing.PurposeInvoice, InvoiceID: &inv.ID})
	if err != nil || intent.CheckoutUrl == nil || intent.AmountMinor != inv.TotalMinor {
		t.Fatalf("checkout: %+v %v", intent, err)
	}
	if _, err := w.flw.CompleteCheckout(intent.Reference, true); err != nil {
		t.Fatal(err)
	}
	if e := w.event(t); e != "payment.succeeded posted" {
		t.Fatalf("webhook: %s", e)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid {
		t.Fatalf("invoice %s", got.Status)
	}
	// Card fees are posted apart from the cash: 1.4% here.
	fee := inv.TotalMinor * 14 / 1000
	if b := balance(t, w.db, nil, "fees:flutterwave"); b != fee {
		t.Errorf("fees %d, want %d", b, fee)
	}
	methods, _ := q.ListPaymentMethods(ctx, org)
	if len(methods) != 1 || methods[0].Kind != "card" || *methods[0].Last4 != "4081" || !methods[0].IsDefault || methods[0].TokenSealed == nil ||
		strings.Contains(string(methods[0].TokenSealed), "flw-t1") {
		t.Fatalf("saved card: %+v", methods)
	}

	// The same webhook again is a duplicate; the ledger doesn't move.
	before := balance(t, w.db, &org, billing.AccReceivable)
	w.flw.RedeliverLast()
	if e := w.event(t); e != "payment.succeeded duplicate" {
		t.Fatalf("redelivery: %s", e)
	}
	if b := balance(t, w.db, &org, billing.AccReceivable); b != before {
		t.Errorf("receivable moved on a duplicate: %d → %d", before, b)
	}

	// November's invoice: charged to the saved card.
	w.clk.t = time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC)
	nov, _ := w.s.Draft(ctx, org, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	nov2, err := w.s.Issue(ctx, nov.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.s.ChargeMethod(ctx, methods[0], &nov2.ID, nov2.TotalMinor)
	if err != nil || !out.Succeeded {
		t.Fatalf("saved-card charge: %+v %v", out, err)
	}
	w.event(t) // the charge's own webhook: a duplicate of what the charge posted
	if got := invoiceNow(t, q, nov2.ID); got.Status != billing.StatusPaid {
		t.Errorf("November %s", got.Status)
	}

	// A declined card is the customer's failure; an outage isn't.
	w.flw.Decline("flw-t1-"+intent.Reference, "Insufficient funds")
	out, err = w.s.ChargeMethod(ctx, methods[0], nil, 500_000)
	if err != nil || out.Succeeded || out.Unavailable || !strings.Contains(out.Message, "Insufficient") {
		t.Fatalf("declined: %+v %v", out, err)
	}
	w.flw.SetDown(true)
	out, err = w.s.ChargeMethod(ctx, methods[0], nil, 500_000)
	if err != nil || out.Succeeded || !out.Unavailable {
		t.Fatalf("outage: %+v %v", out, err)
	}
	w.flw.SetDown(false)
	mustCheck(t, w.db)
}

func TestTransfersIntoVirtualAccounts(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	org := newOrg(t, w.db, "acme")
	if _, err := w.s.UpdateDetails(ctx, org, billing.Details{DeductsWHT: true}); err != nil {
		t.Fatal(err)
	}
	inv := issuedProInvoice(t, w.s, w.clk, org)
	va, err := w.s.EnsureVirtualAccount(ctx, org)
	if err != nil || va.Provider != billing.ProviderISpend || va.AccountNumber == "" {
		t.Fatalf("virtual account: %+v %v", va, err)
	}
	if again, _ := w.s.EnsureVirtualAccount(ctx, org); again.ID != va.ID {
		t.Error("a second account was issued")
	}
	// Short by exactly the WHT.
	if _, err := w.isp.Transfer(va.AccountNumber, inv.TotalMinor-inv.WhtExpectedMinor); err != nil {
		t.Fatal(err)
	}
	if e := w.event(t); e != "transfer.received posted" {
		t.Fatalf("transfer: %s", e)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaidWHTPending {
		t.Errorf("invoice %s", got.Status)
	}
	// An extra transfer becomes credit.
	if _, err := w.isp.Transfer(va.AccountNumber, 70_000); err != nil {
		t.Fatal(err)
	}
	w.event(t)
	if c, _ := w.s.CreditAvailable(ctx, w.db, org); c != 70_000 {
		t.Errorf("credit %d", c)
	}

	// A transfer whose webhook is lost is found by the re-query.
	w.isp.DropWebhooks(1)
	if _, err := w.isp.Transfer(va.AccountNumber, 30_000); err != nil {
		t.Fatal(err)
	}
	w.noEvent(t)
	n, err := w.s.Requery(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("re-query: %d %v", n, err)
	}
	if c, _ := w.s.CreditAvailable(ctx, w.db, org); c != 100_000 {
		t.Errorf("credit after the re-query %d", c)
	}
	if n, _ := w.s.Requery(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("a second re-query posted %d", n)
	}
	mustCheck(t, w.db)
}

func TestVirtualAccountFallbackOnISpendOutage(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	org := newOrg(t, w.db, "acme")
	inv := issuedProInvoice(t, w.s, w.clk, org)
	w.isp.SetVirtualAccountsDown(true)
	va, err := w.s.EnsureVirtualAccount(ctx, org)
	if err != nil || va.Provider != billing.ProviderFlutterwave {
		t.Fatalf("fallback account: %+v %v", va, err)
	}
	if _, err := w.flw.Transfer(va.AccountNumber, inv.TotalMinor); err != nil {
		t.Fatal(err)
	}
	if e := w.event(t); e != "transfer.received posted" {
		t.Fatalf("transfer: %s", e)
	}
	if got := invoiceNow(t, store.New(w.db), inv.ID); got.Status != billing.StatusPaid {
		t.Errorf("invoice %s", got.Status)
	}
	mustCheck(t, w.db)
}

func TestWalletMandateAndStablecoin(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	q := store.New(w.db)
	org := newOrg(t, w.db, "acme")
	if _, err := w.s.AddContact(ctx, org, "ap@acme.example", nil); err != nil {
		t.Fatal(err)
	}
	inv := issuedProInvoice(t, w.s, w.clk, org)

	// Pay with iSpend, setting up a ₦50,000 monthly mandate.
	intent, err := w.s.StartCheckout(ctx, billing.CheckoutStart{OrgID: org, Channel: billing.ChannelWallet, Purpose: billing.PurposeInvoice,
		InvoiceID: &inv.ID, MandateLimitMinor: 5_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.isp.Approve(*intent.CheckoutUrl, 10_000_000); err != nil {
		t.Fatal(err)
	}
	if e := w.event(t); e != "payment.succeeded posted" {
		t.Fatalf("wallet: %s", e)
	}
	if got := invoiceNow(t, q, inv.ID); got.Status != billing.StatusPaid {
		t.Errorf("invoice %s", got.Status)
	}
	methods, _ := q.ListPaymentMethods(ctx, org)
	if len(methods) != 1 || methods[0].Kind != "mandate" || methods[0].LimitMinor == nil || *methods[0].LimitMinor != 5_000_000 {
		t.Fatalf("mandate: %+v", methods)
	}
	// A charge against the mandate, then one over its limit.
	if out, err := w.s.ChargeMethod(ctx, methods[0], nil, 2_000_000); err != nil || !out.Succeeded {
		t.Fatalf("mandate charge: %+v %v", out, err)
	}
	w.event(t)
	if out, err := w.s.ChargeMethod(ctx, methods[0], nil, 6_000_000); err != nil || out.Succeeded || out.Unavailable {
		t.Fatalf("over the limit: %+v %v", out, err)
	}
	// Revoked in iSpend: PGDock stops using it and asks for another method.
	w.isp.RevokeMandate(*methods[0].ProviderRef)
	if e := w.event(t); e != "mandate.revoked posted" {
		t.Fatalf("revoked: %s", e)
	}
	if left, _ := q.ListPaymentMethods(ctx, org); len(left) != 0 {
		t.Errorf("methods after the revocation: %+v", left)
	}
	if !strings.Contains(w.sent.msgs[len(w.sent.msgs)-1].Subject, "mandate was revoked") {
		t.Errorf("no email about the revoked mandate")
	}

	// Stablecoin top-ups: off by default; on, the naira amount is fixed at
	// iSpend's quote.
	if _, err := w.s.StartCheckout(ctx, billing.CheckoutStart{OrgID: org, Channel: billing.ChannelStablecoin, Purpose: billing.PurposeTopup, AmountMinor: 1_000_000}); !errors.Is(err, billing.ErrInvalid) {
		t.Fatalf("stablecoin off: %v", err)
	}
	set, _ := w.s.Settings(ctx)
	set.Stablecoin = true
	if err := w.s.SetSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	credit, _ := w.s.CreditAvailable(ctx, w.db, org)
	sc, err := w.s.StartCheckout(ctx, billing.CheckoutStart{OrgID: org, Channel: billing.ChannelStablecoin, Purpose: billing.PurposeTopup, AmountMinor: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.isp.Approve(*sc.CheckoutUrl, 0); err != nil {
		t.Fatal(err)
	}
	w.event(t)
	if c, _ := w.s.CreditAvailable(ctx, w.db, org); c != credit+1_000_000 {
		t.Errorf("credit %d, want %d", c, credit+1_000_000)
	}
	pays, _ := q.ListOrgPayments(ctx, store.ListOrgPaymentsParams{OrgID: org, Lim: 1})
	if pays[0].Channel != billing.ChannelStablecoin || !strings.Contains(string(pays[0].FxQuote), "USDT") {
		t.Errorf("stablecoin payment: %+v", pays[0])
	}
	mustCheck(t, w.db)
}

func TestRefundsAndWebhookAuthentication(t *testing.T) {
	w := newPayWorld(t)
	ctx := context.Background()
	org := newOrg(t, w.db, "acme")
	va, err := w.s.EnsureVirtualAccount(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.isp.Transfer(va.AccountNumber, 500_000); err != nil { // no invoice: credit
		t.Fatal(err)
	}
	w.event(t)
	pays, _ := store.New(w.db).ListOrgPayments(ctx, store.ListOrgPaymentsParams{OrgID: org, Lim: 1})
	r, err := w.s.Refund(ctx, pays[0].ID, 200_000, "closing the account", nil)
	if err != nil || r.Status != "pending" {
		t.Fatalf("refund: %+v %v", r, err)
	}
	if e := w.event(t); e != "refund.completed posted" {
		t.Fatalf("refund webhook: %s", e)
	}
	if c, _ := w.s.CreditAvailable(ctx, w.db, org); c != 300_000 {
		t.Errorf("credit after the refund %d", c)
	}

	// Forged and stale webhooks are refused.
	for name, req := range map[string]*http.Request{
		"no hash":   httptest.NewRequest("POST", "/", strings.NewReader(`{"event":"charge.completed","data":{"id":1}}`)),
		"wrong sig": httptest.NewRequest("POST", "/", strings.NewReader(`{"id":"e","type":"transfer.received","data":{}}`)),
	} {
		var err error
		if name == "no hash" {
			_, err = w.flwP.ParseWebhook(ctx, req)
		} else {
			req.Header.Set("X-ISpend-Timestamp", "1")
			req.Header.Set("X-ISpend-Signature", "00")
			_, err = w.ispP.ParseWebhook(ctx, req)
		}
		if !errors.Is(err, billing.ErrUnauthenticated) {
			t.Errorf("%s: %v", name, err)
		}
	}
	body := `{"id":"e1","type":"transfer.received","data":{}}`
	old := time.Now().Add(-10 * time.Minute).Unix()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("X-ISpend-Timestamp", itoa(old))
	req.Header.Set("X-ISpend-Signature", ispend.Sign("isp-secret", old, []byte(body)))
	if _, err := w.ispP.ParseWebhook(ctx, req); !errors.Is(err, billing.ErrUnauthenticated) {
		t.Errorf("a replayed webhook: %v", err)
	}
	// A webhook that names a transaction the provider doesn't know is rejected.
	out, _ := w.s.HandleEvent(ctx, billing.ProviderISpend, billing.Event{ID: "forged", Kind: billing.EventTransferReceived, ProviderRef: "txn_nope"})
	if out == billing.OutcomePosted {
		t.Error("an unverifiable transfer was posted")
	}
	mustCheck(t, w.db)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
