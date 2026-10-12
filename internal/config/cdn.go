package config

import (
	"errors"
	"strings"
)

// CDN is the CDN in front of pgdock-edge, purged when a public bucket goes
// private (V4 §5.4): Cloudflare's zone (PGDOCK_CDN_CLOUDFLARE_ZONE_ID) and
// an API token with Cache Purge permission (PGDOCK_CDN_CLOUDFLARE_TOKEN, or
// _FILE). Unset, nothing is purged and cached files expire on their own.
type CDN struct {
	CloudflareZoneID string
	CloudflareToken  string
}

// On reports whether purging is set up.
func (c CDN) On() bool { return c.CloudflareZoneID != "" }

func loadCDN(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	c := CDN{CloudflareZoneID: strings.TrimSpace(getenv("PGDOCK_CDN_CLOUDFLARE_ZONE_ID"))}
	var errs []error
	tok, err := secretFrom(getenv, readFile, "PGDOCK_CDN_CLOUDFLARE_TOKEN")
	if err != nil {
		errs = append(errs, err)
	}
	c.CloudflareToken = tok
	if (c.CloudflareZoneID == "") != (c.CloudflareToken == "") {
		errs = append(errs, errors.New("PGDOCK_CDN_CLOUDFLARE_ZONE_ID and PGDOCK_CDN_CLOUDFLARE_TOKEN go together"))
	}
	cfg.CDN = c
	return errs
}
