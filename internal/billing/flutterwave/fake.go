package flutterwave

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Fake is a stand-in for Flutterwave's sandbox, for tests: the API PGDock
// uses, plus what a customer or a bank would do (complete a checkout, send
// a transfer), with webhooks delivered to WebhookURL.
type Fake struct {
	SecretKey   string
	WebhookHash string
	// WebhookURL receives webhooks; empty sends none.
	WebhookURL string

	mu        sync.Mutex
	down      bool
	drop      int
	declined  map[string]string // token → reason
	next      int
	checkouts map[string]fakeCheckout
	txs       map[string]*fakeTx // by id
	vas       map[string]string  // tx_ref → account number
	lastHook  []byte
	Now       func() time.Time
}

type fakeCheckout struct {
	Amount json.Number
	Email  string
}

type fakeTx struct {
	ID          int         `json:"id"`
	TxRef       string      `json:"tx_ref"`
	FlwRef      string      `json:"flw_ref"`
	Amount      json.Number `json:"amount"`
	AppFee      json.Number `json:"app_fee"`
	Currency    string      `json:"currency"`
	Status      string      `json:"status"`
	PaymentType string      `json:"payment_type"`
	CreatedAt   time.Time   `json:"created_at"`
	Processor   string      `json:"processor_response"`
	Card        *fakeCard   `json:"card,omitempty"`
}

type fakeCard struct {
	Last4  string `json:"last_4digits"`
	Type   string `json:"type"`
	Token  string `json:"token"`
	Expiry string `json:"expiry"`
}

// NewFake returns a fake with the given secrets.
func NewFake(secret, hash string) *Fake {
	return &Fake{SecretKey: secret, WebhookHash: hash, declined: map[string]string{}, checkouts: map[string]fakeCheckout{},
		txs: map[string]*fakeTx{}, vas: map[string]string{}, Now: time.Now}
}

// SetDown makes every API call answer 503 (an outage).
func (f *Fake) SetDown(down bool) { f.mu.Lock(); f.down = down; f.mu.Unlock() }

// DropWebhooks loses the next n webhooks (missed events).
func (f *Fake) DropWebhooks(n int) { f.mu.Lock(); f.drop = n; f.mu.Unlock() }

// Decline makes charges of token fail.
func (f *Fake) Decline(token, reason string) { f.mu.Lock(); f.declined[token] = reason; f.mu.Unlock() }

func write(w http.ResponseWriter, code int, status, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "message": msg, "data": data})
}

func (f *Fake) newTx(ref string, amount json.Number, kind string) *fakeTx {
	f.next++
	fee := kobo(amount) * 14 / 1000 // 1.4%
	t := &fakeTx{ID: 1000 + f.next, TxRef: ref, FlwRef: fmt.Sprintf("FLW-%d", f.next), Amount: amount, AppFee: naira(fee),
		Currency: "NGN", Status: "successful", PaymentType: kind, CreatedAt: f.Now().UTC()}
	f.txs[fmt.Sprint(t.ID)] = t
	return t
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.SecretKey {
		write(w, http.StatusUnauthorized, "error", "Invalid authorization key", nil)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	str := func(k string) string { s, _ := body[k].(string); return s }
	num := func(k string) json.Number { return json.Number(fmt.Sprint(body[k])) }
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/v3/payments":
		cust, _ := body["customer"].(map[string]any)
		email, _ := cust["email"].(string)
		f.checkouts[str("tx_ref")] = fakeCheckout{Amount: num("amount"), Email: email}
		write(w, 200, "success", "Hosted Link", map[string]string{"link": "https://checkout.flutterwave.test/pay/" + str("tx_ref")})
	case r.Method == http.MethodPost && path == "/v3/tokenized-charges":
		if reason, ok := f.declined[str("token")]; ok {
			t := f.newTx(str("tx_ref"), num("amount"), "card")
			t.Status, t.Processor = "failed", reason
			write(w, http.StatusBadRequest, "error", reason, t)
			return
		}
		t := f.newTx(str("tx_ref"), num("amount"), "card")
		t.Card = &fakeCard{Last4: "4081", Type: "VISA", Token: str("token"), Expiry: "09/32"}
		f.hook(t)
		write(w, 200, "success", "Charge successful", t)
	case r.Method == http.MethodPost && path == "/v3/virtual-account-numbers":
		ref := str("tx_ref")
		acct, ok := f.vas[ref]
		if !ok {
			acct = fmt.Sprintf("78%08d", len(f.vas)+1)
			f.vas[ref] = acct
		}
		write(w, 200, "success", "Virtual account created", map[string]string{"account_number": acct, "bank_name": "Wema Bank", "order_ref": "URF_" + ref})
	case r.Method == http.MethodGet && path == "/v3/transactions/verify_by_reference":
		for _, t := range f.txs {
			if t.TxRef == r.URL.Query().Get("tx_ref") {
				write(w, 200, "success", "Transaction fetched", t)
				return
			}
		}
		write(w, http.StatusNotFound, "error", "No transaction was found for this id", nil)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v3/transactions/") && strings.HasSuffix(path, "/verify"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v3/transactions/"), "/verify")
		if t, ok := f.txs[id]; ok {
			write(w, 200, "success", "Transaction fetched", t)
			return
		}
		write(w, http.StatusNotFound, "error", "No transaction was found for this id", nil)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v3/transactions/") && strings.HasSuffix(path, "/refund"):
		f.next++
		write(w, 200, "success", "Transaction refund initiated", map[string]any{"id": 9000 + f.next, "status": "completed"})
	case r.Method == http.MethodGet && path == "/v3/transactions":
		items := []*fakeTx{}
		for _, t := range f.txs {
			items = append(items, t)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "message": "Transactions fetched", "data": items,
			"meta": map[string]any{"page_info": map[string]int{"total": len(items), "current_page": 1, "total_pages": 1}}})
	default:
		write(w, http.StatusNotFound, "error", "not found: "+r.Method+" "+path, nil)
	}
}

// hook delivers a charge.completed webhook (f.mu held).
func (f *Fake) hook(t *fakeTx) {
	body, _ := json.Marshal(map[string]any{"event": "charge.completed", "data": t})
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
	go func() {
		req, err := http.NewRequest(http.MethodPost, f.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("verif-hash", f.WebhookHash)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}()
}

// RedeliverLast sends the last webhook again (a duplicate).
func (f *Fake) RedeliverLast() { f.mu.Lock(); defer f.mu.Unlock(); f.send(f.lastHook) }

// CompleteCheckout is the customer paying on the hosted page with a card
// (token saved), or failing to.
func (f *Fake) CompleteCheckout(ref string, ok bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, found := f.checkouts[ref]
	if !found {
		return "", fmt.Errorf("no checkout %s", ref)
	}
	t := f.newTx(ref, c.Amount, "card")
	t.Card = &fakeCard{Last4: "4081", Type: "VISA", Token: "flw-t1-" + ref, Expiry: "09/32"}
	if !ok {
		t.Status, t.Processor = "failed", "Declined"
	}
	f.hook(t)
	return fmt.Sprint(t.ID), nil
}

// Transfer is a bank transfer into the virtual account with accountNumber.
func (f *Fake) Transfer(accountNumber string, kobo int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ref, acct := range f.vas {
		if acct == accountNumber {
			t := f.newTx(ref, naira(kobo), "bank_transfer")
			f.hook(t)
			return fmt.Sprint(t.ID), nil
		}
	}
	return "", fmt.Errorf("no virtual account %s", accountNumber)
}
