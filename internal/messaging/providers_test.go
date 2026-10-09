package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func capture(t *testing.T, answer string) (*httptest.Server, *[]*http.Request, *[]string) {
	t.Helper()
	var reqs []*http.Request
	var bodies []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqs, bodies = append(reqs, r), append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(s.Close)
	return s, &reqs, &bodies
}

func TestTermii(t *testing.T) {
	s, reqs, bodies := capture(t, `{"message_id":"3017544054459","message":"Successfully Sent","balance":9,"user":"x"}`)
	p := Termii{BaseURL: s.URL, APIKey: "k", SenderID: "PGDock"}
	sent, err := p.Send(context.Background(), Message{Channel: SMS, To: "+2348031234567", Body: "123456 is your code"})
	if err != nil || sent.ID != "3017544054459" {
		t.Fatalf("%+v %v", sent, err)
	}
	var b map[string]string
	_ = json.Unmarshal([]byte((*bodies)[0]), &b)
	if (*reqs)[0].URL.Path != "/api/sms/send" || b["to"] != "2348031234567" || b["channel"] != "dnd" || b["from"] != "PGDock" || b["api_key"] != "k" {
		t.Fatalf("%s %v", (*reqs)[0].URL.Path, b)
	}
	if _, err := p.Send(context.Background(), Message{Channel: WhatsApp}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestWhatsAppCloud(t *testing.T) {
	s, reqs, bodies := capture(t, `{"messaging_product":"whatsapp","messages":[{"id":"wamid.X"}]}`)
	p := WhatsAppCloud{BaseURL: s.URL, PhoneNumberID: "123", AccessToken: "tok", Template: "pgdock_otp"}
	sent, err := p.Send(context.Background(), Message{Channel: WhatsApp, To: "+2348031234567", Code: "654321"})
	if err != nil || sent.ID != "wamid.X" {
		t.Fatalf("%+v %v", sent, err)
	}
	if (*reqs)[0].URL.Path != "/123/messages" || (*reqs)[0].Header.Get("Authorization") != "Bearer tok" ||
		!strings.Contains((*bodies)[0], `"name":"pgdock_otp"`) || strings.Count((*bodies)[0], `"654321"`) != 2 {
		t.Fatalf("%s %s", (*reqs)[0].URL.Path, (*bodies)[0])
	}
}

func TestTwilioAndAfricasTalking(t *testing.T) {
	s, reqs, bodies := capture(t, `{"sid":"SM1"}`)
	tw := Twilio{BaseURL: s.URL, AccountSID: "AC1", AuthToken: "t", From: "+15005550006", WhatsAppFrom: "+14155238886"}
	if _, err := tw.Send(context.Background(), Message{Channel: WhatsApp, To: "+2348031234567", Body: "code 1"}); err != nil {
		t.Fatal(err)
	}
	f, _ := url.ParseQuery((*bodies)[0])
	if user, _, _ := (*reqs)[0].BasicAuth(); user != "AC1" || f.Get("To") != "whatsapp:+2348031234567" || f.Get("From") != "whatsapp:+14155238886" {
		t.Fatalf("%v", f)
	}
	s2, _, _ := capture(t, `{"SMSMessageData":{"Recipients":[{"status":"Success","messageId":"ATX","cost":"NGN 2.2000"}]}}`)
	at := AfricasTalking{BaseURL: s2.URL, Username: "u", APIKey: "k"}
	sent, err := at.Send(context.Background(), Message{Channel: SMS, To: "+2348031234567", Body: "x"})
	if err != nil || sent.ID != "ATX" || sent.CostMinor == nil || *sent.CostMinor != 220 || sent.Currency != "NGN" {
		t.Fatalf("%+v %v", sent, err)
	}
}

type flaky struct {
	name  string
	fail  bool
	calls int
}

func (f *flaky) Name() string            { return f.name }
func (f *flaky) Supports(ch string) bool { return ch == SMS }
func (f *flaky) Send(context.Context, Message) (Sent, error) {
	f.calls++
	if f.fail {
		return Sent{}, errors.New(f.name + " down")
	}
	return Sent{ID: f.name}, nil
}

func TestFailover(t *testing.T) {
	a, b := &flaky{name: "termii"}, &flaky{name: "africastalking"}
	now := time.Unix(1_800_000_000, 0)
	var alerts []string
	f := &Failover{Providers: []Provider{a, b}, Cooldown: time.Minute, now: func() time.Time { return now },
		OnFail: func(p string, err error) { alerts = append(alerts, p) }}
	msg := Message{Channel: SMS, To: "+2348031234567", Body: "123456"}
	if s, err := f.Send(context.Background(), msg); err != nil || s.Provider != "termii" {
		t.Fatalf("healthy: %+v %v", s, err)
	}
	// The primary goes down: the fallback sends, and for the cooldown the
	// primary isn't tried first.
	a.fail = true
	if s, err := f.Send(context.Background(), msg); err != nil || s.Provider != "africastalking" || a.calls != 2 {
		t.Fatalf("primary down: %+v %v (primary tried %d)", s, err, a.calls)
	}
	if s, err := f.Send(context.Background(), msg); err != nil || s.Provider != "africastalking" || a.calls != 2 {
		t.Fatalf("cooling: %+v %v (primary tried %d)", s, err, a.calls)
	}
	// Both down: the error names both; the cooling primary is still tried.
	b.fail = true
	if _, err := f.Send(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "termii down") || !strings.Contains(err.Error(), "africastalking down") {
		t.Fatalf("both down: %v", err)
	}
	// After the cooldown a recovered primary is first again.
	a.fail, b.fail = false, false
	now = now.Add(2 * time.Minute)
	if s, err := f.Send(context.Background(), msg); err != nil || s.Provider != "termii" {
		t.Fatalf("recovered: %+v %v", s, err)
	}
	if f.Name() != "termii+africastalking" || len(alerts) != 3 {
		t.Fatalf("name %q, alerts %v", f.Name(), alerts)
	}
	if _, err := f.Send(context.Background(), Message{Channel: WhatsApp}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("whatsapp: %v", err)
	}
}
