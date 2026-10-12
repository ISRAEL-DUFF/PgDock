// Package flutterwave is PGDock's Flutterwave provider (V3 §3.4.2): hosted
// card checkout, tokenised charges, virtual accounts as the transfer
// fallback, verification, refunds and transaction listing, against the v3
// API. Amounts at Flutterwave are naira with decimals; PGDock's are kobo.
package flutterwave

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
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
	BaseURL   string // https://api.flutterwave.com; a sandbox or fake in tests
	SecretKey string
	// WebhookHash is the secret hash set on the dashboard, sent back in the
	// verif-hash header of each webhook.
	WebhookHash string
	// BVN is required by Flutterwave for permanent virtual accounts; the
	// company's, for accounts issued to customers.
	BVN    string
	Client *http.Client
}

// Provider is the Flutterwave client.
type Provider struct{ cfg Config }

// New returns the client.
func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.flutterwave.com"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string { return billing.ProviderFlutterwave }

func (p *Provider) Capabilities() billing.Capabilities {
	return billing.Capabilities{Cards: true, SavedCharges: true, VirtualAccounts: true, Refunds: true}
}

// naira is kobo as Flutterwave's decimal naira.
func naira(kobo int64) json.Number {
	return json.Number(fmt.Sprintf("%d.%02d", kobo/100, kobo%100))
}

// kobo is Flutterwave's naira as kobo, exactly.
func kobo(n json.Number) int64 {
	if n == "" {
		return 0
	}
	d, err := billing.ParseDec(string(n))
	if err != nil {
		return 0
	}
	return d.Mul(billing.DecInt(100)).Round()
}

type envelope struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Meta    json.RawMessage `json:"meta"`
}

