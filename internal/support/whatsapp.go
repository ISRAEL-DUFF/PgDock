package support

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
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// CloudAPI is the WhatsApp Business Platform's Cloud API (Meta's Graph
// API): PGDock's business number sends replies, and its webhook delivers
// customers' messages.
type CloudAPI struct {
	// BaseURL is the Graph API, with its version
	// (https://graph.facebook.com/v21.0 when empty).
	BaseURL       string
	PhoneNumberID string
	AccessToken   string
	// AppSecret signs webhooks (X-Hub-Signature-256); VerifyToken answers
	// the webhook's verification.
	AppSecret   string
	VerifyToken string
	Client      *http.Client
}

// DefaultGraphURL is the Graph API PGDock talks to.
const DefaultGraphURL = "https://graph.facebook.com/v21.0"

// SendText sends body to the number to (E.164).
func (c CloudAPI) SendText(ctx context.Context, to, body string) (string, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultGraphURL
	}
	if len(body) > 4096 {
		body = body[:4090] + "…"
	}
	payload, _ := json.Marshal(map[string]any{
		"messaging_product": "whatsapp", "recipient_type": "individual", "to": strings.TrimPrefix(to, "+"),
		"type": "text", "text": map[string]any{"preview_url": false, "body": body},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/"+c.PhoneNumberID+"/messages", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	hc := c.Client
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("whatsapp: %d %.300s", res.StatusCode, raw)
	}
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Messages) == 0 {
		return "", fmt.Errorf("whatsapp: unexpected answer %.300s", raw)
	}
	return out.Messages[0].ID, nil
}

// ErrBadSignature is a webhook whose signature doesn't verify.
var ErrBadSignature = errors.New("signature does not verify")

// VerifySignature checks X-Hub-Signature-256 ("sha256=<hex HMAC of the
// body with the app secret>").
func (c CloudAPI) VerifySignature(body []byte, header string) error {
	if c.AppSecret == "" {
		return ErrBadSignature
	}
	m := hmac.New(sha256.New, []byte(c.AppSecret))
	m.Write(body)
	want := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(header)) {
		return ErrBadSignature
	}
	return nil
}

// Sign is the X-Hub-Signature-256 of body with secret (for tests and the fake).
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Inbound is a customer's WhatsApp message.
type Inbound struct {
	From, Name, Body, ID string
}

// ParseWebhook extracts the text messages in a webhook body. Statuses
// (delivered, read) and media are skipped; media get a placeholder.
func ParseWebhook(body []byte) ([]Inbound, error) {
	var w struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Contacts []struct {
						WaID    string `json:"wa_id"`
						Profile struct {
							Name string `json:"name"`
						} `json:"profile"`
					} `json:"contacts"`
					Messages []struct {
						From string `json:"from"`
						ID   string `json:"id"`
						Type string `json:"type"`
						Text struct {
							Body string `json:"body"`
						} `json:"text"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, err
	}
	var out []Inbound
	for _, e := range w.Entry {
		for _, ch := range e.Changes {
			names := map[string]string{}
			for _, c := range ch.Value.Contacts {
				names[c.WaID] = c.Profile.Name
			}
			for _, m := range ch.Value.Messages {
				text := m.Text.Body
				if m.Type != "text" {
					text = "(a " + m.Type + " message, which the support console can't show yet)"
				}
				out = append(out, Inbound{From: "+" + strings.TrimPrefix(m.From, "+"), Name: names[m.From], Body: text, ID: "wa:" + m.ID})
			}
		}
	}
	return out, nil
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// NormalizePhone turns a typed number into E.164, or "".
func NormalizePhone(s string) string {
	s = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(s))
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if strings.HasPrefix(s, "0") && len(s) == 11 {
		s = "+234" + s[1:] // a Nigerian local number
	}
	if !strings.HasPrefix(s, "+") {
		s = "+" + s
	}
	if !e164.MatchString(s) {
		return ""
	}
	return s
}

// WhatsAppPlans are the plans WhatsApp support is for (V3 §7.1).
func WhatsAppPlan(plan string) bool { return plan == "pro" || plan == "team" }

// InboundWhatsApp threads a customer's WhatsApp message into the open
// ticket with that number, or opens one for the number's organisation. A
// number no Pro or Team organisation registered gets a reply saying how
// to reach support instead.
func (s *Service) InboundWhatsApp(ctx context.Context, in Inbound) (*store.Ticket, error) {
	q := store.New(s.db)
	if t, err := q.OpenTicketFrom(ctx, store.OpenTicketFromParams{Requester: in.From, Channel: ChannelWhatsApp}); err == nil {
		_, err := s.CustomerReply(ctx, t, in.From, nil, in.Body, in.ID)
		return &t, err
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	org, err := q.OrgByPhone(ctx, in.From)
	plan := "free"
	if err == nil {
		if plan, err = q.OrgPlan(ctx, org); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err != nil || !WhatsAppPlan(plan) {
		if s.whatsapp != nil {
			msg := "PGDock support on WhatsApp is for Pro and Team organisations that registered this number (Organisation → Support)."
			if s.cfg.Address != "" {
				msg += " Email " + s.cfg.Address + ", or open a ticket from the dashboard."
			}
			if _, err := s.whatsapp.SendText(ctx, in.From, msg); err != nil {
				s.log.Warn("support whatsapp", "err", err)
			}
		}
		return nil, nil
	}
	subject := firstLine(in.Body)
	if len(subject) > 80 {
		subject = subject[:80]
	}
	t, err := s.OpenTicket(ctx, Open{OrgID: &org, Requester: in.From, Name: in.Name, Channel: ChannelWhatsApp, Subject: subject, Body: in.Body, ExternalID: in.ID})
	return &t, err
}
