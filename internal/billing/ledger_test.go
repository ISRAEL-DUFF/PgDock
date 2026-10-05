package billing_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/billing"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

func newOrg(t *testing.T, db *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	var org uuid.UUID
	if err := db.QueryRow(context.Background(), `INSERT INTO organizations (name, slug, plan_id)
		SELECT $1::text, $1::text, id FROM quota_plans WHERE name = 'Personal' RETURNING id`, slug).Scan(&org); err != nil {
		t.Fatal(err)
	}
	return org
}

// issue is an invoice for 10,750 kobo: 10,000 revenue and 750 VAT.
func issue(org uuid.UUID, key string) billing.Txn {
	return billing.Txn{
		Key: key, SourceType: billing.SourceInvoice, SourceID: key,
		Entries: []billing.Entry{
			billing.Dr(billing.AccReceivable, &org, 10750),
			billing.Cr("revenue:pro", &org, 10000),
			billing.Cr(billing.AccVATPayable, &org, 750),
		},
	}
}

func mustCheck(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	probs, err := billing.Check(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(probs) > 0 {
		t.Fatalf("ledger invariant broken: %+v", probs)
	}
}

func balance(t *testing.T, db *pgxpool.Pool, org *uuid.UUID, account string) int64 {
	t.Helper()
	b, err := billing.Balance(context.Background(), db, org, account)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValidate(t *testing.T) {
	org := uuid.New()
	ok := issue(org, "inv-1")
	if err := ok.Validate(); err != nil {
		t.Fatalf("balanced txn: %v", err)
	}
	cases := map[string]func(*billing.Txn){
		"unbalanced":     func(x *billing.Txn) { x.Entries[2].Amount = 700 },
		"one entry":      func(x *billing.Txn) { x.Entries = x.Entries[:1] },
		"zero amount":    func(x *billing.Txn) { x.Entries[1].Amount, x.Entries[2].Amount, x.Entries[0].Amount = 0, 750, 750 },
		"no key":         func(x *billing.Txn) { x.Key = "" },
		"no source":      func(x *billing.Txn) { x.SourceID = "" },
		"bad source":     func(x *billing.Txn) { x.SourceType = "gift" },
		"bad account":    func(x *billing.Txn) { x.Entries[1].Account = "revenue" },
		"org account":    func(x *billing.Txn) { x.Entries[0].Org = nil },
		"spaced account": func(x *billing.Txn) { x.Entries[1].Account = "revenue:a b" },
	}
	for name, mut := range cases {
		x := issue(org, "inv-1")
		mut(&x)
		if err := x.Validate(); err == nil {
			t.Errorf("%s: Validate accepted it", name)
		}
	}
	x := issue(org, "inv-1")
	x.Entries[0].Amount = 1
	if err := x.Validate(); !errors.Is(err, billing.ErrUnbalanced) {
		t.Errorf("unbalanced: got %v, want ErrUnbalanced", err)
	}
}

func TestPostAndBalances(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	other := newOrg(t, db, "globex")

	for _, x := range []billing.Txn{issue(org, "inv-1"), issue(other, "inv-2")} {
		posted, err := billing.Post(ctx, db, x)
		if err != nil || !posted {
			t.Fatalf("post %s: %v %v", x.Key, posted, err)
		}
	}
	// acme pays in full by card; the provider keeps a fee.
	pay := billing.Txn{
		Key: "pay-1", SourceType: billing.SourcePayment, SourceID: "flw-123",
		Entries: []billing.Entry{
			billing.Dr("cash:flutterwave", &org, 10600),
			billing.Dr("fees:flutterwave", &org, 150),
			billing.Cr(billing.AccReceivable, &org, 10750),
		},
	}
	if _, err := billing.Post(ctx, db, pay); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 0 {
		t.Errorf("acme receivable = %d, want 0", b)
	}
	if b := balance(t, db, &other, billing.AccReceivable); b != 10750 {
		t.Errorf("globex receivable = %d, want 10750", b)
	}
	if b := balance(t, db, nil, "revenue:pro"); b != -20000 {
		t.Errorf("revenue across orgs = %d, want -20000", b)
	}
	if b := balance(t, db, &org, "revenue:pro"); b != -10000 {
		t.Errorf("acme revenue = %d, want -10000", b)
	}
	mustCheck(t, db)
}

func TestPostIsIdempotent(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")

	x := issue(org, "inv-1")
	if posted, err := billing.Post(ctx, db, x); err != nil || !posted {
		t.Fatalf("first post: %v %v", posted, err)
	}
	if posted, err := billing.Post(ctx, db, x); err != nil || posted {
		t.Fatalf("second post: posted=%v err=%v, want a no-op", posted, err)
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 10750 {
		t.Errorf("receivable = %d after a repeat, want 10750", b)
	}
	// The same key with different entries is a bug in the caller.
	y := x
	y.Entries = append(append([]billing.Entry{}, x.Entries[:2]...), billing.Cr(billing.AccVATPayable, &org, 500), billing.Cr(billing.AccVATPayable, &org, 250))
	if _, err := billing.Post(ctx, db, y); err == nil {
		t.Error("re-posting a key with other entries succeeded")
	}
	if want := billing.TxnID("inv-1"); want != billing.TxnID("inv-1") || want == billing.TxnID("inv-2") {
		t.Error("TxnID is not a stable function of the key")
	}
	mustCheck(t, db)
}

func TestPostInsideCallersTransaction(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")

	// The caller's change and the posting commit or roll back together.
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := billing.Post(ctx, tx, issue(org, "inv-1")); err != nil {
			return err
		}
		return errors.New("the invoice could not be saved")
	})
	if err == nil {
		t.Fatal("expected the caller's error")
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 0 {
		t.Errorf("rolled-back posting left receivable = %d", b)
	}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		_, err := billing.Post(ctx, tx, issue(org, "inv-1"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, db, &org, billing.AccReceivable); b != 10750 {
		t.Errorf("receivable = %d, want 10750", b)
	}
}

// The database refuses what Post never sends: unbalanced transactions and
// changes to posted entries.
func TestLedgerTriggers(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	org := newOrg(t, db, "acme")
	if _, err := billing.Post(ctx, db, issue(org, "inv-1")); err != nil {
		t.Fatal(err)
	}

	raw := func(tx pgx.Tx, txn uuid.UUID, key, dir string, amount int64) error {
		_, err := tx.Exec(ctx, `INSERT INTO ledger_entries (txn_id, org_id, account, direction, amount_minor, source_type, source_id, idempotency_key)
			VALUES ($1, $2, 'receivable', $3, $4, 'adjustment', 'x', $5)`, txn, org, dir, amount, key)
		return err
	}
	txn := uuid.New()
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := raw(tx, txn, "raw#0", "debit", 500); err != nil {
			return err
		}
		return raw(tx, txn, "raw#1", "credit", 400)
	})
	if err == nil || !strings.Contains(err.Error(), "unbalanced") {
		t.Fatalf("unbalanced raw insert: %v", err)
	}
	// Balanced within the transaction, though not after each statement:
	// the check waits for the commit.
	txn = uuid.New()
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if err := raw(tx, txn, "raw#0", "debit", 500); err != nil {
			return err
		}
		return raw(tx, txn, "raw#1", "credit", 500)
	}); err != nil {
		t.Fatalf("balanced raw insert: %v", err)
	}

	for _, sql := range []string{
		`UPDATE ledger_entries SET amount_minor = amount_minor + 1`,
		`DELETE FROM ledger_entries`,
		`TRUNCATE ledger_entries`,
	} {
		if _, err := db.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: %v, want refused", sql, err)
		}
	}
	mustCheck(t, db)
}

func TestPublishedPriceBookIsFrozen(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `INSERT INTO price_books (version, effective_at, prices) VALUES (900, now(), '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE price_books SET notes = 'draft edit' WHERE version = 900`); err != nil {
		t.Fatalf("editing a draft: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE price_books SET published_at = now() WHERE version = 900`); err != nil {
		t.Fatalf("publishing: %v", err)
	}
	for _, sql := range []string{
		`UPDATE price_books SET prices = '{"x": 1}' WHERE version = 900`,
		`DELETE FROM price_books WHERE version = 900`,
	} {
		if _, err := db.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "published") {
			t.Errorf("%s: %v, want refused", sql, err)
		}
	}
}
