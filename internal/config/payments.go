package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Payments configures the payment providers (V3 §3.4). A provider without
// its keys is off; channel routing defaults to the spec's (cards through
// Flutterwave; transfers through iSpend with Flutterwave as the fallback;
// wallets through iSpend) among the providers that are on.
type Payments struct {
	FlutterwaveURL         string // PGDOCK_FLW_BASE_URL, default the live API
	FlutterwaveSecretKey   string // PGDOCK_FLW_SECRET_KEY (or _FILE)
	FlutterwaveWebhookHash string // PGDOCK_FLW_WEBHOOK_HASH (or _FILE)
	FlutterwaveBVN         string // PGDOCK_FLW_BVN, for permanent virtual accounts

	ISpendURL           string // PGDOCK_ISPEND_BASE_URL
	ISpendAPIKey        string // PGDOCK_ISPEND_API_KEY (or _FILE)
	ISpendWebhookSecret string // PGDOCK_ISPEND_WEBHOOK_SECRET (or _FILE)

	Cards      string // PGDOCK_PAY_CARDS
	VAPrimary  string // PGDOCK_PAY_VA_PRIMARY
	VAFallback string // PGDOCK_PAY_VA_FALLBACK
	Wallet     string // PGDOCK_PAY_WALLET
}

// FlutterwaveOn reports whether Flutterwave has its keys.
func (p Payments) FlutterwaveOn() bool { return p.FlutterwaveSecretKey != "" }

// ISpendOn reports whether iSpend has its keys.
func (p Payments) ISpendOn() bool { return p.ISpendAPIKey != "" && p.ISpendURL != "" }

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
		FlutterwaveURL: strings.TrimRight(getenv("PGDOCK_FLW_BASE_URL"), "/"), FlutterwaveBVN: getenv("PGDOCK_FLW_BVN"),
		ISpendURL: strings.TrimRight(getenv("PGDOCK_ISPEND_BASE_URL"), "/"),
	}
	var errs []error
	for name, dst := range map[string]*string{
		"PGDOCK_FLW_SECRET_KEY": &p.FlutterwaveSecretKey, "PGDOCK_FLW_WEBHOOK_HASH": &p.FlutterwaveWebhookHash,
		"PGDOCK_ISPEND_API_KEY": &p.ISpendAPIKey, "PGDOCK_ISPEND_WEBHOOK_SECRET": &p.ISpendWebhookSecret,
	} {
		v, err := secretFrom(getenv, readFile, name)
		if err != nil {
			errs = append(errs, err)
		}
		*dst = v
	}
	for name, u := range map[string]string{"PGDOCK_FLW_BASE_URL": p.FlutterwaveURL, "PGDOCK_ISPEND_BASE_URL": p.ISpendURL} {
		if u == "" {
			continue
		}
		if x, err := url.Parse(u); err != nil || (x.Scheme != "https" && x.Scheme != "http") || x.Host == "" {
			errs = append(errs, fmt.Errorf("%s must be an http(s) URL", name))
		}
	}
	if p.FlutterwaveOn() && len(p.FlutterwaveWebhookHash) < 16 {
		errs = append(errs, errors.New("PGDOCK_FLW_SECRET_KEY needs PGDOCK_FLW_WEBHOOK_HASH (at least 16 characters), the secret hash set for webhooks"))
	}
	if p.ISpendAPIKey != "" && p.ISpendURL == "" {
		errs = append(errs, errors.New("PGDOCK_ISPEND_API_KEY needs PGDOCK_ISPEND_BASE_URL"))
	}
	if p.ISpendOn() && len(p.ISpendWebhookSecret) < 16 {
		errs = append(errs, errors.New("PGDOCK_ISPEND_API_KEY needs PGDOCK_ISPEND_WEBHOOK_SECRET (at least 16 characters)"))
	}
	on := map[string]bool{"flutterwave": p.FlutterwaveOn(), "ispend": p.ISpendOn(), "": true}
	pick := func(env string, def ...string) string {
		if v := strings.ToLower(strings.TrimSpace(getenv(env))); v != "" {
			if v == "none" {
				return ""
			}
			if !on[v] {
				errs = append(errs, fmt.Errorf("%s=%s: that provider isn't configured", env, v))
			}
			return v
		}
		for _, d := range def {
			if on[d] {
				return d
			}
		}
		return ""
	}
	p.Cards = pick("PGDOCK_PAY_CARDS", "flutterwave")
	p.VAPrimary = pick("PGDOCK_PAY_VA_PRIMARY", "ispend", "flutterwave")
	fallback := "flutterwave"
	if p.VAPrimary == "flutterwave" {
		fallback = ""
	}
	p.VAFallback = pick("PGDOCK_PAY_VA_FALLBACK", fallback)
	p.Wallet = pick("PGDOCK_PAY_WALLET", "ispend")
	cfg.Payments = p
	return errs
}
