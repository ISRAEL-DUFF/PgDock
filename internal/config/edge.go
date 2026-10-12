package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Edge configures backend services' gateway, pgdock-edge (V4 §2.1).
type Edge struct {
	// Domain is the API hostname suffix (PGDOCK_API_DOMAIN): projects are
	// served at https://<ref>.<Domain>. Empty shows no URL.
	Domain string
	// Secret signs pgdock-edge's requests (PGDOCK_EDGE_SECRET or _FILE),
	// the same value as the edges'. Empty disables the edge feed.
	Secret string
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func loadEdge(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	e := Edge{Domain: strings.ToLower(strings.Trim(getenv("PGDOCK_API_DOMAIN"), "."))}
	var errs []error
	if e.Domain != "" && !domainRe.MatchString(e.Domain) && e.Domain != "localhost" {
		errs = append(errs, fmt.Errorf("PGDOCK_API_DOMAIN: %q is not a domain name", e.Domain))
	}
	secret, file := getenv("PGDOCK_EDGE_SECRET"), getenv("PGDOCK_EDGE_SECRET_FILE")
	switch {
	case secret != "" && file != "":
		errs = append(errs, errors.New("set only one of PGDOCK_EDGE_SECRET and PGDOCK_EDGE_SECRET_FILE"))
	case file != "":
		b, err := readFile(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_EDGE_SECRET_FILE: %w", err))
		}
		secret = strings.TrimSpace(string(b))
	}
	if secret != "" && len(secret) < 32 {
		errs = append(errs, errors.New("PGDOCK_EDGE_SECRET must be at least 32 characters"))
	}
	e.Secret = secret
	cfg.Edge = e
	return errs
}
