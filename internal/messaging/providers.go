package messaging

import (
	"bytes"
	"context"
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
)

// Channels.
const (
	SMS      = "sms"
	WhatsApp = "whatsapp"
)

// Message is one code to send.
type Message struct {
	Channel string // sms | whatsapp
	To      string // E.164
	// Body is the text of an SMS; Code is the one-time code a WhatsApp
	// authentication template carries.
	Body string
	Code string
}

// Sent is what a provider reports.
type Sent struct {
	ID string
	// CostMinor and Currency are the provider's price when it reports one.
	CostMinor *int64
	Currency  string
	// Provider is the provider that sent it, when a Failover chose.
	Provider string
}

// Provider sends codes.
type Provider interface {
	Name() string
	Supports(channel string) bool
	Send(ctx context.Context, m Message) (Sent, error)
}

// ErrUnsupported is a channel the provider doesn't carry.
var ErrUnsupported = errors.New("the provider doesn't send on this channel")

var defaultClient = &http.Client{Timeout: 15 * time.Second}

func client(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return defaultClient
}

func do(c *http.Client, req *http.Request, what string) ([]byte, error) {
	res, err := client(c).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %d %.300s", what, res.StatusCode, raw)
	}
	return raw, nil
}

// ---- Termii (SMS, Nigeria) ------------------------------------------------------

// Termii is Termii's messaging API: SMS through its DND route, which
// reaches numbers registered as do-not-disturb with transactional messages.
type Termii struct {
	// BaseURL is the account's API base (https://api.ng.termii.com when empty).
	BaseURL  string
	APIKey   string
	SenderID string
	// Channel is "dnd" (default: transactional, reaches DND numbers) or
	// "generic".
	Channel string
	Client  *http.Client
}

// DefaultTermiiURL is Termii's API.
const DefaultTermiiURL = "https://api.ng.termii.com"

// Name implements Provider.
func (Termii) Name() string { return "termii" }

// Supports implements Provider.
func (Termii) Supports(ch string) bool { return ch == SMS }

