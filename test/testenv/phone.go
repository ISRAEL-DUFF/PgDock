package testenv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// FakePhone answers as Termii (POST /api/sms/send), Africa's Talking (POST
// /version1/messaging) and the WhatsApp Cloud API (POST /<phone
// id>/messages), recording what it was asked to send.
type FakePhone struct {
	URL string
	srv *httptest.Server

	mu    sync.Mutex
	sent  []PhoneMessage
	fail  int  // fail the next n sends with a 500
	down  bool // fail every send with a 503 (an outage)
	tried int  // sends asked for, failed or not
}

// PhoneMessage is one recorded send.
type PhoneMessage struct {
	Channel string // sms | whatsapp
	To      string // without the +
	Body    string // the SMS text
	Code    string // the WhatsApp template's code
}

// NewFakePhone starts one.
func NewFakePhone() *FakePhone {
	f := &FakePhone{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = f.srv.URL
	return f
}

// Close stops it.
func (f *FakePhone) Close() { f.srv.Close() }

// SetDown starts or ends an outage: every send fails with a 503.
func (f *FakePhone) SetDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

// Tried is how many sends it was asked for, failed or not.
func (f *FakePhone) Tried() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tried
}

// FailNext makes the next n sends fail.
func (f *FakePhone) FailNext(n int) {
	f.mu.Lock()
	f.fail = n
	f.mu.Unlock()
}

func (f *FakePhone) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tried++
	if f.down {
		http.Error(w, `{"message":"service unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	if f.fail > 0 {
		f.fail--
		http.Error(w, `{"message":"down"}`, http.StatusInternalServerError)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/sms/send":
		var in struct{ To, From, SMS, APIKey string }
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		in.To, in.SMS, in.APIKey = raw["to"], raw["sms"], raw["api_key"]
		if in.APIKey == "" {
			http.Error(w, `{"message":"no key"}`, http.StatusUnauthorized)
			return
		}
		f.sent = append(f.sent, PhoneMessage{Channel: "sms", To: in.To, Body: in.SMS})
		_ = json.NewEncoder(w).Encode(map[string]any{"message_id": len(f.sent), "message": "Successfully Sent", "balance": 100})
	case r.Method == http.MethodPost && r.URL.Path == "/version1/messaging":
		_ = r.ParseForm()
		if r.Header.Get("apiKey") == "" || r.Form.Get("username") == "" {
			http.Error(w, `{"message":"no key"}`, http.StatusUnauthorized)
			return
		}
		f.sent = append(f.sent, PhoneMessage{Channel: "sms", To: strings.TrimPrefix(r.Form.Get("to"), "+"), Body: r.Form.Get("message")})
		_ = json.NewEncoder(w).Encode(map[string]any{"SMSMessageData": map[string]any{"Recipients": []map[string]string{
			{"status": "Success", "messageId": "ATXid_test", "cost": "NGN 3.2000"}}}})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
		var in struct {
			To       string `json:"to"`
			Template struct {
				Components []struct {
					Type       string `json:"type"`
					Parameters []struct {
						Text string `json:"text"`
					} `json:"parameters"`
				} `json:"components"`
			} `json:"template"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		m := PhoneMessage{Channel: "whatsapp", To: in.To}
		for _, c := range in.Template.Components {
			if c.Type == "body" && len(c.Parameters) > 0 {
				m.Code = c.Parameters[0].Text
			}
		}
		f.sent = append(f.sent, m)
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "wamid.test"}}})
	default:
		http.NotFound(w, r)
	}
}

// Sent is everything recorded.
func (f *FakePhone) Sent() []PhoneMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PhoneMessage(nil), f.sent...)
}

// Count is how many went to to (E.164, with or without +).
func (f *FakePhone) Count(to string) int {
	n := 0
	for _, m := range f.Sent() {
		if m.To == strings.TrimPrefix(to, "+") {
			n++
		}
	}
	return n
}

// WaitCode waits for a code sent to to on channel (the six digits in the
// SMS, or the WhatsApp template's code).
func (f *FakePhone) WaitCode(t testing.TB, channel, to string, after int) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		seen := 0
		for _, m := range f.Sent() {
			if m.Channel != channel || m.To != strings.TrimPrefix(to, "+") {
				continue
			}
			seen++
			if seen <= after {
				continue
			}
			if m.Code != "" {
				return m.Code
			}
			if c := sixDigitsIn(m.Body); c != "" {
				return c
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no %s code reached %s", channel, to)
	return ""
}

func sixDigitsIn(s string) string {
	run := 0
	for i, r := range s {
		if r >= '0' && r <= '9' {
			run++
			if run == 6 && (i+1 == len(s) || s[i+1] < '0' || s[i+1] > '9') {
				return s[i-5 : i+1]
			}
		} else {
			run = 0
		}
	}
	return ""
}

// FakeTurnstile answers Turnstile's siteverify: the token "pass" passes.
type FakeTurnstile struct{ *httptest.Server }

// NewFakeTurnstile starts one.
func NewFakeTurnstile() FakeTurnstile {
	return FakeTurnstile{httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ok := r.Form.Get("response") == "pass" && r.Form.Get("secret") != ""
		_ = json.NewEncoder(w).Encode(map[string]any{"success": ok})
	}))}
}
