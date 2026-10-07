// Package billing is PGDock's billing core (V3 §3): price books, billing
// accounts, the double-entry ledger, rating, invoices and credit notes,
// the forecast and spend controls. Amounts are integers in kobo.
package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Ledger accounts (V3 §3.3). Per-organisation accounts carry the org;
// platform accounts don't.
const (
	AccReceivable    = "receivable"     // what the org owes (postpaid)
	AccCreditBalance = "credit_balance" // prepaid funds and credits the org holds
	AccWHTReceivable = "wht_receivable" // WHT deducted, awaiting the credit note
	AccVATPayable    = "vat_payable"    // VAT collected, owed to the tax authority
	AccCreditsIssued = "credits_issued" // SLA and promotional credits
	// Prefixed platform accounts.
	AccRevenuePrefix = "revenue:" // revenue:<plan or product line>
	AccCashPrefix    = "cash:"    // cash:flutterwave, cash:ispend, cash:bank
	AccFeesPrefix    = "fees:"    // fees:<provider>
)

// orgAccount reports whether account belongs to one organisation.
func orgAccount(account string) bool {
	switch account {
	case AccReceivable, AccCreditBalance, AccWHTReceivable:
		return true
	}
	return false
}

func validAccount(account string) bool {
	switch account {
	case AccReceivable, AccCreditBalance, AccWHTReceivable, AccVATPayable, AccCreditsIssued:
		return true
	}
	for _, p := range []string{AccRevenuePrefix, AccCashPrefix, AccFeesPrefix} {
		if rest, ok := strings.CutPrefix(account, p); ok && rest != "" && !strings.ContainsAny(rest, " \t\n") {
			return true
		}
	}
	return false
}

// Source types of ledger transactions.
const (
	SourceInvoice    = "invoice"
	SourcePayment    = "payment"
	SourceUsage      = "usage"
	SourceWHT        = "wht"
	SourceRefund     = "refund"
	SourceCredit     = "credit"
	SourceAdjustment = "adjustment"
)

// Entry is one side of a ledger transaction.
type Entry struct {
	Account string
	Org     *uuid.UUID // required for an org's accounts
	Debit   bool
	Amount  int64 // kobo, > 0
}

// Dr builds a debit entry. Platform entries may name the org they arose
// from (revenue by org); an org's own accounts must.
func Dr(account string, org *uuid.UUID, amount int64) Entry {
	return Entry{Account: account, Org: org, Debit: true, Amount: amount}
}

// Cr builds a credit entry.
func Cr(account string, org *uuid.UUID, amount int64) Entry {
	return Entry{Account: account, Org: org, Amount: amount}
}

// Txn is one business event: entries whose debits equal their credits.
type Txn struct {
	// Key makes posting idempotent: posting the same key again is a no-op.
	Key        string
	SourceType string
	SourceID   string
	Memo       string
	By         *uuid.UUID
	Entries    []Entry
}

// ErrUnbalanced is a transaction whose debits and credits differ.
var ErrUnbalanced = errors.New("ledger: unbalanced transaction")

// ledgerNS derives transaction ids from idempotency keys.
var ledgerNS = uuid.MustParse("8b6f3f1c-6c55-4b40-9a54-2c7f0d6a51e1")

// TxnID is the id of the transaction posted under key.
func TxnID(key string) uuid.UUID { return uuid.NewSHA1(ledgerNS, []byte(key)) }

// Validate checks t without posting it.
func (t Txn) Validate() error {
	if t.Key == "" || t.SourceID == "" {
		return errors.New("ledger: a transaction needs an idempotency key and a source")
	}
	switch t.SourceType {
	case SourceInvoice, SourcePayment, SourceUsage, SourceWHT, SourceRefund, SourceCredit, SourceAdjustment:
	default:
		return fmt.Errorf("ledger: unknown source type %q", t.SourceType)
	}
	if len(t.Entries) < 2 {
		return errors.New("ledger: a transaction needs at least two entries")
	}
	var dr, cr int64
	for _, e := range t.Entries {
		if !validAccount(e.Account) {
			return fmt.Errorf("ledger: unknown account %q", e.Account)
		}
		if orgAccount(e.Account) && e.Org == nil {
			return fmt.Errorf("ledger: %s needs an organisation", e.Account)
		}
		if e.Amount <= 0 {
			return fmt.Errorf("ledger: %s: amount must be positive (got %d)", e.Account, e.Amount)
		}
		if e.Debit {
			dr += e.Amount
		} else {
			cr += e.Amount
		}
	}
	if dr != cr {
		return fmt.Errorf("%w: debits %d, credits %d", ErrUnbalanced, dr, cr)
	}
	return nil
}

