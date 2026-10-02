package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Insight configures the SQL console and metrics (M6).
type Insight struct {
	// ConsoleDisabled turns the SQL console and table browser off for every
	// project (PGDOCK_CONSOLE_DISABLED).
	ConsoleDisabled bool
	// MetricsInterval is how often project and node metrics are sampled
	// (PGDOCK_METRICS_INTERVAL, default 1m).
	MetricsInterval time.Duration
	// MetricsToken lets a scraper read /metrics with "Authorization:
	// Bearer <token>" (PGDOCK_METRICS_TOKEN). Without it only signed-in
	// operators can.
	MetricsToken string
	// AlertsInterval is how often alert conditions are evaluated
	// (PGDOCK_ALERTS_INTERVAL, default 30s).
	AlertsInterval time.Duration
	// PublicURL is the web UI's address, for links in alerts
	// (PGDOCK_PUBLIC_URL, e.g. https://pgdock.example.com).
	PublicURL string
	// TenancySweep is how often opaque renames, credential grace periods,
	// usage recording, and org deletions are processed
	// (PGDOCK_TENANCY_SWEEP_INTERVAL, default 5m).
	TenancySweep time.Duration
}

func loadInsight(getenv func(string) string, cfg *Config) []error {
	var errs []error
	in := Insight{MetricsInterval: time.Minute, MetricsToken: getenv("PGDOCK_METRICS_TOKEN"), AlertsInterval: 30 * time.Second,
		PublicURL: strings.TrimRight(getenv("PGDOCK_PUBLIC_URL"), "/")}
	if v := getenv("PGDOCK_ALERTS_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > time.Hour {
			errs = append(errs, fmt.Errorf("PGDOCK_ALERTS_INTERVAL: must be a duration from 1s to 1h, got %q", v))
		}
		in.AlertsInterval = d
	}
	if u := in.PublicURL; u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		errs = append(errs, fmt.Errorf("PGDOCK_PUBLIC_URL: must start with https://, got %q", u))
	}
	if v := getenv("PGDOCK_CONSOLE_DISABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_CONSOLE_DISABLED: %w", err))
		}
		in.ConsoleDisabled = b
	}
	if v := getenv("PGDOCK_METRICS_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > time.Hour {
			errs = append(errs, fmt.Errorf("PGDOCK_METRICS_INTERVAL: must be a duration from 1s to 1h, got %q", v))
		}
		in.MetricsInterval = d
	}
	in.TenancySweep = 5 * time.Minute
	if v := getenv("PGDOCK_TENANCY_SWEEP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second || d > time.Hour {
			errs = append(errs, fmt.Errorf("PGDOCK_TENANCY_SWEEP_INTERVAL: must be a duration from 1s to 1h, got %q", v))
		}
		in.TenancySweep = d
	}
	if t := in.MetricsToken; t != "" && len(t) < 24 {
		errs = append(errs, errors.New("PGDOCK_METRICS_TOKEN: must be at least 24 characters"))
	}
	cfg.Insight = in
	return errs
}
