package config

import (
	"strings"
	"testing"
)

func TestAuthPhone(t *testing.T) {
	cfg, err := load(env(nil), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.AuthPhone; a.SMSOn() || a.SMSPriceMinor != 450 || a.WhatsAppPriceMinor != 1500 || a.Currency != "NGN" || a.FreeAllowed {
		t.Fatalf("defaults: %+v", a)
	}
	cfg, err = load(env(map[string]string{"PGDOCK_TERMII_API_KEY": "tk", "PGDOCK_TERMII_SENDER_ID": "PGDock",
		"PGDOCK_SMS_PRICE_MINOR": "500", "PGDOCK_PHONE_AUTH_FREE": "true", "PGDOCK_MESSAGE_CURRENCY": "ghs"}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.AuthPhone; !a.SMSOn() || a.SMSPriceMinor != 500 || !a.FreeAllowed || a.Currency != "GHS" {
		t.Fatalf("set: %+v", a)
	}
	cfg, err = load(env(map[string]string{"PGDOCK_AFRICASTALKING_API_KEY": "at", "PGDOCK_AFRICASTALKING_USERNAME": "pgdock"}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.AuthPhone; !a.SMSOn() || !a.ATOn() || a.ATUsername != "pgdock" {
		t.Fatalf("africa's talking: %+v", a)
	}
	for _, bad := range []map[string]string{
		{"PGDOCK_TERMII_API_KEY": "tk"},
		{"PGDOCK_AFRICASTALKING_API_KEY": "at"},
		{"PGDOCK_AFRICASTALKING_API_KEY": "at", "PGDOCK_AFRICASTALKING_USERNAME": "u", "PGDOCK_AFRICASTALKING_URL": "x"},
		{"PGDOCK_SMS_PRICE_MINOR": "4.5"},
		{"PGDOCK_WHATSAPP_OTP_TEMPLATE": "code"},
		{"PGDOCK_TERMII_URL": "ftp://x"},
	} {
		if _, err := load(env(bad), noFiles); err == nil || !strings.Contains(err.Error(), "PGDOCK_") {
			t.Fatalf("%v: %v", bad, err)
		}
	}
}
