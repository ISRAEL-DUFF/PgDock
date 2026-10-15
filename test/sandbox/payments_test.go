// Package sandbox runs Xenibase's wallet provider client against the
// provider's real sandbox (docs/iswallet.md). It needs sandbox credentials,
// so it runs only with XENIBASE_TEST_SANDBOX=1 (make test-payments-sandbox).
// What needs a person (simulating a bank transfer, redelivering a webhook)
// is in docs/payments.md, "Sandbox rehearsal"; this test checks everything
// the client can do on its own.
package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/xenibase/internal/billing"
	"github.com/israel-duff/xenibase/internal/billing/iswallet"
)

func need(t *testing.T, keys ...string) map[string]string {
	t.Helper()
	if os.Getenv("XENIBASE_TEST_SANDBOX") == "" {
		t.Skip("set XENIBASE_TEST_SANDBOX=1 (make test-payments-sandbox) to run against the provider's sandbox")
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

// TestISWalletSandbox needs XENIBASE_ISWALLET_BASE_URL (the sandbox's),
// XENIBASE_ISWALLET_API_KEY and a director's BVN the sandbox accepts in
// XENIBASE_TEST_SANDBOX_BVN; XENIBASE_ISWALLET_CLIENT_ID is passed on when set.
func TestISWalletSandbox(t *testing.T) {
	env := need(t, "XENIBASE_ISWALLET_BASE_URL", "XENIBASE_ISWALLET_API_KEY", "XENIBASE_TEST_SANDBOX_BVN")
	if strings.Contains(strings.ToLower(env["XENIBASE_ISWALLET_API_KEY"]), "live") {
		t.Fatal("XENIBASE_ISWALLET_API_KEY looks like a live key: refusing to run against a live account")
	}
	if !strings.Contains(strings.ToLower(env["XENIBASE_ISWALLET_BASE_URL"]), "sandbox") && os.Getenv("XENIBASE_SANDBOX_ALLOW_URL") == "" {
		t.Fatal("XENIBASE_ISWALLET_BASE_URL doesn't look like a sandbox; set XENIBASE_SANDBOX_ALLOW_URL=1 if it is one")
	}
	p := iswallet.New(iswallet.Config{BaseURL: env["XENIBASE_ISWALLET_BASE_URL"], APIKey: env["XENIBASE_ISWALLET_API_KEY"],
		ClientID: os.Getenv("XENIBASE_ISWALLET_CLIENT_ID")})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Owner references are unique across everyone using the provider, and
	// the sandbox isn't routinely reset.
	id := uuid.New()
	var wallet billing.Wallet
	t.Run("wallet", func(t *testing.T) {
		var err error
		wallet, err = p.CreateWallet(ctx, billing.WalletRequest{OwnerRef: "xenibase:sandbox:org:" + id.String(), Name: "Xenibase sandbox " + id.String()[:8],
			Email: "sandbox+" + id.String()[:8] + "@xenibase.test", BVN: env["XENIBASE_TEST_SANDBOX_BVN"], Tier: "TIER_2", IdempotencyKey: uuid.NewString()})
		if err != nil || wallet.ID == "" {
			t.Fatalf("create wallet: %+v %v", wallet, err)
		}
		t.Logf("wallet %s (%s, %s)", wallet.ID, wallet.Status, wallet.Tier)
	})

	t.Run("virtual account", func(t *testing.T) {
		if wallet.ID == "" {
			t.Skip("no wallet")
		}
		va, err := p.IssueVirtualAccount(ctx, billing.Customer{OrgID: id, ProviderID: wallet.ID})
		if err != nil {
			t.Fatalf("virtual account: %v", err)
		}
		if len(va.AccountNumber) != 10 || va.BankName == "" {
			t.Fatalf("virtual account %+v: want a 10-digit NUBAN and a bank", va)
		}
		// The rehearsal simulates a transfer into this account (docs/payments.md).
		t.Logf("virtual account for the transfer rehearsal: %s at %s (%s)", va.AccountNumber, va.BankName, va.AccountName)
	})

	t.Run("balance", func(t *testing.T) {
		if wallet.ID == "" {
			t.Skip("no wallet")
		}
		b, err := p.Balance(ctx, wallet.ID)
		if err != nil {
			t.Fatalf("balance: %v", err)
		}
		t.Logf("balance: %d kobo available, %d pending", b.Available, b.Pending)
	})

	t.Run("rate", func(t *testing.T) {
		r, err := p.Rate(ctx, "USD", "NGN", 10_000)
		if err != nil {
			t.Fatalf("rate: %v", err)
		}
		if r.Effective.Sign() <= 0 || r.FetchedAt.IsZero() {
			t.Fatalf("rate %+v", r)
		}
		t.Logf("USD/NGN market %s, effective %s (%d bps), fetched %s", r.Market, r.Effective, r.SpreadBPS, r.FetchedAt.Format(time.RFC3339))
	})

	t.Run("operating wallet", func(t *testing.T) {
		op, err := p.OperatingWallet(ctx)
		if err != nil || op == "" {
			t.Fatalf("operating wallet: %q %v", op, err)
		}
	})
}
