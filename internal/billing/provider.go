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
	ProviderFlutterwave = "flutterwave"
	ProviderISpend      = "ispend"
	ProviderBank        = "bank" // manual payments recorded by the platform admin

	ChannelCard       = "card"       // a hosted checkout
	ChannelSavedCard  = "saved_card" // a tokenised charge
	ChannelWallet     = "wallet"     // Pay with iSpend
	ChannelMandate    = "mandate"    // a charge against an iSpend mandate
	ChannelTransfer   = "transfer"   // into a virtual account
	ChannelStablecoin = "stablecoin" // a USDT top-up through iSpend
	ChannelManual     = "manual"

	PurposeInvoice   = "invoice"
	PurposeTopup     = "topup"
	PurposeCardSetup = "card_setup"
)

// Normalised provider events (V3 §3.4.1).
const (
	EventPaymentSucceeded = "payment.succeeded"
	EventPaymentFailed    = "payment.failed"
	EventTransferReceived = "transfer.received"
	EventRefundCompleted  = "refund.completed"
	EventMandateRevoked   = "mandate.revoked"
)

// Capabilities is what a provider can do.
type Capabilities struct {
	Cards           bool // hosted card checkout
	SavedCharges    bool // tokenised card charges
	VirtualAccounts bool
	Wallet          bool
	Mandates        bool
	Refunds         bool
	Stablecoin      bool
}

// Customer is an org as a provider sees it.
type Customer struct {
	OrgID uuid.UUID
	Name  string
	Email string
	// ProviderID is the provider's customer id, once created.
	ProviderID string
}

// CheckoutRequest starts a hosted payment.
type CheckoutRequest struct {
	Reference   string // ours, unique: the provider returns it on its events
	AmountMinor int64
	Customer    Customer
	Channel     string // card, wallet, stablecoin
	Purpose     string
	Description string
	RedirectURL string
	// Mandate asks for a recurring wallet mandate with this monthly limit.
	MandateLimitMinor int64
}

// CheckoutSession is a hosted page to send the customer to.
type CheckoutSession struct {
	URL         string
	ProviderRef string
}

// ChargeRequest charges a saved card token or a wallet mandate.
type ChargeRequest struct {
	Reference   string
	AmountMinor int64
	Customer    Customer
	Token       string // a card token, or a mandate id
	Description string
}

// Card is a card a payment was made with, for saving.
type Card struct {
	Token    string
	Brand    string
	Last4    string
	ExpMonth int
	ExpYear  int
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
	Reference   string // ours, when the payment started here
	Status      string
	AmountMinor int64
	FeeMinor    int64
	Currency    string
	Channel     string
	// AccountNumber is the virtual account a transfer was paid into.
	AccountNumber string
	Card          *Card
	MandateID     string
	MandateLimit  int64
	// FXQuote is iSpend's naira quote for a stablecoin top-up.
	FXQuote json.RawMessage
	Message string
	At      time.Time
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
	Reference   string
	AmountMinor int64
	Currency    string
	MandateID   string
	Raw         json.RawMessage
}

// RefundRequest refunds part or all of a transaction.
type RefundRequest struct {
	ProviderRef string
	AmountMinor int64
	Reference   string
}

// RefundResult is a refund's state at the provider.
type RefundResult struct {
	ProviderRef string
	Status      string // succeeded (completed), pending, failed
}

// PaymentProvider is one payment provider (V3 §3.4.1). Billing logic only
// ever talks to providers through it.
type PaymentProvider interface {
	Name() string
	Capabilities() Capabilities
	CreateCustomer(ctx context.Context, c Customer) (string, error)
	Checkout(ctx context.Context, r CheckoutRequest) (CheckoutSession, error)
	ChargeSaved(ctx context.Context, r ChargeRequest) (Transaction, error)
	IssueVirtualAccount(ctx context.Context, c Customer) (VirtualAccount, error)
	Verify(ctx context.Context, providerRef string) (Transaction, error)
	// VerifyReference looks a transaction up by our reference.
	VerifyReference(ctx context.Context, reference string) (Transaction, error)
	ParseWebhook(ctx context.Context, r *http.Request) (Event, error)
	Refund(ctx context.Context, r RefundRequest) (RefundResult, error)
	ListTransactions(ctx context.Context, from, to time.Time) ([]Transaction, error)
	RevokeMandate(ctx context.Context, mandateID string) error
}

// Provider errors.
var (
	// ErrUnavailable is a provider outage: retried, and never counted
	// against the customer (V3 §3.4.8).
	ErrUnavailable = errors.New("payment provider unavailable")
	// ErrUnauthenticated is a webhook that fails its signature check.
	ErrUnauthenticated = errors.New("webhook authentication failed")
	// ErrDeclined is a charge the provider refused (a customer failure).
	ErrDeclined = errors.New("payment declined")
	// ErrUnsupported is a capability the provider lacks.
	ErrUnsupported = errors.New("not supported by this provider")
	// ErrIgnoredEvent is a webhook PGDock doesn't act on.
	ErrIgnoredEvent = errors.New("event not handled")
)
