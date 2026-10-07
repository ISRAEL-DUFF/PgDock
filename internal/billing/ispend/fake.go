package ispend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fake is a stand-in for iSpend's merchant sandbox (V3 §3.4.6: "simulated
// transfers"), for tests: the merchant API plus what a customer or a bank
// would do, with signed webhooks delivered to WebhookURL.
type Fake struct {
	APIKey        string
	WebhookSecret string
	WebhookURL    string
	Now           func() time.Time

	mu        sync.Mutex
	down      bool
	vaDown    bool
	drop      int
	next      int
	customers map[string]string // id → name
	vas       map[string]string // account number → customer id
	sessions  map[string]fakeSession
	txs       map[string]*Txn // by id
	mandates  map[string]*fakeMandate
	lastHook  []byte
}

type fakeSession struct {
	Reference string
	Amount    int64
	Kind      string
	Limit     int64
}

type fakeMandate struct {
	Mandate
	Revoked bool
	Balance int64 // the customer's wallet
}

// NewFake returns a fake.
func NewFake(apiKey, secret string) *Fake {
	return &Fake{APIKey: apiKey, WebhookSecret: secret, Now: time.Now, customers: map[string]string{}, vas: map[string]string{},
		sessions: map[string]fakeSession{}, txs: map[string]*Txn{}, mandates: map[string]*fakeMandate{}}
}

// SetDown makes every call answer 503.
func (f *Fake) SetDown(down bool) { f.mu.Lock(); f.down = down; f.mu.Unlock() }

// SetVirtualAccountsDown makes issuing virtual accounts fail (iSpend's
// banking arrangements not live, or an outage), the rest working.
func (f *Fake) SetVirtualAccountsDown(down bool) { f.mu.Lock(); f.vaDown = down; f.mu.Unlock() }

// DropWebhooks loses the next n webhooks.
func (f *Fake) DropWebhooks(n int) { f.mu.Lock(); f.drop = n; f.mu.Unlock() }

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *Fake) id(prefix string) string { f.next++; return fmt.Sprintf("%s_%04d", prefix, f.next) }

func (f *Fake) newTxn(ref, channel string, amount int64) *Txn {
	t := &Txn{ID: f.id("txn"), Reference: ref, Status: "succeeded", AmountMinor: amount, FeeMinor: amount / 200, Currency: "NGN",
		Channel: channel, CreatedAt: f.Now().UTC()}
	f.txs[t.ID] = t
	return t
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.APIKey {
		reply(w, http.StatusUnauthorized, map[string]string{"message": "bad API key"})
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	str := func(k string) string { s, _ := body[k].(string); return s }
	num := func(k string) int64 { n, _ := body[k].(float64); return int64(n) }
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/merchant/customers":
		id := f.id("cus")
		f.customers[id] = str("name")
		reply(w, 201, map[string]string{"id": id})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "customers" && parts[3] == "virtual-accounts":
		if f.vaDown {
			reply(w, http.StatusServiceUnavailable, map[string]string{"message": "virtual accounts unavailable"})
			return
		}
		cus := parts[2]
		if _, ok := f.customers[cus]; !ok {
			reply(w, 404, map[string]string{"message": "no customer"})
			return
		}
		for acct, c := range f.vas {
			if c == cus {
				reply(w, 200, map[string]string{"id": "va_" + acct, "account_number": acct, "bank_name": "iSpend MFB", "account_name": "PGDock/" + f.customers[cus]})
				return
			}
		}
		acct := fmt.Sprintf("90%08d", len(f.vas)+1)
		f.vas[acct] = cus
		reply(w, 201, map[string]string{"id": "va_" + acct, "account_number": acct, "bank_name": "iSpend MFB", "account_name": "PGDock/" + f.customers[cus]})
	case r.Method == http.MethodPost && r.URL.Path == "/merchant/checkout-sessions":
		id := f.id("cs")
		s := fakeSession{Reference: str("reference"), Amount: num("amount_minor"), Kind: str("kind")}
		if m, ok := body["mandate"].(map[string]any); ok {
			l, _ := m["monthly_limit_minor"].(float64)
			s.Limit = int64(l)
		}
		f.sessions[id] = s
		reply(w, 201, map[string]string{"id": id, "url": "https://pay.ispend.test/checkout/" + id})
	case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "mandates" && parts[3] == "charges":
		m, ok := f.mandates[parts[2]]
		switch {
		case !ok:
			reply(w, 404, map[string]string{"message": "no mandate"})
			return
		case m.Revoked:
			reply(w, http.StatusPaymentRequired, map[string]string{"message": "mandate revoked"})
			return
		case num("amount_minor") > m.MonthlyLimitMinor || num("amount_minor") > m.Balance:
			reply(w, http.StatusPaymentRequired, map[string]string{"message": "over the mandate's limit or the wallet's balance"})
			return
		}
		for _, t := range f.txs { // idempotent by reference
			if t.Reference == str("reference") {
				reply(w, 200, t)
				return
			}
		}
		m.Balance -= num("amount_minor")
		t := f.newTxn(str("reference"), "mandate", num("amount_minor"))
		t.Mandate = &m.Mandate
		f.hook("payment.succeeded", t)
		reply(w, 201, t)
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "mandates":
		if m, ok := f.mandates[parts[2]]; ok {
			m.Revoked = true
		}
		w.WriteHeader(204)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "transactions":
		for _, t := range f.txs {
			if t.ID == parts[2] || t.Reference == parts[2] {
				reply(w, 200, t)
				return
			}
		}
		reply(w, 404, map[string]string{"message": "no transaction"})
	case r.Method == http.MethodGet && r.URL.Path == "/merchant/transactions":
		from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
		to, _ := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
		items := []*Txn{}
		for _, t := range f.txs {
			if !t.CreatedAt.Before(from) && t.CreatedAt.Before(to) {
				items = append(items, t)
			}
		}
		reply(w, 200, map[string]any{"items": items})
	case r.Method == http.MethodPost && r.URL.Path == "/merchant/refunds":
		id := f.id("rf")
		reply(w, 201, map[string]string{"id": id, "status": "pending"})
		go f.later(func() { f.hook("refund.completed", map[string]string{"id": id, "status": "completed"}) })
	default:
		reply(w, 404, map[string]string{"message": "not found: " + r.Method + " " + r.URL.Path})
	}
}

