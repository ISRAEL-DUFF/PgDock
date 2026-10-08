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
