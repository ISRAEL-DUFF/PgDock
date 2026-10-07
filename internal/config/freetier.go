package config

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// FreeTier configures pausing and archiving Free projects (V3 §4) and the
// waker that answers their connections.
type FreeTier struct {
	// WakerAddr is the waker as the poolers reach it, host:port
	// (PGDOCK_WAKER_ADDR). Empty turns pausing off: a paused project's
	// clients would only be refused.
	WakerAddr string
	// WakerListen is where the waker listens (PGDOCK_WAKER_LISTEN, default
	// ":6435"). Only the poolers should reach it.
	WakerListen string
	// PauseAfter, ArchiveAfter and DeleteAfter are PGDOCK_FREE_PAUSE_AFTER
	// (168h), PGDOCK_FREE_ARCHIVE_AFTER (2160h, 90 days) and
	// PGDOCK_FREE_DELETE_AFTER (8760h, a year).
	PauseAfter   time.Duration
	ArchiveAfter time.Duration
	DeleteAfter  time.Duration
}

// WakerHostPort splits WakerAddr.
func (f FreeTier) WakerHostPort() (string, int) {
	h, p, err := net.SplitHostPort(f.WakerAddr)
	if err != nil {
		return "", 0
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

// Signup configures open signup's protections (V3 §7.4).
type Signup struct {
	// TurnstileSiteKey and TurnstileSecret are the Cloudflare Turnstile
	// keys (PGDOCK_TURNSTILE_SITE_KEY, PGDOCK_TURNSTILE_SECRET or _FILE);
	// with them set, signing up needs a solved challenge.
	TurnstileSiteKey string
	TurnstileSecret  string
	// TurnstileURL is the verification endpoint (PGDOCK_TURNSTILE_URL).
	TurnstileURL string
	// PerIP is how many accounts one IP address may create a day
	// (PGDOCK_SIGNUPS_PER_IP, default 3; 0 turns the limit off).
	PerIP int
}

func loadFreeTier(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	var errs []error
	f := FreeTier{WakerAddr: getenv("PGDOCK_WAKER_ADDR"), WakerListen: getenv("PGDOCK_WAKER_LISTEN"),
		PauseAfter: 7 * 24 * time.Hour, ArchiveAfter: 90 * 24 * time.Hour, DeleteAfter: 365 * 24 * time.Hour}
	if f.WakerListen == "" {
		f.WakerListen = ":6435"
	}
	if f.WakerAddr != "" {
		if h, p := f.WakerHostPort(); h == "" || p < 1 || p > 65535 {
			errs = append(errs, fmt.Errorf("PGDOCK_WAKER_ADDR %q: want host:port", f.WakerAddr))
		}
	}
	for name, dst := range map[string]*time.Duration{
		"PGDOCK_FREE_PAUSE_AFTER": &f.PauseAfter, "PGDOCK_FREE_ARCHIVE_AFTER": &f.ArchiveAfter, "PGDOCK_FREE_DELETE_AFTER": &f.DeleteAfter,
	} {
		if v := getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 2*time.Hour {
				errs = append(errs, fmt.Errorf("%s %q: want a duration of at least 2h", name, v))
				continue
			}
			*dst = d
		}
	}
	cfg.FreeTier = f

	s := Signup{TurnstileSiteKey: getenv("PGDOCK_TURNSTILE_SITE_KEY"), TurnstileURL: getenv("PGDOCK_TURNSTILE_URL"), PerIP: 3}
	secret, err := secretFrom(getenv, readFile, "PGDOCK_TURNSTILE_SECRET")
	if err != nil {
		errs = append(errs, err)
	}
	s.TurnstileSecret = secret
	if (s.TurnstileSiteKey == "") != (s.TurnstileSecret == "") {
		errs = append(errs, fmt.Errorf("set both PGDOCK_TURNSTILE_SITE_KEY and PGDOCK_TURNSTILE_SECRET, or neither"))
	}
	if v := getenv("PGDOCK_SIGNUPS_PER_IP"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("PGDOCK_SIGNUPS_PER_IP %q: want a whole number", v))
		} else {
			s.PerIP = n
		}
	}
	cfg.Signup = s
	return errs
}
