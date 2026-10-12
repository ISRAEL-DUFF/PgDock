// Package sandbox runs PGDock's payment provider clients against the
// providers' real sandboxes (V4.1 §13). It needs sandbox credentials, so it
// runs only with PGDOCK_TEST_SANDBOX=1 (make test-payments-sandbox) and
// skips each provider whose keys aren't set. What needs a person (paying on
// a hosted page, simulating a transfer) is in docs/payments.md, "Sandbox
// rehearsal"; this test checks everything the clients can do on their own.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/billing/flutterwave"
	"github.com/israel-duff/pgdock/internal/billing/ispend"
)

func need(t *testing.T, keys ...string) map[string]string {
	t.Helper()
	if os.Getenv("PGDOCK_TEST_SANDBOX") == "" {
		t.Skip("set PGDOCK_TEST_SANDBOX=1 (make test-payments-sandbox) to run against the providers' sandboxes")
	}
	out := map[string]string{}
	for _, k := range keys {
		v := os.Getenv(k)
		if v == "" {
			t.Skipf("%s is not set", k)
		}
		out[k] = v
	}
	return out
}

func customer() billing.Customer {
	id := uuid.New()
	return billing.Customer{OrgID: id, Name: "PGDock sandbox " + id.String()[:8], Email: "sandbox+" + id.String()[:8] + "@pgdock.test"}
}

// checkProvider runs what any provider must do without a person. pay, when
// set (the fakes), pays the checkout so the paid reference is verified too.
func checkProvider(t *testing.T, p billing.PaymentProvider, channel string, verifyRef string, pay func(ref, url string) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := customer()

	t.Run("customer", func(t *testing.T) {
		id, err := p.CreateCustomer(ctx, c)
		if err != nil && !errors.Is(err, billing.ErrUnsupported) {
			t.Fatalf("create customer: %v", err)
		}
		c.ProviderID = id
	})

	t.Run("checkout", func(t *testing.T) {
		ref := "pgd_sandbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		s, err := p.Checkout(ctx, billing.CheckoutRequest{Reference: ref, AmountMinor: 10_000, Customer: c, Channel: channel,
			Purpose: billing.PurposeTopup, Description: "PGDock sandbox rehearsal", RedirectURL: "https://pgdock.test/billing"})
		if err != nil {
			t.Fatalf("checkout: %v", err)
		}
		if !strings.HasPrefix(s.URL, "https://") {
			t.Fatalf("checkout URL %q", s.URL)
		}
		// The rehearsal pays this page by hand (docs/payments.md).
		t.Logf("hosted page for %s (reference %s): %s", p.Name(), ref, s.URL)
		// Before payment the provider must not report it as succeeded.
		if tx, err := p.VerifyReference(ctx, ref); err == nil && tx.Status == billing.TxSucceeded {
			t.Fatalf("an unpaid checkout verifies as succeeded: %+v", tx)
		}
		if pay != nil {
			if err := pay(ref, s.URL); err != nil {
				t.Fatal(err)
			}
			verifyRef = ref
		}
	})

	t.Run("virtual account", func(t *testing.T) {
		if !p.Capabilities().VirtualAccounts {
			t.Skip("no virtual accounts")
		}
		va, err := p.IssueVirtualAccount(ctx, c)
		if errors.Is(err, billing.ErrUnsupported) {
			t.Skip(err)
		}
		if err != nil {
			t.Fatalf("virtual account: %v", err)
		}
		if len(va.AccountNumber) != 10 || va.BankName == "" {
			t.Fatalf("virtual account %+v: want a 10-digit NUBAN and a bank", va)
		}
		t.Logf("%s virtual account for the transfer rehearsal: %s at %s (%s)", p.Name(), va.AccountNumber, va.BankName, va.AccountName)
	})

	t.Run("transactions", func(t *testing.T) {
		txs, err := p.ListTransactions(ctx, time.Now().Add(-24*time.Hour), time.Now())
		if err != nil {
			t.Fatalf("list transactions: %v", err)
		}
		t.Logf("%s: %d transactions in the last 24 hours", p.Name(), len(txs))
		for _, tx := range txs {
			if tx.ProviderRef == "" || tx.Status == "" {
				t.Errorf("a transaction without a reference or status: %+v", tx)
			}
			if tx.Status == billing.TxSucceeded && !strings.EqualFold(tx.Currency, "NGN") {
				t.Logf("note: %s is in %s; PGDock posts NGN only", tx.ProviderRef, tx.Currency)
			}
		}
	})

	t.Run("verify a paid reference", func(t *testing.T) {
		if verifyRef == "" {
			t.Skip("set the paid reference from the rehearsal to check it verifies")
		}
		tx, err := p.VerifyReference(ctx, verifyRef)
		if err != nil {
			t.Fatalf("verify %s: %v", verifyRef, err)
		}
		if tx.Status != billing.TxSucceeded || !strings.EqualFold(tx.Currency, "NGN") || tx.AmountMinor <= 0 {
			t.Fatalf("verify %s: %+v", verifyRef, tx)
		}
		t.Logf("verified %s: %s", verifyRef, fmt.Sprintf("NGN %d.%02d, fee %d kobo", tx.AmountMinor/100, tx.AmountMinor%100, tx.FeeMinor))
	})
}

