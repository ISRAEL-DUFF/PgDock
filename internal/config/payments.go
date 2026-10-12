package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Payments configures the payment provider (V3 §3.4, V4.1): the wallet
// provider, which keeps each organisation's wallet. Without its keys, wallets
// are off and invoices are paid by manual payments.
type Payments struct {
	ISwalletURL           string // PGDOCK_ISWALLET_BASE_URL
	ISwalletAPIKey        string // PGDOCK_ISWALLET_API_KEY (or _FILE)
	ISwalletWebhookSecret string // PGDOCK_ISWALLET_WEBHOOK_SECRET (or _FILE)
	ISwalletClientID      string // PGDOCK_ISWALLET_CLIENT_ID, when the key doesn't imply it
	// ISwalletOwnerPrefix keeps this installation's wallets apart from any
	// other's at the provider (owner references are unique across tenants):
	// pgdock:prod, pgdock:staging.
	ISwalletOwnerPrefix string // PGDOCK_ISWALLET_OWNER_PREFIX
}

// ISwalletOn reports whether the wallet provider is configured.
func (p Payments) ISwalletOn() bool { return p.ISwalletAPIKey != "" && p.ISwalletURL != "" }

func secretFrom(getenv func(string) string, readFile func(string) ([]byte, error), name string) (string, error) {
	v, file := getenv(name), getenv(name+"_FILE")
	switch {
	case v != "" && file != "":
		return "", fmt.Errorf("set only one of %s and %s_FILE", name, name)
	case file != "":
		b, err := readFile(file)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return v, nil
}

func loadPayments(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	p := Payments{
		ISwalletURL: strings.TrimRight(getenv("PGDOCK_ISWALLET_BASE_URL"), "/"), ISwalletClientID: getenv("PGDOCK_ISWALLET_CLIENT_ID"),
		ISwalletOwnerPrefix: strings.Trim(getenv("PGDOCK_ISWALLET_OWNER_PREFIX"), ": "),
	}
	var errs []error
	for name, dst := range map[string]*string{
		"PGDOCK_ISWALLET_API_KEY": &p.ISwalletAPIKey, "PGDOCK_ISWALLET_WEBHOOK_SECRET": &p.ISwalletWebhookSecret,
	} {
		v, err := secretFrom(getenv, readFile, name)
		if err != nil {
			errs = append(errs, err)
		}
		*dst = v
	}
	if p.ISwalletURL != "" {
		if x, err := url.Parse(p.ISwalletURL); err != nil || (x.Scheme != "https" && x.Scheme != "http") || x.Host == "" {
			errs = append(errs, errors.New("PGDOCK_ISWALLET_BASE_URL must be an http(s) URL"))
		}
	}
	if p.ISwalletAPIKey != "" {
		switch {
		case p.ISwalletURL == "":
			errs = append(errs, errors.New("PGDOCK_ISWALLET_API_KEY needs PGDOCK_ISWALLET_BASE_URL"))
		case len(p.ISwalletWebhookSecret) < 16:
			errs = append(errs, errors.New("PGDOCK_ISWALLET_API_KEY needs PGDOCK_ISWALLET_WEBHOOK_SECRET (at least 16 characters), the signing secret of the webhook subscription"))
		case p.ISwalletOwnerPrefix == "":
			errs = append(errs, errors.New("PGDOCK_ISWALLET_API_KEY needs PGDOCK_ISWALLET_OWNER_PREFIX (for example pgdock:prod): wallet owner references are unique across everyone using the provider, so each environment needs its own"))
		}
	}
	cfg.Payments = p
	return errs
}
