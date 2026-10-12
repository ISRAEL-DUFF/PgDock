package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Status connects PGDock to its separately hosted status page (V3 §2.6).
type Status struct {
	// URL is pgdock-status's base URL (PGDOCK_STATUS_URL); empty keeps
	// incidents in PGDock only.
	URL string
	// PushSecret signs heartbeats and incidents (PGDOCK_STATUS_PUSH_SECRET
	// or _FILE), the same as push_secret in status.toml.
	PushSecret string
	// Components are the status page's component IDs incidents can name
	// (PGDOCK_STATUS_COMPONENTS, comma-separated).
	Components []string
	// Region is the default region of incidents (PGDOCK_STATUS_REGION).
	Region string
}

func loadStatus(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	s := Status{URL: strings.TrimRight(getenv("PGDOCK_STATUS_URL"), "/"), Region: getenv("PGDOCK_STATUS_REGION")}
	for _, c := range strings.Split(getenv("PGDOCK_STATUS_COMPONENTS"), ",") {
		if c = strings.TrimSpace(c); c != "" {
			s.Components = append(s.Components, c)
		}
	}
	var errs []error
	secret, file := getenv("PGDOCK_STATUS_PUSH_SECRET"), getenv("PGDOCK_STATUS_PUSH_SECRET_FILE")
	switch {
	case secret != "" && file != "":
		errs = append(errs, errors.New("set only one of PGDOCK_STATUS_PUSH_SECRET and PGDOCK_STATUS_PUSH_SECRET_FILE"))
	case file != "":
		b, err := readFile(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_STATUS_PUSH_SECRET_FILE: %w", err))
		}
		secret = strings.TrimSpace(string(b))
	}
	s.PushSecret = secret
	if s.URL != "" {
		if u, err := url.Parse(s.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, errors.New("PGDOCK_STATUS_URL must be the status page's http(s) URL"))
		}
		if len(s.PushSecret) < 32 {
			errs = append(errs, errors.New("PGDOCK_STATUS_URL needs PGDOCK_STATUS_PUSH_SECRET (or _FILE) of at least 32 characters"))
		}
	}
	cfg.Status = s
	return errs
}