// call does one API request. Network errors and 5xx are ErrUnavailable.
func (p *Provider) call(ctx context.Context, method, path string, body any) (envelope, int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return envelope{}, 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.cfg.BaseURL+path, r)
	if err != nil {
		return envelope{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.cfg.Client.Do(req)
	if err != nil {
		return envelope{}, 0, fmt.Errorf("%w: %w", billing.ErrUnavailable, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
		return envelope{}, res.StatusCode, fmt.Errorf("%w: flutterwave answered %d", billing.ErrUnavailable, res.StatusCode)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return envelope{}, res.StatusCode, fmt.Errorf("flutterwave %s %s: %d %.200s", method, path, res.StatusCode, raw)
	}
	if res.StatusCode >= 400 || env.Status != "success" {
		return env, res.StatusCode, fmt.Errorf("flutterwave %s %s: %s", method, path, env.Message)
	}
	return env, res.StatusCode, nil
}

// CreateCustomer: Flutterwave needs no customer object.
func (p *Provider) CreateCustomer(context.Context, billing.Customer) (string, error) { return "", nil }

// Checkout creates a hosted payment page (Flutterwave Standard).
func (p *Provider) Checkout(ctx context.Context, r billing.CheckoutRequest) (billing.CheckoutSession, error) {
	if r.Channel != billing.ChannelCard {
		return billing.CheckoutSession{}, billing.ErrUnsupported
	}
	env, _, err := p.call(ctx, http.MethodPost, "/v3/payments", map[string]any{
		"tx_ref": r.Reference, "amount": naira(r.AmountMinor), "currency": "NGN", "redirect_url": r.RedirectURL,
		"payment_options": "card",
		"customer":        map[string]string{"email": r.Customer.Email, "name": r.Customer.Name},
		"customizations":  map[string]string{"title": "PGDock", "description": r.Description},
		"meta":            map[string]string{"org_id": r.Customer.OrgID.String(), "purpose": r.Purpose},
	})
	if err != nil {
		return billing.CheckoutSession{}, err
	}
	var d struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil || d.Link == "" {
		return billing.CheckoutSession{}, fmt.Errorf("flutterwave checkout: no link")
	}
	return billing.CheckoutSession{URL: d.Link}, nil
}

// tx is Flutterwave's transaction.
type tx struct {
	ID            json.Number `json:"id"`
	TxRef         string      `json:"tx_ref"`
	FlwRef        string      `json:"flw_ref"`
	Amount        json.Number `json:"amount"`
	ChargedAmount json.Number `json:"charged_amount"`
	AppFee        json.Number `json:"app_fee"`
	Currency      string      `json:"currency"`
	Status        string      `json:"status"`
	PaymentType   string      `json:"payment_type"`
	CreatedAt     time.Time   `json:"created_at"`
	Processor     string      `json:"processor_response"`
	Card          *struct {
		Last4  string `json:"last_4digits"`
		Issuer string `json:"issuer"`
		Type   string `json:"type"`
		Token  string `json:"token"`
		Expiry string `json:"expiry"` // "09/32"
	} `json:"card"`
}

func (t tx) normalise() billing.Transaction {
	out := billing.Transaction{
		ProviderRef: t.ID.String(), Reference: t.TxRef, AmountMinor: kobo(t.Amount), FeeMinor: kobo(t.AppFee),
		Currency: t.Currency, At: t.CreatedAt, Message: t.Processor, Channel: billing.ChannelCard,
	}
	switch strings.ToLower(t.Status) {
	case "successful":
		out.Status = billing.TxSucceeded
	case "failed", "cancelled", "error":
		out.Status = billing.TxFailed
	default:
		out.Status = billing.TxPending
	}
	switch t.PaymentType {
	case "bank_transfer", "account":
		// A transfer into a virtual account carries the account's tx_ref.
		out.Channel, out.AccountNumber = billing.ChannelTransfer, t.TxRef
	}
	if t.Card != nil && t.Card.Token != "" {
		c := &billing.Card{Token: t.Card.Token, Brand: t.Card.Type, Last4: t.Card.Last4}
		if m, y, ok := strings.Cut(t.Card.Expiry, "/"); ok {
			c.ExpMonth, _ = strconv.Atoi(m)
			c.ExpYear, _ = strconv.Atoi(y)
			if c.ExpYear < 100 {
				c.ExpYear += 2000
			}
		}
		out.Card = c
	}
	if out.FeeMinor > out.AmountMinor {
		out.FeeMinor = 0
	}
	return out
}

// ChargeSaved charges a card token (a tokenised charge).
func (p *Provider) ChargeSaved(ctx context.Context, r billing.ChargeRequest) (billing.Transaction, error) {
	env, code, err := p.call(ctx, http.MethodPost, "/v3/tokenized-charges", map[string]any{
		"token": r.Token, "currency": "NGN", "country": "NG", "amount": naira(r.AmountMinor),
		"email": r.Customer.Email, "tx_ref": r.Reference, "narration": r.Description,
	})
	if errors.Is(err, billing.ErrUnavailable) {
		return billing.Transaction{}, err
	}
	var t tx
	_ = json.Unmarshal(env.Data, &t)
	if err != nil {
		if code >= 400 && code < 500 {
			return billing.Transaction{Status: billing.TxFailed, Message: env.Message}, fmt.Errorf("%w: %s", billing.ErrDeclined, env.Message)
		}
		return billing.Transaction{}, err
	}
	return t.normalise(), nil
}

// IssueVirtualAccount issues a permanent virtual account.
func (p *Provider) IssueVirtualAccount(ctx context.Context, c billing.Customer) (billing.VirtualAccount, error) {
	ref := "pgdva-" + c.OrgID.String()
	first, last, _ := strings.Cut(c.Name, " ")
	if last == "" {
		last = "PGDock"
	}
	env, _, err := p.call(ctx, http.MethodPost, "/v3/virtual-account-numbers", map[string]any{
		"email": c.Email, "is_permanent": true, "bvn": p.cfg.BVN, "tx_ref": ref, "narration": c.Name,
		"firstname": first, "lastname": last,
	})
	if err != nil {
		return billing.VirtualAccount{}, err
	}
	var d struct {
		AccountNumber string `json:"account_number"`
		BankName      string `json:"bank_name"`
		OrderRef      string `json:"order_ref"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil || d.AccountNumber == "" {
		return billing.VirtualAccount{}, fmt.Errorf("flutterwave virtual account: no account number")
	}
	return billing.VirtualAccount{AccountNumber: d.AccountNumber, BankName: d.BankName, AccountName: c.Name, ProviderRef: ref}, nil
}

// Verify looks a transaction up by its Flutterwave id.
func (p *Provider) Verify(ctx context.Context, id string) (billing.Transaction, error) {
	env, _, err := p.call(ctx, http.MethodGet, "/v3/transactions/"+url.PathEscape(id)+"/verify", nil)
	if err != nil {
		return billing.Transaction{}, err
	}
	var t tx
	if err := json.Unmarshal(env.Data, &t); err != nil {
		return billing.Transaction{}, err
	}
	return t.normalise(), nil
}

// VerifyReference looks a transaction up by our tx_ref.
func (p *Provider) VerifyReference(ctx context.Context, ref string) (billing.Transaction, error) {
	env, _, err := p.call(ctx, http.MethodGet, "/v3/transactions/verify_by_reference?tx_ref="+url.QueryEscape(ref), nil)
	if err != nil {
		return billing.Transaction{}, err
	}
	var t tx
	if err := json.Unmarshal(env.Data, &t); err != nil {
		return billing.Transaction{}, err
	}
	return t.normalise(), nil
}

// ParseWebhook authenticates a webhook by its verif-hash header and
// normalises it. Its body is only a hint: the transaction is re-verified.
func (p *Provider) ParseWebhook(_ context.Context, r *http.Request) (billing.Event, error) {
	if p.cfg.WebhookHash == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("verif-hash")), []byte(p.cfg.WebhookHash)) != 1 {
		return billing.Event{}, billing.ErrUnauthenticated
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return billing.Event{}, err
	}
	var body struct {
		Event string `json:"event"`
		Data  tx     `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return billing.Event{}, fmt.Errorf("flutterwave webhook: %w", err)
	}
	if body.Event != "charge.completed" || body.Data.ID == "" {
		return billing.Event{}, billing.ErrIgnoredEvent
	}
	t := body.Data.normalise()
	ev := billing.Event{
		ID: body.Event + ":" + t.ProviderRef + ":" + strings.ToLower(body.Data.Status), ProviderRef: t.ProviderRef, Reference: t.Reference,
		AmountMinor: t.AmountMinor, Currency: t.Currency, Raw: raw,
	}
	switch {
	case t.Status == billing.TxSucceeded && t.Channel == billing.ChannelTransfer:
		ev.Kind = billing.EventTransferReceived
	case t.Status == billing.TxSucceeded:
		ev.Kind = billing.EventPaymentSucceeded
	case t.Status == billing.TxFailed:
		ev.Kind = billing.EventPaymentFailed
	default:
		return billing.Event{}, billing.ErrIgnoredEvent
	}
	return ev, nil
}

// Refund refunds part or all of a transaction.
func (p *Provider) Refund(ctx context.Context, r billing.RefundRequest) (billing.RefundResult, error) {
	env, _, err := p.call(ctx, http.MethodPost, "/v3/transactions/"+url.PathEscape(r.ProviderRef)+"/refund", map[string]any{"amount": naira(r.AmountMinor)})
	if err != nil {
		return billing.RefundResult{}, err
	}
	var d struct {
		ID     json.Number `json:"id"`
		Status string      `json:"status"`
	}
	_ = json.Unmarshal(env.Data, &d)
	out := billing.RefundResult{ProviderRef: "refund:" + d.ID.String(), Status: billing.TxPending}
	switch strings.ToLower(d.Status) {
	case "completed", "successful":
		out.Status = billing.TxSucceeded
	case "failed":
		out.Status = billing.TxFailed
	}
	return out, nil
}

// ListTransactions lists transactions created in [from, to).
func (p *Provider) ListTransactions(ctx context.Context, from, to time.Time) ([]billing.Transaction, error) {
	var out []billing.Transaction
	for page := 1; page <= 200; page++ {
		q := url.Values{"from": {from.UTC().Format("2006-01-02")}, "to": {to.UTC().Format("2006-01-02")}, "page": {strconv.Itoa(page)}}
		env, _, err := p.call(ctx, http.MethodGet, "/v3/transactions?"+q.Encode(), nil)
		if err != nil {
			return out, err
		}
		var items []tx
		if err := json.Unmarshal(env.Data, &items); err != nil {
			return out, err
		}
		for _, t := range items {
			n := t.normalise()
			if !n.At.Before(from) && n.At.Before(to) {
				out = append(out, n)
			}
		}
		var meta struct {
			PageInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"page_info"`
		}
		_ = json.Unmarshal(env.Meta, &meta)
		if page >= meta.PageInfo.TotalPages {
			break
		}
	}
	return out, nil
}

// RevokeMandate: Flutterwave has no wallet mandates.
func (p *Provider) RevokeMandate(context.Context, string) error { return billing.ErrUnsupported }