// Send implements Provider.
func (t Termii) Send(ctx context.Context, m Message) (Sent, error) {
	if m.Channel != SMS {
		return Sent{}, ErrUnsupported
	}
	base := strings.TrimRight(t.BaseURL, "/")
	if base == "" {
		base = DefaultTermiiURL
	}
	ch := t.Channel
	if ch == "" {
		ch = "dnd"
	}
	body, _ := json.Marshal(map[string]string{"to": strings.TrimPrefix(m.To, "+"), "from": t.SenderID, "sms": m.Body,
		"type": "plain", "channel": ch, "api_key": t.APIKey})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sms/send", bytes.NewReader(body))
	if err != nil {
		return Sent{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := do(t.Client, req, "termii")
	if err != nil {
		return Sent{}, err
	}
	var out struct {
		MessageID any    `json:"message_id"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.MessageID == nil {
		return Sent{}, fmt.Errorf("termii: unexpected answer %.300s", raw)
	}
	return Sent{ID: fmt.Sprint(out.MessageID)}, nil
}

// ---- WhatsApp Business Platform (Cloud API) --------------------------------------

// WhatsAppCloud sends codes as WhatsApp authentication-template messages
// (Meta allows business-initiated messages only as approved templates):
// the template has the code as its body's parameter and, for "copy code"
// templates, its button's.
type WhatsAppCloud struct {
	// BaseURL is the Graph API with its version (https://graph.facebook.com/v21.0).
	BaseURL       string
	PhoneNumberID string
	AccessToken   string
	Template      string // the approved authentication template's name
	Language      string // its language code (en default)
	// NoButton leaves out the copy-code button's parameter (templates
	// without one).
	NoButton bool
	Client   *http.Client
}

// DefaultGraphURL is Meta's Graph API.
const DefaultGraphURL = "https://graph.facebook.com/v21.0"

// Name implements Provider.
func (WhatsAppCloud) Name() string { return "whatsapp_cloud" }

// Supports implements Provider.
func (WhatsAppCloud) Supports(ch string) bool { return ch == WhatsApp }

// Send implements Provider.
func (w WhatsAppCloud) Send(ctx context.Context, m Message) (Sent, error) {
	if m.Channel != WhatsApp {
		return Sent{}, ErrUnsupported
	}
	base := strings.TrimRight(w.BaseURL, "/")
	if base == "" {
		base = DefaultGraphURL
	}
	lang := w.Language
	if lang == "" {
		lang = "en"
	}
	components := []map[string]any{{"type": "body", "parameters": []map[string]string{{"type": "text", "text": m.Code}}}}
	if !w.NoButton {
		components = append(components, map[string]any{"type": "button", "sub_type": "url", "index": "0",
			"parameters": []map[string]string{{"type": "text", "text": m.Code}}})
	}
	body, _ := json.Marshal(map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual", "to": strings.TrimPrefix(m.To, "+"), "type": "template",
		"template": map[string]any{"name": w.Template, "language": map[string]string{"code": lang}, "components": components},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+w.PhoneNumberID+"/messages", bytes.NewReader(body))
	if err != nil {
		return Sent{}, err
	}
	req.Header.Set("Authorization", "Bearer "+w.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	raw, err := do(w.Client, req, "whatsapp")
	if err != nil {
		return Sent{}, err
	}
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Messages) == 0 {
		return Sent{}, fmt.Errorf("whatsapp: unexpected answer %.300s", raw)
	}
	return Sent{ID: out.Messages[0].ID}, nil
}

// ---- Twilio (SMS and WhatsApp) ----------------------------------------------------

// Twilio is Twilio's Messages API.
type Twilio struct {
	BaseURL    string // https://api.twilio.com when empty
	AccountSID string
	AuthToken  string
	// From is the sending number (or MessagingServiceSID a messaging
	// service); WhatsAppFrom the WhatsApp sender, if any.
	From                string
	MessagingServiceSID string
	WhatsAppFrom        string
	Client              *http.Client
}

// Name implements Provider.
func (Twilio) Name() string { return "twilio" }

// Supports implements Provider.
func (t Twilio) Supports(ch string) bool {
	return ch == SMS || (ch == WhatsApp && t.WhatsAppFrom != "")
}

// Send implements Provider.
func (t Twilio) Send(ctx context.Context, m Message) (Sent, error) {
	if !t.Supports(m.Channel) {
		return Sent{}, ErrUnsupported
	}
	base := strings.TrimRight(t.BaseURL, "/")
	if base == "" {
		base = "https://api.twilio.com"
	}
	form := url.Values{"Body": {m.Body}}
	if m.Channel == WhatsApp {
		form.Set("To", "whatsapp:"+m.To)
		form.Set("From", "whatsapp:"+t.WhatsAppFrom)
	} else {
		form.Set("To", m.To)
		if t.MessagingServiceSID != "" {
			form.Set("MessagingServiceSid", t.MessagingServiceSID)
		} else {
			form.Set("From", t.From)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/2010-04-01/Accounts/"+url.PathEscape(t.AccountSID)+"/Messages.json",
		strings.NewReader(form.Encode()))
	if err != nil {
		return Sent{}, err
	}
	req.SetBasicAuth(t.AccountSID, t.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw, err := do(t.Client, req, "twilio")
	if err != nil {
		return Sent{}, err
	}
	var out struct {
		SID string `json:"sid"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.SID == "" {
		return Sent{}, fmt.Errorf("twilio: unexpected answer %.300s", raw)
	}
	return Sent{ID: out.SID}, nil
}

// ---- Africa's Talking (SMS) ---------------------------------------------------------

// AfricasTalking is Africa's Talking's SMS API.
type AfricasTalking struct {
	BaseURL  string // https://api.africastalking.com when empty
	Username string
	APIKey   string
	From     string // a sender id or short code (optional)
	Client   *http.Client
}

// Name implements Provider.
func (AfricasTalking) Name() string { return "africastalking" }

// Supports implements Provider.
func (AfricasTalking) Supports(ch string) bool { return ch == SMS }

// Send implements Provider.
func (a AfricasTalking) Send(ctx context.Context, m Message) (Sent, error) {
	if m.Channel != SMS {
		return Sent{}, ErrUnsupported
	}
	base := strings.TrimRight(a.BaseURL, "/")
	if base == "" {
		base = "https://api.africastalking.com"
	}
	form := url.Values{"username": {a.Username}, "to": {m.To}, "message": {m.Body}}
	if a.From != "" {
		form.Set("from", a.From)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/version1/messaging", strings.NewReader(form.Encode()))
	if err != nil {
		return Sent{}, err
	}
	req.Header.Set("apiKey", a.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw, err := do(a.Client, req, "africastalking")
	if err != nil {
		return Sent{}, err
	}
	var out struct {
		SMSMessageData struct {
			Recipients []struct {
				Status    string `json:"status"`
				MessageID string `json:"messageId"`
				Cost      string `json:"cost"`
			} `json:"Recipients"`
		} `json:"SMSMessageData"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.SMSMessageData.Recipients) == 0 {
		return Sent{}, fmt.Errorf("africastalking: unexpected answer %.300s", raw)
	}
	r := out.SMSMessageData.Recipients[0]
	if r.Status != "Success" {
		return Sent{}, fmt.Errorf("africastalking: %s", r.Status)
	}
	s := Sent{ID: r.MessageID}
	// "NGN 2.2000"
	if cur, amt, ok := strings.Cut(r.Cost, " "); ok {
		if f, err := strconv.ParseFloat(amt, 64); err == nil {
			minor := int64(f*100 + 0.5)
			s.CostMinor, s.Currency = &minor, cur
		}
	}
	return s, nil
}

// ---- Failover ----------------------------------------------------------------

// Failover sends through the first of Providers that takes the message
// (V4 §15: multiple SMS providers with fallback). A provider that fails is
// tried last for Cooldown (a minute by default), so an outage costs one
// timeout, not one per message; it is still tried if the others fail.
type Failover struct {
	Providers []Provider
	Cooldown  time.Duration
	// OnFail is told of each provider's failure (an operator alert).
	OnFail func(provider string, err error)

	mu   sync.Mutex
	down map[int]time.Time
	now  func() time.Time
}

// Name implements Provider: the providers in order.
func (f *Failover) Name() string {
	names := make([]string, len(f.Providers))
	for i, p := range f.Providers {
		names[i] = p.Name()
	}
	return strings.Join(names, "+")
}

// Supports implements Provider.
func (f *Failover) Supports(ch string) bool {
	for _, p := range f.Providers {
		if p.Supports(ch) {
			return true
		}
	}
	return false
}

// Send implements Provider. The Sent names the provider that sent it.
func (f *Failover) Send(ctx context.Context, m Message) (Sent, error) {
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	f.mu.Lock()
	var up, cooling []int
	for i, p := range f.Providers {
		if !p.Supports(m.Channel) {
			continue
		}
		if until, ok := f.down[i]; ok && now().Before(until) {
			cooling = append(cooling, i)
		} else {
			up = append(up, i)
		}
	}
	f.mu.Unlock()
	var errs []error
	for _, i := range append(up, cooling...) {
		if ctx.Err() != nil {
			break
		}
		p := f.Providers[i]
		s, err := p.Send(ctx, m)
		f.mu.Lock()
		if err == nil {
			delete(f.down, i)
			f.mu.Unlock()
			s.Provider = p.Name()
			return s, nil
		}
		if f.down == nil {
			f.down = map[int]time.Time{}
		}
		cool := f.Cooldown
		if cool <= 0 {
			cool = time.Minute
		}
		f.down[i] = now().Add(cool)
		f.mu.Unlock()
		if f.OnFail != nil {
			f.OnFail(p.Name(), err)
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return Sent{}, ErrUnsupported
	}
	return Sent{}, errors.Join(errs...)
}

// PlatformProviders are the providers PGDock can hold its own accounts
// with (cmd/server builds them from PGDOCK_TERMII_*, PGDOCK_AFRICASTALKING_*
// and PGDOCK_WHATSAPP_*). Each is a sub-processor in the default DPA
// (legal.SubProcessors); a test fails when one is missing there.
var PlatformProviders = []Provider{Termii{}, AfricasTalking{}, WhatsAppCloud{}}