// TestFlutterwaveSandbox needs PGDOCK_FLW_SECRET_KEY (a test key,
// FLWSECK_TEST-…) and optionally PGDOCK_FLW_BASE_URL, PGDOCK_FLW_BVN (for
// virtual accounts) and PGDOCK_SANDBOX_FLW_PAID_REF.
func TestFlutterwaveSandbox(t *testing.T) {
	env := need(t, "PGDOCK_FLW_SECRET_KEY")
	if !strings.Contains(env["PGDOCK_FLW_SECRET_KEY"], "TEST") {
		t.Fatal("PGDOCK_FLW_SECRET_KEY is not a test key (FLWSECK_TEST-…): refusing to run against a live account")
	}
	p := flutterwave.New(flutterwave.Config{BaseURL: os.Getenv("PGDOCK_FLW_BASE_URL"), SecretKey: env["PGDOCK_FLW_SECRET_KEY"],
		WebhookHash: os.Getenv("PGDOCK_FLW_WEBHOOK_HASH"), BVN: os.Getenv("PGDOCK_FLW_BVN")})
	checkProvider(t, p, billing.ChannelCard, os.Getenv("PGDOCK_SANDBOX_FLW_PAID_REF"), nil)
}

// TestISpendSandbox needs PGDOCK_ISPEND_BASE_URL (the sandbox's) and
// PGDOCK_ISPEND_API_KEY, and optionally PGDOCK_SANDBOX_ISPEND_PAID_REF.
func TestISpendSandbox(t *testing.T) {
	env := need(t, "PGDOCK_ISPEND_BASE_URL", "PGDOCK_ISPEND_API_KEY")
	if !strings.Contains(strings.ToLower(env["PGDOCK_ISPEND_BASE_URL"]), "sandbox") && os.Getenv("PGDOCK_SANDBOX_ALLOW_URL") == "" {
		t.Fatal("PGDOCK_ISPEND_BASE_URL doesn't look like a sandbox; set PGDOCK_SANDBOX_ALLOW_URL=1 if it is one")
	}
	p := ispend.New(ispend.Config{BaseURL: env["PGDOCK_ISPEND_BASE_URL"], APIKey: env["PGDOCK_ISPEND_API_KEY"],
		WebhookSecret: os.Getenv("PGDOCK_ISPEND_WEBHOOK_SECRET")})
	checkProvider(t, p, billing.ChannelWallet, os.Getenv("PGDOCK_SANDBOX_ISPEND_PAID_REF"), nil)
}
