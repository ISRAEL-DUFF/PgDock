package config

import (
	"errors"
	"net/url"
	"strings"
)

// Support configures support's channels (V3 §7.1).
type Support struct {
	// Email is the support address (PGDOCK_SUPPORT_EMAIL): replies come
	// from it and customers write to it.
	Email string
	// InboundSecret authenticates the email provider's inbound webhook
	// (PGDOCK_SUPPORT_INBOUND_SECRET or _FILE).
	InboundSecret string

	// WhatsApp Business Platform (Cloud API): PGDOCK_WHATSAPP_PHONE_NUMBER_ID,
	// _ACCESS_TOKEN, _APP_SECRET and _VERIFY_TOKEN (each also _FILE but the
	// id), and PGDOCK_WHATSAPP_GRAPH_URL.
	WhatsAppPhoneNumberID string
	WhatsAppAccessToken   string
	WhatsAppAppSecret     string
	WhatsAppVerifyToken   string
	WhatsAppGraphURL      string
}

// WhatsAppOn reports whether WhatsApp is configured.
func (s Support) WhatsAppOn() bool { return s.WhatsAppPhoneNumberID != "" }

func loadSupport(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	s := Support{Email: strings.TrimSpace(getenv("PGDOCK_SUPPORT_EMAIL")), WhatsAppPhoneNumberID: getenv("PGDOCK_WHATSAPP_PHONE_NUMBER_ID"),
		WhatsAppGraphURL: strings.TrimRight(getenv("PGDOCK_WHATSAPP_GRAPH_URL"), "/")}
	var errs []error
	for name, dst := range map[string]*string{
		"PGDOCK_SUPPORT_INBOUND_SECRET": &s.InboundSecret, "PGDOCK_WHATSAPP_ACCESS_TOKEN": &s.WhatsAppAccessToken,
		"PGDOCK_WHATSAPP_APP_SECRET": &s.WhatsAppAppSecret, "PGDOCK_WHATSAPP_VERIFY_TOKEN": &s.WhatsAppVerifyToken,
	} {
		v, err := secretFrom(getenv, readFile, name)
		if err != nil {
			errs = append(errs, err)
		}
		*dst = v
	}
	if s.Email != "" && !strings.Contains(s.Email, "@") {
		errs = append(errs, errors.New("PGDOCK_SUPPORT_EMAIL must be an email address"))
	}
	if s.InboundSecret != "" && len(s.InboundSecret) < 16 {
		errs = append(errs, errors.New("PGDOCK_SUPPORT_INBOUND_SECRET must be at least 16 characters"))
	}
	if s.WhatsAppOn() && (s.WhatsAppAccessToken == "" || len(s.WhatsAppAppSecret) < 16 || s.WhatsAppVerifyToken == "") {
		errs = append(errs, errors.New("PGDOCK_WHATSAPP_PHONE_NUMBER_ID needs PGDOCK_WHATSAPP_ACCESS_TOKEN, PGDOCK_WHATSAPP_APP_SECRET (at least 16 characters) and PGDOCK_WHATSAPP_VERIFY_TOKEN"))
	}
	if s.WhatsAppGraphURL != "" {
		if u, err := url.Parse(s.WhatsAppGraphURL); err != nil || u.Scheme != "https" && u.Scheme != "http" {
			errs = append(errs, errors.New("PGDOCK_WHATSAPP_GRAPH_URL must be an http(s) URL"))
		}
	}
	cfg.Support = s
	return errs
}