func (f *Fake) later(fn func()) {
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// hook sends a signed webhook (f.mu held).
func (f *Fake) hook(kind string, data any) {
	body, _ := json.Marshal(map[string]any{"id": f.id("evt"), "type": kind, "created_at": f.Now().UTC(), "data": data})
	f.lastHook = body
	if f.drop > 0 {
		f.drop--
		return
	}
	f.send(body)
}

func (f *Fake) send(body []byte) {
	if f.WebhookURL == "" {
		return
	}
	ts := f.Now().Unix()
	sig := Sign(f.WebhookSecret, ts, body)
	go func() {
		req, err := http.NewRequest(http.MethodPost, f.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-ISpend-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-ISpend-Signature", sig)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}()
}

// RedeliverLast sends the last webhook again (a duplicate).
func (f *Fake) RedeliverLast() { f.mu.Lock(); defer f.mu.Unlock(); f.send(f.lastHook) }

// Transfer is a bank transfer into a virtual account.
func (f *Fake) Transfer(accountNumber string, kobo int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.vas[accountNumber]; !ok {
		return "", fmt.Errorf("no virtual account %s", accountNumber)
	}
	t := f.newTxn("", "transfer", kobo)
	t.VirtualAccountNumber = accountNumber
	f.hook("transfer.received", t)
	return t.ID, nil
}

// Approve is the customer approving a checkout in iSpend: a wallet payment
// (with a mandate of the asked limit and the given wallet balance, if one
// was asked for), or a stablecoin top-up at the quoted rate.
func (f *Fake) Approve(sessionURL string, walletBalance int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := sessionURL[strings.LastIndex(sessionURL, "/")+1:]
	s, ok := f.sessions[id]
	if !ok {
		return "", fmt.Errorf("no checkout session %s", id)
	}
	channel := "wallet"
	if s.Kind == "stablecoin" {
		channel = "stablecoin"
	}
	t := f.newTxn(s.Reference, channel, s.Amount)
	if s.Kind == "stablecoin" {
		usdt := float64(s.Amount) / 100 / 1550
		t.FXQuote, _ = json.Marshal(map[string]any{"quote_id": f.id("q"), "asset": "USDT", "amount": fmt.Sprintf("%.6f", usdt), "rate_ngn": "1550.00"})
	}
	if s.Limit > 0 {
		m := &fakeMandate{Mandate: Mandate{ID: f.id("mdt"), MonthlyLimitMinor: s.Limit}, Balance: walletBalance}
		f.mandates[m.ID] = m
		t.Mandate = &m.Mandate
	}
	f.hook("payment.succeeded", t)
	return t.ID, nil
}

// RevokeMandate is the customer revoking a mandate in iSpend.
func (f *Fake) RevokeMandate(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.mandates[id]; ok {
		m.Revoked = true
		f.hook("mandate.revoked", m.Mandate)
	}
}
