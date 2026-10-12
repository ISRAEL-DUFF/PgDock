// Package iswallet is PGDock's wallet provider: each organisation has a
// wallet at iswallet that holds its money, funded by bank transfer into a
// virtual account, and debited for invoices by a transfer to PGDock's
// operating wallet. It also prices a dollar in naira. Amounts are kobo.
//
// The contract is iswallet's OpenAPI document and its answers to PGDock's
// integration request (12 October 2026): a transfer is synchronous and
// final, a debit needs no customer approval (PGDock records the consent),
// there are no holds or locked quotes, and an idempotent call can be looked
// up by its key.
package iswallet

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/israel-duff/pgdock/internal/billing"
)

// Config configures the client.
type Config struct {
	BaseURL string
	APIKey  string
	// WebhookSecret signs webhooks (the subscription's signing secret).
	WebhookSecret string
	// ClientID is PGDock's client id at iswallet, when the key doesn't imply it.
	ClientID string
	Client   *http.Client
	// Now is the clock for webhook replay protection.
	Now func() time.Time
}

// MaxSkew is how far a webhook's timestamp may be from now.
const MaxSkew = 5 * time.Minute

// Client is the iswallet provider.
type Client struct {
	cfg Config

	mu       sync.Mutex
	operator string // PGDock's operating wallet, once known
}

// New returns the client.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Client{cfg: cfg}
}

var _ billing.WalletProvider = (*Client)(nil)

func (c *Client) Name() string { return billing.ProviderISwallet }

func (c *Client) Capabilities() billing.Capabilities {
	return billing.Capabilities{VirtualAccounts: true, Wallet: true}
}

// apiError is iswallet's error envelope.
type apiError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
	// Original is error.original_response, with a 409's existing resource.
	Original json.RawMessage
}

func (e *apiError) Error() string {
	return fmt.Sprintf("iswallet: %s (%d %s, request %s)", e.Message, e.Status, e.Code, e.RequestID)
}

