package iswallet

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/billing"
)

func hookRequest(c *Client, body string) *http.Request {
	ts := c.cfg.Now().Unix()
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	r.Header.Set("X-iSpend-Timestamp", strconv.FormatInt(ts, 10))
	r.Header.Set("X-iSpend-Signature", Sign(c.cfg.WebhookSecret, ts, []byte(body)))
	return r
}

func TestWebhookDeposits(t *testing.T) {
	c := New(Config{WebhookSecret: "secret-0123456789ab"})
	ev, err := c.ParseWebhook(context.Background(), hookRequest(c,
		`{"event_type":"wallet.credit.posted","wallet_id":"w1","data":{"wallet_id":"w1","amount":5000000,"amount_credited":4930000,"currency":"NGN","txn_id":"t1","source_type":"va_deposit"}}`))
	if err != nil || ev.AmountMinor != 4930000 || ev.ProviderRef != "w1:t1" || ev.Kind != billing.EventTransferReceived {
		t.Fatalf("a bank transfer books what the wallet got: %+v %v", ev, err)
	}
	// The payer's gross is never a default.
	if _, err := c.ParseWebhook(context.Background(), hookRequest(c,
		`{"event_type":"wallet.credit.posted","data":{"wallet_id":"w1","amount":5000000,"txn_id":"t2","source_type":"va_deposit"}}`)); err == nil || errors.Is(err, billing.ErrIgnoredEvent) {
		t.Fatalf("a deposit with no amount_credited must be an error, got %v", err)
	}
	// Our own transfers, other collections and reversals are not deposits.
	for _, body := range []string{
		`{"event_type":"wallet.credit.posted","data":{"wallet_id":"w1","amount_credited":1,"txn_id":"t3","source_type":"wallet_transfer"}}`,
		`{"event_type":"wallet.credit.posted","data":{"wallet_id":"w1","amount_credited":1,"txn_id":"t4","source_type":"pull_inflow"}}`,
		`{"event_type":"wallet.credit.reversed","wallet_id":"w1","data":{"amount":4930000,"uncovered_amount":100,"txn_id":"t5"}}`,
		`{"event_type":"wallet.transfer.completed","data":{"txn_id":"t6"}}`,
	} {
		if _, err := c.ParseWebhook(context.Background(), hookRequest(c, body)); !errors.Is(err, billing.ErrIgnoredEvent) {
			t.Fatalf("%s: %v", body, err)
		}
	}
	// A forged one is refused.
	r := hookRequest(c, `{"event_type":"wallet.credit.posted"}`)
	r.Header.Set("X-iSpend-Signature", "sha256=00")
	if _, err := c.ParseWebhook(context.Background(), r); !errors.Is(err, billing.ErrUnauthenticated) {
		t.Fatalf("forged: %v", err)
	}
	// And an old one.
	old := New(Config{WebhookSecret: "secret-0123456789ab", Now: func() time.Time { return time.Now().Add(time.Hour) }})
	r = hookRequest(c, `{"event_type":"wallet.credit.posted"}`)
	if _, err := old.ParseWebhook(context.Background(), r); !errors.Is(err, billing.ErrUnauthenticated) {
		t.Fatalf("replayed: %v", err)
	}
}

func TestTransactionStates(t *testing.T) {
	state := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"txn_id":"t1","type":"inflow","direction":"credit","status":"` + state + `","amount":5000000,"amount_credited":4930000,"currency":"NGN"}`))
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL})
	for s, want := range map[string]string{
		"CONFIRMED_SUCCESS": billing.TxSucceeded, "confirmed": billing.TxSucceeded, "PENDING": billing.TxPending, "pending": billing.TxPending,
		"CONFIRMED_FAILURE": billing.TxFailed, "failed": billing.TxFailed, "REVERSED": billing.TxFailed,
		"UNRESOLVED": billing.TxPending, "MANUAL_RESOLVED": billing.TxPending, "SOMETHING_NEW": billing.TxPending,
	} {
		state = s
		tx, err := c.Verify(context.Background(), "w1:t1")
		if err != nil || tx.Status != want || tx.AmountMinor != 4930000 {
			t.Errorf("%s: status %q amount %d, %v (want %s)", s, tx.Status, tx.AmountMinor, err, want)
		}
	}
}

func TestVerifyNeedsTheCreditedAmount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"txn_id":"t1","type":"inflow","direction":"credit","status":"CONFIRMED_SUCCESS","amount":5000000,"currency":"NGN"}`))
	}))
	defer srv.Close()
	if _, err := New(Config{BaseURL: srv.URL}).Verify(context.Background(), "w1:t1"); err == nil {
		t.Fatal("booked a deposit with only the payer's gross")
	}
}
