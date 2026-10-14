package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Web configures the browser-facing side of xenibase-server.
type Web struct {
	// SecureCookies (XENIBASE_COOKIE_SECURE, default true) sets Secure and the
	// __Host- prefix on cookies. Only disable for plain-HTTP development.
	SecureCookies bool
	// TrustedProxies (XENIBASE_TRUSTED_PROXIES, comma-separated CIDRs or IPs)
	// may set X-Forwarded-For, e.g. Caddy's address.
	TrustedProxies []netip.Prefix
	// PublicIPs (XENIBASE_PUBLIC_IPS) are this host's public addresses, used
	// to check that the DB hostname points here.
	PublicIPs []netip.Addr
	// SetupCode (XENIBASE_SETUP_CODE) fixes the first-run setup code instead of
	// generating one; for automated installs and tests.
	SetupCode string
}

func loadWeb(getenv func(string) string, cfg *Config) []error {
	var errs []error
	w := Web{SecureCookies: true, SetupCode: getenv("XENIBASE_SETUP_CODE")}
	if v := getenv("XENIBASE_COOKIE_SECURE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("XENIBASE_COOKIE_SECURE: %w", err))
		}
		w.SecureCookies = b
	}
	for _, part := range splitList(getenv("XENIBASE_TRUSTED_PROXIES")) {
		p, err := netip.ParsePrefix(part)
		if err != nil {
			a, aerr := netip.ParseAddr(part)
			if aerr != nil {
				errs = append(errs, fmt.Errorf("XENIBASE_TRUSTED_PROXIES: %q is not an IP or CIDR", part))
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		w.TrustedProxies = append(w.TrustedProxies, p.Masked())
	}
	for _, part := range splitList(getenv("XENIBASE_PUBLIC_IPS")) {
		a, err := netip.ParseAddr(part)
		if err != nil {
			errs = append(errs, fmt.Errorf("XENIBASE_PUBLIC_IPS: %q is not an IP address", part))
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