// call sends a request. key is the Idempotency-Key header when set.
func (c *Client) call(ctx context.Context, method, path, key string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := c.cfg.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", billing.ErrUnavailable, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 400 {
		e := &apiError{Status: res.StatusCode, Message: strings.TrimSpace(string(raw))}
		var env struct {
			Error struct {
				Code      string          `json:"code"`
				Message   string          `json:"message"`
				RequestID string          `json:"request_id"`
				Original  json.RawMessage `json:"original_response"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error.Code != "" {
			e.Code, e.Message, e.RequestID, e.Original = env.Error.Code, env.Error.Message, env.Error.RequestID, env.Error.Original
		}
		if e.RequestID == "" {
			e.RequestID = res.Header.Get("X-Request-ID")
		}
		return classify(e)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("iswallet %s %s: %w", method, path, err)
		}
	}
	return nil
}

// classify maps an error response onto what billing acts on.
func classify(e *apiError) error {
	switch {
	case e.Status >= 500 || e.Status == http.StatusTooManyRequests || e.Code == "RATE_LIMITED":
		return fmt.Errorf("%w: %w", billing.ErrUnavailable, e)
	}
	switch e.Code {
	case "INSUFFICIENT_FUNDS":
		return fmt.Errorf("%w: %w", billing.ErrInsufficientFunds, e)
	case "WALLET_INACTIVE":
		return fmt.Errorf("%w: %w", billing.ErrWalletInactive, e)
	case "EXCEEDS_SINGLE_TXN_LIMIT", "EXCEEDS_DAILY_LIMIT", "CURRENCY_MISMATCH":
		return fmt.Errorf("%w: %w", billing.ErrWalletLimit, e)
	case "IDEMPOTENCY_KEY_NOT_FOUND":
		return fmt.Errorf("%w: %w", billing.ErrRequestNotFound, e)
	}
	return e
}

// ---- Wallets ------------------------------------------------------------------

type walletBody struct {
	ID       string `json:"wallet_id"`
	OwnerRef string `json:"owner_ref"`
	Status   string `json:"status"`
	Tier     string `json:"tier"`
}

func (w walletBody) wallet() billing.Wallet {
	return billing.Wallet{ID: w.ID, OwnerRef: w.OwnerRef, Status: w.Status, Tier: w.Tier}
}

// CreateWallet creates a business wallet. A wallet that exists already (the
// owner reference is unique) is returned as it is: that is how a lost
// response is recovered.
func (c *Client) CreateWallet(ctx context.Context, r billing.WalletRequest) (billing.Wallet, error) {
	body := map[string]any{
		"owner_type": "client_end_business", "owner_ref": r.OwnerRef, "currency": "NGN",
		"business_name": r.Name, "business_email": r.Email, "business_phone": r.Phone, "bvn": r.BVN,
	}
	if c.cfg.ClientID != "" {
		body["client_id"] = c.cfg.ClientID
	}
	if r.TIN != "" {
		body["tin"] = r.TIN
	}
	if r.CACNumber != "" {
		body["cac_number"] = r.CACNumber
	}
	if r.Tier != "" {
		body["initial_tier"] = r.Tier
		// The provider wants the contact fields flat and a tier above 1
		// needs an email for the account number to be issued.
		body["email"] = r.Email
		body["phone"] = r.Phone
	}
	var out walletBody
	err := c.call(ctx, http.MethodPost, "/v1/wallets", r.IdempotencyKey, body, &out)
	var e *apiError
	if errors.As(err, &e) && e.Code == "WALLET_ALREADY_EXISTS" && len(e.Original) > 0 {
		var existing walletBody
		if json.Unmarshal(e.Original, &existing) == nil && existing.ID != "" {
			return existing.wallet(), nil
		}
		// Not in the original_response: look it up by owner reference.
		var found struct {
			Wallets []walletBody `json:"wallets"`
		}
		if lerr := c.call(ctx, http.MethodGet, "/v1/wallets?currency=NGN&owner_ref="+url.QueryEscape(r.OwnerRef), "", nil, &found); lerr == nil && len(found.Wallets) > 0 {
			return found.Wallets[0].wallet(), nil
		}
	}
	if err != nil {
		return billing.Wallet{}, err
	}
	return out.wallet(), nil
}

// Balance reads a wallet's naira balance.
func (c *Client) Balance(ctx context.Context, walletID string) (billing.WalletBalance, error) {
	var out struct {
		AsOf    time.Time `json:"as_of"`
		Balance struct {
			Total, Available, Pending int64
		} `json:"balance"`
		Balances []struct {
			Currency                  string `json:"currency"`
			Total, Available, Pending int64
		} `json:"balances"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/wallets/"+url.PathEscape(walletID)+"/balance", "", nil, &out); err != nil {
		return billing.WalletBalance{}, err
	}
	// Read balances[]: a currency with no balance is absent, not zero.
	b := billing.WalletBalance{AsOf: out.AsOf}
	for _, x := range out.Balances {
		if x.Currency == "NGN" {
			b.Total, b.Available, b.Pending = x.Total, x.Available, x.Pending
		}
	}
	return b, nil
}

// OperatingWallet is PGDock's own wallet at iswallet.
func (c *Client) OperatingWallet(ctx context.Context) (string, error) {
	c.mu.Lock()
	op := c.operator
	c.mu.Unlock()
	if op != "" {
		return op, nil
	}
	var out struct {
		ID string `json:"operating_wallet_id"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/platform/account", "", nil, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("iswallet: no operating wallet for this client")
	}
	c.mu.Lock()
	c.operator = out.ID
	c.mu.Unlock()
	return out.ID, nil
}

// Transfer moves money between two wallets. It completes synchronously.
func (c *Client) Transfer(ctx context.Context, r billing.TransferRequest) (billing.TransferResult, error) {
	narration := r.Narration
	if len(narration) > 255 {
		narration = narration[:255]
	}
	var out struct {
		TransferID  string    `json:"transfer_id"`
		TxnID       string    `json:"txn_id"`
		Status      string    `json:"status"`
		CompletedAt time.Time `json:"completed_at"`
	}
	err := c.call(ctx, http.MethodPost, "/v1/transfers", r.IdempotencyKey, map[string]any{
		"from_wallet_id": r.From, "to_wallet_id": r.To, "amount": r.AmountMinor, "currency": "NGN", "narration": narration,
	}, &out)
	if err != nil {
		return billing.TransferResult{}, err
	}
	if out.Status != "" && out.Status != "completed" {
		return billing.TransferResult{}, fmt.Errorf("iswallet: transfer %s is %s", out.TransferID, out.Status)
	}
	return billing.TransferResult{TransferID: out.TransferID, TxnID: out.TxnID, At: out.CompletedAt}, nil
}

// Request looks up what was recorded under an idempotency key. A key with
// nothing under it is billing.ErrRequestNotFound: nothing was posted, so the
// request is safe to send again.
func (c *Client) Request(ctx context.Context, key string) (billing.RequestState, error) {
	var out struct {
		Kind       string `json:"kind"`
		ResourceID string `json:"resource_id"`
		Status     string `json:"status"`
		Terminal   bool   `json:"terminal"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/requests/"+url.PathEscape(key), "", nil, &out); err != nil {
		return billing.RequestState{}, err
	}
	return billing.RequestState{Kind: out.Kind, ResourceID: out.ResourceID, Status: out.Status, Terminal: out.Terminal}, nil
}

// Rate prices from→to for amountMinor of from. The answer is never an
// error because a feed is stale: check FetchedAt.
func (c *Client) Rate(ctx context.Context, from, to string, amountMinor int64) (billing.FXRate, error) {
	var out struct {
		Rate struct {
			From      string    `json:"from"`
			To        string    `json:"to"`
			Market    string    `json:"market_rate"`
			Effective string    `json:"effective_rate"`
			SpreadBPS int       `json:"spread_bps"`
			FetchedAt time.Time `json:"fetched_at"`
			ExpiresAt time.Time `json:"expires_at"`
			Sources   []string  `json:"sources"`
			Derived   bool      `json:"derived"`
		} `json:"rate"`
	}
	q := url.Values{"from": {from}, "to": {to}, "amount": {strconv.FormatInt(amountMinor, 10)}}
	if err := c.call(ctx, http.MethodGet, "/v1/rates?"+q.Encode(), "", nil, &out); err != nil {
		return billing.FXRate{}, err
	}
	market, err := billing.ParseDec(out.Rate.Market)
	if err != nil {
		return billing.FXRate{}, fmt.Errorf("iswallet: market rate: %w", err)
	}
	eff, err := billing.ParseDec(out.Rate.Effective)
	if err != nil {
		return billing.FXRate{}, fmt.Errorf("iswallet: effective rate: %w", err)
	}
	return billing.FXRate{From: out.Rate.From, To: out.Rate.To, Market: market, Effective: eff, SpreadBPS: out.Rate.SpreadBPS,
		FetchedAt: out.Rate.FetchedAt, ExpiresAt: out.Rate.ExpiresAt, Sources: out.Rate.Sources, Derived: out.Rate.Derived}, nil
}

// ---- Payment provider -----------------------------------------------------------

// CreateCustomer isn't how a wallet is made: it needs a director's BVN, so
// billing.EnsureWallet creates it and stores its id first.
func (c *Client) CreateCustomer(context.Context, billing.Customer) (string, error) {
	return "", billing.ErrWalletSetup
}

func (c *Client) Checkout(context.Context, billing.CheckoutRequest) (billing.CheckoutSession, error) {
	return billing.CheckoutSession{}, billing.ErrUnsupported
}

func (c *Client) ChargeSaved(context.Context, billing.ChargeRequest) (billing.Transaction, error) {
	return billing.Transaction{}, billing.ErrUnsupported
}

func (c *Client) Refund(context.Context, billing.RefundRequest) (billing.RefundResult, error) {
	return billing.RefundResult{}, billing.ErrUnsupported
}

func (c *Client) RevokeMandate(context.Context, string) error { return billing.ErrUnsupported }

func (c *Client) VerifyReference(context.Context, string) (billing.Transaction, error) {
	return billing.Transaction{}, billing.ErrUnsupported
}

// IssueVirtualAccount issues (or returns) the wallet's permanent bank
// account. Its ProviderRef is the wallet id, which is how a deposit is
// attributed to the organisation.
func (c *Client) IssueVirtualAccount(ctx context.Context, cust billing.Customer) (billing.VirtualAccount, error) {
	if cust.ProviderID == "" {
		return billing.VirtualAccount{}, billing.ErrWalletSetup
	}
	var out struct {
		AccountNumber string `json:"account_number"`
		AccountName   string `json:"account_name"`
		BankName      string `json:"bank_name"`
	}
	err := c.call(ctx, http.MethodPost, "/v1/wallets/"+url.PathEscape(cust.ProviderID)+"/virtual-account", "",
		map[string]any{"is_permanent": true, "client_reference": cust.OrgID.String()}, &out)
	if err != nil {
		return billing.VirtualAccount{}, err
	}
	return billing.VirtualAccount{AccountNumber: out.AccountNumber, BankName: out.BankName, AccountName: out.AccountName, ProviderRef: cust.ProviderID}, nil
}

// txn is a transaction as iswallet lists it.
type txn struct {
	TxnID          string    `json:"txn_id"`
	Type           string    `json:"type"`
	Direction      string    `json:"direction"`
	Status         string    `json:"status"`
	Amount         int64     `json:"amount"`
	AmountCredited int64     `json:"amount_credited"`
	Currency       string    `json:"currency"`
	OccurredAt     time.Time `json:"occurred_at"`
	ConfirmedAt    time.Time `json:"confirmed_at"`
}

// status maps iswallet's transaction status to billing's.
func status(s string) string {
	switch strings.ToLower(s) {
	case "", "posted", "completed", "confirmed", "success", "succeeded", "settled":
		return billing.TxSucceeded
	case "pending", "processing", "initiated":
		return billing.TxPending
	}
	return billing.TxFailed
}

// Verify reads a deposit back: providerRef is <wallet id>:<transaction id>.
func (c *Client) Verify(ctx context.Context, providerRef string) (billing.Transaction, error) {
	wid, id, ok := strings.Cut(providerRef, billing.RefSep)
	if !ok || wid == "" || id == "" {
		return billing.Transaction{}, fmt.Errorf("iswallet: %q is not a <wallet>:<transaction> reference", providerRef)
	}
	var t txn
	if err := c.call(ctx, http.MethodGet, "/v1/wallets/"+url.PathEscape(wid)+"/transactions/"+url.PathEscape(id), "", nil, &t); err != nil {
		return billing.Transaction{}, err
	}
	if t.Direction != "credit" || t.Type != "inflow" {
		return billing.Transaction{}, fmt.Errorf("iswallet: transaction %s is not a deposit (%s %s)", id, t.Direction, t.Type)
	}
	amount := t.AmountCredited
	if amount <= 0 {
		amount = t.Amount
	}
	at := t.ConfirmedAt
	if at.IsZero() {
		at = t.OccurredAt
	}
	// AccountNumber carries the wallet id: the virtual account's reference.
	return billing.Transaction{ProviderRef: providerRef, Status: status(t.Status), AmountMinor: amount, Currency: t.Currency,
		Channel: billing.ChannelTransfer, AccountNumber: wid, At: at}, nil
}

// ListTransactions lists the deposits and the debits (transfers to the
// operating wallet) in [from, to), for re-querying and reconciliation.
func (c *Client) ListTransactions(ctx context.Context, from, to time.Time) ([]billing.Transaction, error) {
	var out []billing.Transaction
	for start := from; start.Before(to); start = start.Add(24 * time.Hour) {
		end := start.Add(24 * time.Hour)
		if end.After(to) {
			end = to
		}
		var res struct {
			Transactions []struct {
				WalletID string `json:"wallet_id"`
				txn
			} `json:"transactions"`
		}
		q := url.Values{"from": {start.UTC().Format(time.RFC3339)}, "to": {end.UTC().Format(time.RFC3339)}, "format": {"json"}}
		if err := c.call(ctx, http.MethodGet, "/v1/ledger/export?"+q.Encode(), "", nil, &res); err != nil {
			return nil, err
		}
		for _, t := range res.Transactions {
			if t.Currency != "" && t.Currency != "NGN" {
				continue
			}
			switch {
			case t.Type == "inflow" && t.Direction == "credit":
				out = append(out, billing.Transaction{ProviderRef: t.WalletID + billing.RefSep + t.TxnID, Status: billing.TxSucceeded,
					AmountMinor: t.Amount, Currency: "NGN", Channel: billing.ChannelTransfer, AccountNumber: t.WalletID, At: t.OccurredAt})
			case t.Type == "transfer" && t.Direction == "debit":
				out = append(out, billing.Transaction{ProviderRef: t.WalletID + billing.RefSep + t.TxnID, Status: billing.TxSucceeded,
					AmountMinor: t.Amount, Currency: "NGN", Channel: billing.ChannelWallet, AccountNumber: t.WalletID, At: t.OccurredAt})
			}
		}
	}
	return out, nil
}

// ---- Webhooks -----------------------------------------------------------------------

// Sign is the signature of a webhook: HMAC-SHA256 over "<timestamp>.<body>".
func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.", ts)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// ParseWebhook authenticates a webhook and normalises it. Only deposits
// matter to PGDock (our own debits are booked as they are made); any other
// event is ignored.
func (c *Client) ParseWebhook(ctx context.Context, r *http.Request) (billing.Event, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return billing.Event{}, err
	}
	ts, err := strconv.ParseInt(r.Header.Get("X-iSpend-Timestamp"), 10, 64)
	if err != nil {
		return billing.Event{}, billing.ErrUnauthenticated
	}
	if d := c.cfg.Now().Sub(time.Unix(ts, 0)); d > MaxSkew || d < -MaxSkew {
		return billing.Event{}, billing.ErrUnauthenticated
	}
	want := Sign(c.cfg.WebhookSecret, ts, body)
	if !hmac.Equal([]byte(r.Header.Get("X-iSpend-Signature")), []byte(want)) {
		return billing.Event{}, billing.ErrUnauthenticated
	}
	var env struct {
		ID        string `json:"id"`
		EventType string `json:"event_type"`
		WalletID  string `json:"wallet_id"`
		Data      struct {
			WalletID       string `json:"wallet_id"`
			Amount         int64  `json:"amount"`
			AmountCredited int64  `json:"amount_credited"`
			Currency       string `json:"currency"`
			TxnID          string `json:"txn_id"`
			SourceType     string `json:"source_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return billing.Event{}, fmt.Errorf("iswallet webhook: %w", err)
	}
	if env.EventType != "wallet.credit.posted" || (env.Data.SourceType != "pull_inflow" && env.Data.SourceType != "va_deposit") {
		return billing.Event{}, billing.ErrIgnoredEvent
	}
	wid := env.Data.WalletID
	if wid == "" {
		wid = env.WalletID
	}
	amount := env.Data.AmountCredited
	if amount <= 0 {
		amount = env.Data.Amount
	}
	// Dedupe on the transaction, not on the delivery: an event can be sent twice.
	return billing.Event{ID: env.Data.TxnID, Kind: billing.EventTransferReceived, ProviderRef: wid + billing.RefSep + env.Data.TxnID,
		AmountMinor: amount, Currency: env.Data.Currency, Raw: body}, nil
}