// Beginner is a pool or a transaction.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Post records t, in a transaction of its own or, given one, a savepoint
// of the caller's (post with the change the entries account for). The
// database checks the balance again when the outer transaction commits. It
// returns false when t's key was already posted.
func Post(ctx context.Context, db Beginner, t Txn) (bool, error) {
	if err := t.Validate(); err != nil {
		return false, err
	}
	var posted bool
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var err error
		posted, err = post(ctx, tx, t)
		return err
	})
	return posted, err
}

func post(ctx context.Context, db store.DBTX, t Txn) (bool, error) {
	q := store.New(db)
	txn := TxnID(t.Key)
	var memo *string
	if t.Memo != "" {
		memo = &t.Memo
	}
	for i, e := range t.Entries {
		dir := "credit"
		if e.Debit {
			dir = "debit"
		}
		_, err := q.InsertLedgerEntry(ctx, store.InsertLedgerEntryParams{
			TxnID: txn, OrgID: e.Org, Account: e.Account, Direction: dir, AmountMinor: e.Amount,
			SourceType: t.SourceType, SourceID: t.SourceID, IdempotencyKey: fmt.Sprintf("%s#%d", t.Key, i), Memo: memo, CreatedBy: t.By,
		})
		if errors.Is(err, pgx.ErrNoRows) { // the key exists
			if i != 0 {
				return false, fmt.Errorf("ledger: %s was partly posted before", t.Key)
			}
			n, err := q.CountLedgerTxn(ctx, txn)
			if err != nil {
				return false, err
			}
			if int(n) != len(t.Entries) {
				return false, fmt.Errorf("ledger: %s was posted before with %d entries, now %d", t.Key, n, len(t.Entries))
			}
			return false, nil // already posted
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

// Balance is an account's balance, debits minus credits (positive for what
// an org owes on receivable; negative for funds it holds on
// credit_balance). A nil org totals the account across organisations (for
// platform accounts).
func Balance(ctx context.Context, db store.DBTX, org *uuid.UUID, account string) (int64, error) {
	return store.New(db).LedgerBalance(ctx, store.LedgerBalanceParams{OrgID: org, Account: account})
}

// Problem is a broken ledger invariant: an unbalanced transaction (Txn,
// Debits, Credits), or, with Kind set, a ledger that disagrees with the
// invoices, credit notes and payments it records (the M27 billing audit).
type Problem struct {
	Txn     uuid.UUID
	Debits  int64
	Credits int64
	// Kind: invoice_ledger, invoice_arithmetic, invoice_allocations,
	// invoice_outstanding, credit_note_ledger, payment_ledger, receivable,
	// account_sign.
	Kind   string
	Ref    string
	Detail string
}

// Check verifies the ledger's invariants: every transaction balances, so
// all debits equal all credits; every issued invoice, credit note and
// payment has the transaction its amounts say; each invoice's lines, VAT
// and total agree, and what it records as paid is what was allocated to
// it; each organisation's receivable is what its open invoices owe; and
// no organisation's WHT receivable or credit flips sign.
func Check(ctx context.Context, db store.DBTX) ([]Problem, error) {
	q := store.New(db)
	rows, err := q.UnbalancedLedgerTxns(ctx)
	if err != nil {
		return nil, err
	}
	var out []Problem
	for _, r := range rows {
		out = append(out, Problem{Txn: r.TxnID, Debits: r.Debits, Credits: r.Credits})
	}
	t, err := q.LedgerTotals(ctx)
	if err != nil {
		return nil, err
	}
	if t.Debits != t.Credits && len(out) == 0 {
		out = append(out, Problem{Debits: t.Debits, Credits: t.Credits})
	}
	audit, err := q.BillingAuditProblems(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range audit {
		out = append(out, Problem{Kind: a.Kind, Ref: a.Ref, Detail: a.Detail})
	}
	return out, nil
}
