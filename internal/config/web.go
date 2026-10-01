package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Web configures the browser-facing side of pgdock-server.
type Web struct {
	// SecureCookies (PGDOCK_COOKIE_SECURE, default true) sets Secure and the
	// __Host- prefix on cookies. Only disable for plain-HTTP development.
	SecureCookies bool
	// TrustedProxies (PGDOCK_TRUSTED_PROXIES, comma-separated CIDRs or IPs)
	// may set X-Forwarded-For, e.g. Caddy's address.
	TrustedProxies []netip.Prefix
	// PublicIPs (PGDOCK_PUBLIC_IPS) are this host's public addresses, used
	// to check that the DB hostname points here.
	PublicIPs []netip.Addr
	// SetupCode (PGDOCK_SETUP_CODE) fixes the first-run setup code instead of
	// generating one; for automated installs and tests.
	SetupCode string
}

func loadWeb(getenv func(string) string, cfg *Config) []error {
	var errs []error
	w := Web{SecureCookies: true, SetupCode: getenv("PGDOCK_SETUP_CODE")}
	if v := getenv("PGDOCK_COOKIE_SECURE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_COOKIE_SECURE: %w", err))
		}
		w.SecureCookies = b
	}
	for _, part := range splitList(getenv("PGDOCK_TRUSTED_PROXIES")) {
		p, err := netip.ParsePrefix(part)
		if err != nil {
			a, aerr := netip.ParseAddr(part)
			if aerr != nil {
				errs = append(errs, fmt.Errorf("PGDOCK_TRUSTED_PROXIES: %q is not an IP or CIDR", part))
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		w.TrustedProxies = append(w.TrustedProxies, p.Masked())
	}
	for _, part := range splitList(getenv("PGDOCK_PUBLIC_IPS")) {
		a, err := netip.ParseAddr(part)
		if err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_PUBLIC_IPS: %q is not an IP address", part))
			continue
		}
		w.PublicIPs = append(w.PublicIPs, a)
	}
	cfg.Web = w
	return errs
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
