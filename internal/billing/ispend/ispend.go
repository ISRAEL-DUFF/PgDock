// Package ispend is PGDock's iSpend provider (V3 §3.4.3–§3.4.6): permanent
// virtual accounts, Pay with iSpend, wallet mandates, stablecoin top-ups,
// verification, refunds and transaction listing, against the merchant API
// PGDock defines for iSpend (docs/ispend-contract.md). Amounts are kobo.
package ispend

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/billing"
)

// Config configures the client.
type Config struct {
	BaseURL       string
	APIKey        string
	WebhookSecret string
	Client        *http.Client
	// Now is the clock for webhook replay protection.
	Now func() time.Time
}

// MaxSkew is how old a webhook's timestamp may be.
const MaxSkew = 5 * time.Minute

// Provider is the iSpend client.
type Provider struct{ cfg Config }

// New returns the client.
func New(cfg Config) *Provider {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string { return billing.ProviderISpend }

func (p *Provider) Capabilities() billing.Capabilities {
	return billing.Capabilities{VirtualAccounts: true, Wallet: true, Mandates: true, Refunds: true, Stablecoin: true}
}

func (p *Provider) call(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.cfg.BaseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.cfg.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", billing.ErrUnavailable, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	switch {
	case res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: ispend answered %d", billing.ErrUnavailable, res.StatusCode)
	case res.StatusCode == http.StatusPaymentRequired:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("%w: %s", billing.ErrDeclined, e.Message)
	case res.StatusCode >= 400:
		return fmt.Errorf("ispend %s %s: %d %.200s", method, path, res.StatusCode, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// CreateCustomer creates the org's merchant customer.
func (p *Provider) CreateCustomer(ctx context.Context, c billing.Customer) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := p.call(ctx, http.MethodPost, "/merchant/customers", map[string]string{"reference": c.OrgID.String(), "name": c.Name, "email": c.Email}, &out)
	return out.ID, err
}

// Checkout opens a hosted approval page: a wallet payment, a mandate, or
// a stablecoin top-up.
func (p *Provider) Checkout(ctx context.Context, r billing.CheckoutRequest) (billing.CheckoutSession, error) {
	kind := "payment"
	switch r.Channel {
	case billing.ChannelWallet:
	case billing.ChannelStablecoin:
		kind = "stablecoin"
	default:
		return billing.CheckoutSession{}, billing.ErrUnsupported
	}
	body := map[string]any{
		"reference": r.Reference, "amount_minor": r.AmountMinor, "currency": "NGN", "kind": kind, "purpose": r.Purpose,
		"description": r.Description, "redirect_url": r.RedirectURL, "customer_id": r.Customer.ProviderID,
	}
	if r.MandateLimitMinor > 0 {
		body["mandate"] = map[string]int64{"monthly_limit_minor": r.MandateLimitMinor}
	}
	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := p.call(ctx, http.MethodPost, "/merchant/checkout-sessions", body, &out); err != nil {
		return billing.CheckoutSession{}, err
	}
	return billing.CheckoutSession{URL: out.URL, ProviderRef: out.ID}, nil
}

// Txn is iSpend's transaction.
type Txn struct {
	ID                   string          `json:"id"`
	Reference            string          `json:"reference"`
	Status               string          `json:"status"` // succeeded, failed, pending
	AmountMinor          int64           `json:"amount_minor"`
	FeeMinor             int64           `json:"fee_minor"`
	Currency             string          `json:"currency"`
	Channel              string          `json:"channel"` // wallet, transfer, mandate, stablecoin
	VirtualAccountNumber string          `json:"virtual_account_number,omitempty"`
	Mandate              *Mandate        `json:"mandate,omitempty"`
	FXQuote              json.RawMessage `json:"fx_quote,omitempty"`
	Message              string          `json:"message,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
}

// Mandate is a recurring wallet mandate.
type Mandate struct {
	ID                string `json:"id"`
	MonthlyLimitMinor int64  `json:"monthly_limit_minor"`
}

func (t Txn) normalise() billing.Transaction {
	out := billing.Transaction{
		ProviderRef: t.ID, Reference: t.Reference, Status: t.Status, AmountMinor: t.AmountMinor, FeeMinor: t.FeeMinor,
		Currency: t.Currency, AccountNumber: t.VirtualAccountNumber, FXQuote: t.FXQuote, Message: t.Message, At: t.CreatedAt,
	}
	switch t.Status {
	case "succeeded":
		out.Status = billing.TxSucceeded
	case "failed":
		out.Status = billing.TxFailed
	default:
		out.Status = billing.TxPending
	}
	switch t.Channel {
	case "transfer":
		out.Channel = billing.ChannelTransfer
	case "mandate":
		out.Channel = billing.ChannelMandate
	case "stablecoin":
		out.Channel = billing.ChannelStablecoin
	default:
		out.Channel = billing.ChannelWallet
	}
	if t.Mandate != nil {
		out.MandateID, out.MandateLimit = t.Mandate.ID, t.Mandate.MonthlyLimitMinor
	}
	return out
}

// ChargeSaved charges a wallet mandate (idempotent by reference).
func (p *Provider) ChargeSaved(ctx context.Context, r billing.ChargeRequest) (billing.Transaction, error) {
	var t Txn
	err := p.call(ctx, http.MethodPost, "/merchant/mandates/"+url.PathEscape(r.Token)+"/charges",
		map[string]any{"reference": r.Reference, "amount_minor": r.AmountMinor, "description": r.Description}, &t)
	if err != nil {
		return billing.Transaction{Status: billing.TxFailed, Message: err.Error()}, err
	}
	return t.normalise(), nil
}

// IssueVirtualAccount issues the customer's permanent virtual account.
func (p *Provider) IssueVirtualAccount(ctx context.Context, c billing.Customer) (billing.VirtualAccount, error) {
	var out struct {
		ID            string `json:"id"`
		AccountNumber string `json:"account_number"`
		BankName      string `json:"bank_name"`
		AccountName   string `json:"account_name"`
	}
	if err := p.call(ctx, http.MethodPost, "/merchant/customers/"+url.PathEscape(c.ProviderID)+"/virtual-accounts", map[string]string{}, &out); err != nil {
		return billing.VirtualAccount{}, err
	}
	return billing.VirtualAccount{AccountNumber: out.AccountNumber, BankName: out.BankName, AccountName: out.AccountName, ProviderRef: out.ID}, nil
}

// Verify looks a transaction up by iSpend's id or our reference.
func (p *Provider) Verify(ctx context.Context, ref string) (billing.Transaction, error) {
	var t Txn
	if err := p.call(ctx, http.MethodGet, "/merchant/transactions/"+url.PathEscape(ref), nil, &t); err != nil {
		return billing.Transaction{}, err
	}
	return t.normalise(), nil
}

// VerifyReference is Verify: the endpoint takes either.
func (p *Provider) VerifyReference(ctx context.Context, ref string) (billing.Transaction, error) {
	return p.Verify(ctx, ref)
}

// Sign is the webhook signature: hex HMAC-SHA256 of "timestamp.body".
func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.", ts)
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// ParseWebhook checks the signature and timestamp (replay protection) and
// normalises the event.
func (p *Provider) ParseWebhook(_ context.Context, r *http.Request) (billing.Event, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return billing.Event{}, err
	}
	ts, err := strconv.ParseInt(r.Header.Get("X-ISpend-Timestamp"), 10, 64)
	if err != nil || p.cfg.WebhookSecret == "" {
		return billing.Event{}, billing.ErrUnauthenticated
	}
	if d := p.cfg.Now().Sub(time.Unix(ts, 0)); d > MaxSkew || d < -MaxSkew {
		return billing.Event{}, fmt.Errorf("%w: timestamp outside %s", billing.ErrUnauthenticated, MaxSkew)
	}
	want := Sign(p.cfg.WebhookSecret, ts, raw)
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-ISpend-Signature"))) {
		return billing.Event{}, billing.ErrUnauthenticated
	}
	var body struct {
		ID   string          `json:"id"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.ID == "" {
		return billing.Event{}, fmt.Errorf("ispend webhook: bad body")
	}
	ev := billing.Event{ID: body.ID, Kind: body.Type, Raw: raw}
	switch body.Type {
	case billing.EventPaymentSucceeded, billing.EventPaymentFailed, billing.EventTransferReceived:
		var t Txn
		if err := json.Unmarshal(body.Data, &t); err != nil {
			return billing.Event{}, err
		}
		ev.ProviderRef, ev.Reference, ev.AmountMinor, ev.Currency = t.ID, t.Reference, t.AmountMinor, t.Currency
	case billing.EventRefundCompleted:
		var d struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(body.Data, &d)
		ev.ProviderRef = d.ID
	case billing.EventMandateRevoked:
		var d Mandate
		_ = json.Unmarshal(body.Data, &d)
		ev.MandateID = d.ID
	default:
		return billing.Event{}, billing.ErrIgnoredEvent
	}
	return ev, nil
}

// Refund refunds part or all of a transaction.
func (p *Provider) Refund(ctx context.Context, r billing.RefundRequest) (billing.RefundResult, error) {
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := p.call(ctx, http.MethodPost, "/merchant/refunds", map[string]any{"transaction_id": r.ProviderRef, "amount_minor": r.AmountMinor, "reference": r.Reference}, &out); err != nil {
		return billing.RefundResult{}, err
	}
	res := billing.RefundResult{ProviderRef: out.ID, Status: billing.TxPending}
	switch out.Status {
	case "completed":
		res.Status = billing.TxSucceeded
	case "failed":
		res.Status = billing.TxFailed
	}
	return res, nil
}

// ListTransactions lists transactions created in [from, to).
func (p *Provider) ListTransactions(ctx context.Context, from, to time.Time) ([]billing.Transaction, error) {
	var out struct {
		Items []Txn `json:"items"`
	}
	q := url.Values{"from": {from.UTC().Format(time.RFC3339)}, "to": {to.UTC().Format(time.RFC3339)}}
	if err := p.call(ctx, http.MethodGet, "/merchant/transactions?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	txs := make([]billing.Transaction, 0, len(out.Items))
	for _, t := range out.Items {
		txs = append(txs, t.normalise())
	}
	return txs, nil
}

// RevokeMandate revokes a wallet mandate.
func (p *Provider) RevokeMandate(ctx context.Context, id string) error {
	return p.call(ctx, http.MethodDelete, "/merchant/mandates/"+url.PathEscape(id), nil, nil)
}
