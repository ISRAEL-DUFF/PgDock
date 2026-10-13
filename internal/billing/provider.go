package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Providers and channels (V3 §3.4).
const (
	ProviderBank = "bank" // manual payments recorded by the platform admin

	ChannelWallet   = "wallet"   // a debit of an organisation's wallet
	ChannelTransfer = "transfer" // a bank transfer into a virtual account
	ChannelManual   = "manual"

	PurposeInvoice = "invoice"
)

// Normalised provider events (V3 §3.4.1).
const (
	EventPaymentSucceeded = "payment.succeeded"
	EventTransferReceived = "transfer.received"
)

// Capabilities is what a provider can do.
type Capabilities struct {
	VirtualAccounts bool
	Wallet          bool
}

// Customer is an org as a provider sees it.
type Customer struct {
	OrgID uuid.UUID
	// ProviderID is the provider's id for the org (its wallet's).
	ProviderID string
}

// Transaction statuses.
const (
	TxSucceeded = "succeeded"
	TxFailed    = "failed"
	TxPending   = "pending"
)

// Transaction is a provider's authoritative view of a payment.
type Transaction struct {
	ProviderRef string
	Status      string
	AmountMinor int64
	FeeMinor    int64
	Currency    string
	Channel     string
	// AccountNumber is the virtual account a transfer was paid into.
	AccountNumber string
	At            time.Time
}

// VirtualAccount is a permanent account number for transfers.
type VirtualAccount struct {
	AccountNumber string
	BankName      string
	AccountName   string
	ProviderRef   string
}

// Event is a provider webhook, authenticated and normalised.
type Event struct {
	ID          string // the provider's event id (or transaction id)
	Kind        string
	ProviderRef string
	AmountMinor int64
	Currency    string
	Raw         json.RawMessage
}

// PaymentProvider is one payment provider (V3 §3.4.1). Billing logic only
// ever talks to providers through it.
type PaymentProvider interface {
	Name() string
	Capabilities() Capabilities
	IssueVirtualAccount(ctx context.Context, c Customer) (VirtualAccount, error)
	Verify(ctx context.Context, providerRef string) (Transaction, error)
	ParseWebhook(ctx context.Context, r *http.Request) (Event, error)
	ListTransactions(ctx context.Context, from, to time.Time) ([]Transaction, error)
}

// Provider errors.
var (
	// ErrUnavailable is a provider outage: retried, and never counted
	// against the customer (V3 §3.4.8).
	ErrUnavailable = errors.New("payment provider unavailable")
	// ErrUnauthenticated is a webhook that fails its signature check.
	ErrUnauthenticated = errors.New("webhook authentication failed")
	// ErrIgnoredEvent is a webhook Xenibase doesn't act on.
	ErrIgnoredEvent = errors.New("event not handled")
)
