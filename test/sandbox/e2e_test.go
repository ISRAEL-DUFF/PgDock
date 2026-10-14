package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/xenibase/internal/billing"
	"github.com/israel-duff/xenibase/internal/billing/iswallet"
	"github.com/israel-duff/xenibase/internal/store"
	"github.com/israel-duff/xenibase/internal/store/storetest"
)

type quietSuspender struct{}

func (quietSuspender) Suspend(context.Context, uuid.UUID, string) error { return nil }
func (quietSuspender) Reinstate(context.Context, uuid.UUID) error       { return nil }

// TestEndToEndAgainstTheSandbox runs the whole money path with the real
// provider: an organisation opens a wallet, a simulated bank transfer funds
// it, an invoice (priced in dollars) is converted at the provider's rate and
// debited, and the books and the provider agree. Webhooks aren't received here
// (the sandbox can't reach a test process), so the deposit is found by the
// hourly re-query, which is the recovery path for a missed webhook.
// It also needs a Postgres (XENIBASE_TEST_DATABASE_URL).
func TestEndToEndAgainstTheSandbox(t *testing.T) {
	env := need(t, "XENIBASE_ISWALLET_BASE_URL", "XENIBASE_ISWALLET_API_KEY", "XENIBASE_TEST_SANDBOX_BVN")
	base, key := env["XENIBASE_ISWALLET_BASE_URL"], env["XENIBASE_ISWALLET_API_KEY"]
	db := storetest.New(t)
	ctx := context.Background()
	p := iswallet.New(iswallet.Config{BaseURL: base, APIKey: key, WebhookSecret: "unused-here-0123456789",
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	s := billing.New(db, nil, "https://xenibase.example", slog.New(slog.NewTextHandler(io.Discard, nil)))
	prefix := "xenibase:sandbox-test:" + uuid.NewString()[:8]
	s.SetWallets(p, prefix)
	s.SetDunning(quietSuspender{}, nil)
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}

	var org uuid.UUID
	if err := db.QueryRow(ctx, `INSERT INTO organizations (name, slug, plan_id) SELECT 'Sandbox Co', 'sandbox-co', id FROM quota_plans WHERE name = 'Personal' RETURNING id`).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddContact(ctx, org, "ap@sandbox.example", nil); err != nil {
		t.Fatal(err)
	}
	wid, err := s.EnsureWallet(ctx, org, billing.WalletSetup{BusinessName: "Sandbox Co Ltd", Email: "ap@sandbox.example", BVN: env["XENIBASE_TEST_SANDBOX_BVN"]})
	if err != nil {
		t.Fatal(err)
	}
	va, err := s.EnsureTransferAccount(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("wallet %s, account %s at %s", wid, va.AccountNumber, va.BankName)
	if _, err := s.AcceptDebitTerms(ctx, org, nil, billing.DebitTermsVersion); err != nil {
		t.Fatal(err)
	}

	// A bank transfer of ₦100,000, simulated at the provider.
	deposit(t, base, key, va.AccountNumber, 100_000_00)
	var held int64
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
		b, err := p.Balance(ctx, wid)
		if err != nil {
			t.Fatal(err)
		}
		if held = b.Total; held > 0 {
			break
		}
	}
	if held == 0 {
		t.Fatal("the deposit never reached the wallet")
	}
	t.Logf("the wallet was credited %d kobo (the transfer was 10000000: the collection fee came off)", held)

	// The webhook isn't received here: the hourly re-query finds the deposit.
	n, err := s.Requery(ctx, time.Now().Add(-2*time.Hour), time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("re-query booked %d deposits: %v", n, err)
	}
	led, err := billing.Balance(ctx, db, &org, billing.AccWalletBalance)
	if err != nil || -led != held {
		t.Fatalf("the books hold %d for the wallet, the provider %d (%v)", -led, held, err)
	}

	// An invoice: Pro from the 1st, drafted for this month, issued at today's rate.
	now := time.Now().UTC()
	if _, err := s.ChangePlan(ctx, org, billing.PlanRequest{Plan: billing.PlanPro}); err != nil {
		t.Fatal(err)
	}
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	draft, err := s.Draft(ctx, org, month)
	if err != nil || draft == nil {
		t.Fatalf("draft: %v", err)
	}
	issued, err := s.Issue(ctx, draft.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	var paid store.Invoice
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if paid, err = store.New(db).GetInvoice(ctx, issued.ID); err != nil {
			t.Fatal(err)
		}
		if paid.Status == billing.StatusPaid {
			break
		}
	}
	var fx billing.FXQuote
	_ = json.Unmarshal(paid.Fx, &fx)
	t.Logf("invoice %s: $%.2f converted at %s per $1 (%s) = %d kobo; status %s", *paid.Number, float64(*paid.UsdTotalMinor)/100, fx.Effective, fx.Sources, paid.TotalMinor, paid.Status)
	if paid.Status != billing.StatusPaid {
		t.Fatalf("the invoice wasn't paid from the wallet: %s", paid.Status)
	}
	b, err := p.Balance(ctx, wid)
	if err != nil || b.Total != held-paid.TotalMinor {
		t.Fatalf("the wallet holds %d, want %d (%v)", b.Total, held-paid.TotalMinor, err)
	}
	if led, _ = billing.Balance(ctx, db, &org, billing.AccWalletBalance); -led != b.Total {
		t.Fatalf("the books hold %d for the wallet, the provider %d", -led, b.Total)
	}
	if probs, err := billing.Check(ctx, db); err != nil || len(probs) > 0 {
		t.Fatalf("ledger: %+v %v", probs, err)
	}
	rs, err := s.Reconcile(ctx, time.Now().Add(-6*time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if len(r.Differences) > 0 {
			t.Errorf("reconciliation %s: %+v", r.Provider, r.Differences)
		}
	}
}

// deposit simulates a bank transfer into a virtual account (sandbox only).
func deposit(t *testing.T, base, key, account string, kobo int64) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"account_number": account, "amount": kobo, "currency": "NGN",
		"sender_name": "Sandbox Payer", "sender_account_number": "0123456789", "sender_bank_name": "Test Bank"})
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/sandbox/simulate/deposit", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("simulate deposit: %d %s", res.StatusCode, raw)
	}
	t.Logf("simulated deposit: %.300s", raw)
}
