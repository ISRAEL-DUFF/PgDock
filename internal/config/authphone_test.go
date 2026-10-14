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
	cfg, err = load(env(map[string]string{"XENIBASE_TERMII_API_KEY": "tk", "XENIBASE_TERMII_SENDER_ID": "Xenibase",
		"XENIBASE_SMS_PRICE_MINOR": "500", "XENIBASE_PHONE_AUTH_FREE": "true", "XENIBASE_MESSAGE_CURRENCY": "ghs"}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.AuthPhone; !a.SMSOn() || a.SMSPriceMinor != 500 || !a.FreeAllowed || a.Currency != "GHS" {
		t.Fatalf("set: %+v", a)
	}
	cfg, err = load(env(map[string]string{"XENIBASE_AFRICASTALKING_API_KEY": "at", "XENIBASE_AFRICASTALKING_USERNAME": "xenibase"}), noFiles)
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.AuthPhone; !a.SMSOn() || !a.ATOn() || a.ATUsername != "xenibase" {
		t.Fatalf("africa's talking: %+v", a)
	}
	for _, bad := range []map[string]string{
		{"XENIBASE_TERMII_API_KEY": "tk"},
		{"XENIBASE_AFRICASTALKING_API_KEY": "at"},
		{"XENIBASE_AFRICASTALKING_API_KEY": "at", "XENIBASE_AFRICASTALKING_USERNAME": "u", "XENIBASE_AFRICASTALKING_URL": "x"},
		{"XENIBASE_SMS_PRICE_MINOR": "4.5"},
		{"XENIBASE_WHATSAPP_OTP_TEMPLATE": "code"},
		{"XENIBASE_TERMII_URL": "ftp://x"},
	} {
		if _, err := load(env(bad), noFiles); err == nil || !strings.Contains(err.Error(), "XENIBASE_") {
			t.Fatalf("%v: %v", bad, err)
		}
	}
}
