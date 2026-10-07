package support

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FakeGraph is a fake of the WhatsApp Cloud API for tests: it records the
// messages PGDock sends and delivers customers' messages to PGDock's
// webhook, signed like Meta's.
type FakeGraph struct {
	AppSecret, AccessToken, PhoneNumberID string
	// WebhookURL is PGDock's webhook.
	WebhookURL string

	mu   sync.Mutex
	sent []FakeSent
	n    int
}

// FakeSent is a message PGDock sent.
type FakeSent struct{ To, Body string }

// NewFakeGraph returns a fake.
func NewFakeGraph() *FakeGraph {
	return &FakeGraph{AppSecret: "wa-app-secret", AccessToken: "wa-token", PhoneNumberID: "1001"}
}

// ServeHTTP is the Graph API's POST /{phone_number_id}/messages.
func (f *FakeGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/"+f.PhoneNumberID+"/messages" || r.Header.Get("Authorization") != "Bearer "+f.AccessToken {
		http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		return
	}
	var m struct {
		To   string `json:"to"`
		Text struct {
			Body string `json:"body"`
		} `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.sent = append(f.sent, FakeSent{To: "+" + m.To, Body: m.Text.Body})
	f.n++
	id := fmt.Sprintf("wamid.out%d", f.n)
	f.mu.Unlock()
	_, _ = fmt.Fprintf(w, `{"messaging_product":"whatsapp","messages":[{"id":%q}]}`, id)
}

// Sent returns what PGDock sent to a number.
func (f *FakeGraph) Sent(to string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sent {
		if s.To == to {
			out = append(out, s.Body)
		}
	}
	return out
}

// Deliver sends PGDock's webhook a customer's text message, returning the
// HTTP status.
func (f *FakeGraph) Deliver(from, name, text string) (int, error) {
	f.mu.Lock()
	f.n++
	id := fmt.Sprintf("wamid.in%d.%d", f.n, time.Now().UnixNano())
	f.mu.Unlock()
	wa := strings.TrimPrefix(from, "+")
	body, _ := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry": []any{map[string]any{"id": "waba", "changes": []any{map[string]any{"field": "messages", "value": map[string]any{
			"messaging_product": "whatsapp",
			"contacts":          []any{map[string]any{"wa_id": wa, "profile": map[string]any{"name": name}}},
			"messages":          []any{map[string]any{"from": wa, "id": id, "timestamp": fmt.Sprint(time.Now().Unix()), "type": "text", "text": map[string]any{"body": text}}},
		}}}}},
	})
	req, _ := http.NewRequest(http.MethodPost, f.WebhookURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", Sign(f.AppSecret, body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res.StatusCode, nil
}
