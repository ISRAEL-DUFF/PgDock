package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// AuthPhone is the platform's SMS and WhatsApp for project auth codes
// (V4 §6.2): projects without their own provider send through these and are
// billed per message.
type AuthPhone struct {
	// Termii (SMS): PGDOCK_TERMII_API_KEY (or _FILE), PGDOCK_TERMII_SENDER_ID
	// (an approved sender id), PGDOCK_TERMII_URL (the account's base URL).
	TermiiAPIKey   string
	TermiiSenderID string
	TermiiURL      string
	// Africa's Talking is the fallback when Termii fails (V4 §15):
	// PGDOCK_AFRICASTALKING_USERNAME, PGDOCK_AFRICASTALKING_API_KEY (or
	// _FILE), PGDOCK_AFRICASTALKING_FROM (a sender id, optional),
	// PGDOCK_AFRICASTALKING_URL.
	ATUsername string
	ATAPIKey   string
	ATFrom     string
	ATURL      string
	// WhatsApp authentication codes go out from support's WhatsApp number
	// (PGDOCK_WHATSAPP_PHONE_NUMBER_ID and _ACCESS_TOKEN) as the approved
	// authentication template PGDOCK_WHATSAPP_OTP_TEMPLATE in
	// PGDOCK_WHATSAPP_OTP_LANGUAGE (en).
	WhatsAppTemplate string
	WhatsAppLanguage string
	// What a message is billed at, in minor units of Currency
	// (PGDOCK_SMS_PRICE_MINOR, PGDOCK_WHATSAPP_PRICE_MINOR,
	// PGDOCK_MESSAGE_CURRENCY; NGN 4.50 and NGN 15.00 by default).
	SMSPriceMinor      int64
	WhatsAppPriceMinor int64
	Currency           string
	// FreeAllowed lets Free projects use the platform's providers
	// (PGDOCK_PHONE_AUTH_FREE; off: Free projects bring their own).
	FreeAllowed bool
}

// SMSOn reports whether the platform sends SMS.
func (a AuthPhone) SMSOn() bool { return a.TermiiAPIKey != "" || a.ATOn() }

// ATOn reports whether Africa's Talking is configured.
func (a AuthPhone) ATOn() bool { return a.ATAPIKey != "" }

func loadAuthPhone(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	a := AuthPhone{TermiiSenderID: strings.TrimSpace(getenv("PGDOCK_TERMII_SENDER_ID")),
		TermiiURL:        strings.TrimRight(getenv("PGDOCK_TERMII_URL"), "/"),
		ATUsername:       strings.TrimSpace(getenv("PGDOCK_AFRICASTALKING_USERNAME")),
		ATFrom:           strings.TrimSpace(getenv("PGDOCK_AFRICASTALKING_FROM")),
		ATURL:            strings.TrimRight(getenv("PGDOCK_AFRICASTALKING_URL"), "/"),
		WhatsAppTemplate: strings.TrimSpace(getenv("PGDOCK_WHATSAPP_OTP_TEMPLATE")),
		WhatsAppLanguage: strings.TrimSpace(getenv("PGDOCK_WHATSAPP_OTP_LANGUAGE")),
		Currency:         strings.ToUpper(strings.TrimSpace(getenv("PGDOCK_MESSAGE_CURRENCY"))),
		SMSPriceMinor:    450, WhatsAppPriceMinor: 1500}
	var errs []error
	key, err := secretFrom(getenv, readFile, "PGDOCK_TERMII_API_KEY")
	if err != nil {
		errs = append(errs, err)
	}
	a.TermiiAPIKey = key
	if a.ATAPIKey, err = secretFrom(getenv, readFile, "PGDOCK_AFRICASTALKING_API_KEY"); err != nil {
		errs = append(errs, err)
	}
	if a.ATOn() && a.ATUsername == "" {
		errs = append(errs, errors.New("PGDOCK_AFRICASTALKING_API_KEY needs PGDOCK_AFRICASTALKING_USERNAME"))
	}
	if a.Currency == "" {
		a.Currency = "NGN"
	} else if len(a.Currency) != 3 {
		errs = append(errs, errors.New("PGDOCK_MESSAGE_CURRENCY must be a 3-letter currency code"))
	}
	for name, dst := range map[string]*int64{"PGDOCK_SMS_PRICE_MINOR": &a.SMSPriceMinor, "PGDOCK_WHATSAPP_PRICE_MINOR": &a.WhatsAppPriceMinor} {
		if v := getenv(name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				errs = append(errs, fmt.Errorf("%s must be a whole number of minor units", name))
			}
			*dst = n
		}
	}
	if v := getenv("PGDOCK_PHONE_AUTH_FREE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, errors.New("PGDOCK_PHONE_AUTH_FREE must be true or false"))
		}
		a.FreeAllowed = b
	}
	if a.TermiiAPIKey != "" && a.TermiiSenderID == "" {
		errs = append(errs, errors.New("PGDOCK_TERMII_API_KEY needs PGDOCK_TERMII_SENDER_ID"))
	}
	for name, v := range map[string]string{"PGDOCK_TERMII_URL": a.TermiiURL, "PGDOCK_AFRICASTALKING_URL": a.ATURL} {
		if v == "" {
			continue
		}
		if u, err := url.Parse(v); err != nil || u.Scheme != "https" && u.Scheme != "http" {
			errs = append(errs, fmt.Errorf("%s must be an http(s) URL", name))
		}
	}
	if a.WhatsAppTemplate != "" && getenv("PGDOCK_WHATSAPP_PHONE_NUMBER_ID") == "" {
		errs = append(errs, errors.New("PGDOCK_WHATSAPP_OTP_TEMPLATE needs PGDOCK_WHATSAPP_PHONE_NUMBER_ID and PGDOCK_WHATSAPP_ACCESS_TOKEN"))
	}
	cfg.AuthPhone = a
	return errs
}
