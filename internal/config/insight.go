package config

import (
	"errors"
	"fmt"
	"strconv"
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
}

func loadInsight(getenv func(string) string, cfg *Config) []error {
	var errs []error
	in := Insight{MetricsInterval: time.Minute, MetricsToken: getenv("PGDOCK_METRICS_TOKEN")}
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
	if t := in.MetricsToken; t != "" && len(t) < 24 {
		errs = append(errs, errors.New("PGDOCK_METRICS_TOKEN: must be at least 24 characters"))
	}
	cfg.Insight = in
	return errs
}
