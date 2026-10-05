// Package statuspage is pgdock-status (V3 §2.6): a public status page that
// runs away from PGDock, probes it from outside, takes signed heartbeats
// and incidents from pgdock-server, opens and resolves incidents by itself,
// keeps 90 days of history, and emails subscribers. Its state is one SQLite
// file; it never touches PGDock's database.
package statuspage

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/statusapi"
)

// Config is status.toml.
type Config struct {
	// Listen is the HTTP address (default :8080). Put TLS in front.
	Listen string `toml:"listen"`
	// Data is the SQLite file (default /var/lib/pgdock-status/status.db).
	Data string `toml:"data"`
	// PublicURL is where the page is reached, for links in emails and RSS.
	PublicURL string `toml:"public_url"`
	Title     string `toml:"title"`
	// Interval between checks (default 1m: the SLA counts minutes).
	Interval duration `toml:"interval"`
	// FailAfter consecutive failed checks open an incident (default 3);
	// RecoverAfter consecutive good ones resolve it (default 2).
	FailAfter    int `toml:"fail_after"`
	RecoverAfter int `toml:"recover_after"`
	// HeartbeatTTL: a heartbeat component with nothing newer is "unknown"
	// (default 5m).
	HeartbeatTTL duration `toml:"heartbeat_ttl"`
	// PushSecret signs pgdock-server's heartbeats and incidents (or
	// PushSecretFile). Without one, pushes are refused.
	PushSecret     string `toml:"push_secret"`
	PushSecretFile string `toml:"push_secret_file"`
	// TrustProxy takes the client address from X-Forwarded-For (for the
	// subscription rate limit) when a reverse proxy sets it.
	TrustProxy bool `toml:"trust_proxy"`
	// SMTP enables email subscriptions.
	SMTP       *SMTP       `toml:"smtp"`
	Components []Component `toml:"component"`
}

// SMTP is the status service's own mail settings.
type SMTP struct {
	Host         string `toml:"host"`
	Port         int    `toml:"port"`
	Username     string `toml:"username"`
	Password     string `toml:"password"`
	PasswordFile string `toml:"password_file"`
	From         string `toml:"from"`
	TLS          string `toml:"tls"` // starttls (default), tls, none
}

// Component is one line on the page.
type Component struct {
	ID          string `toml:"id"`
	Name        string `toml:"name"`
	Region      string `toml:"region"`
	Description string `toml:"description"`
	// Heartbeat components take their state from pgdock-server's
	// heartbeats instead of probes.
	Heartbeat bool    `toml:"heartbeat"`
	Probes    []Probe `toml:"probe"`
}

// Probe is one outside check of a component. A component with several is
// degraded when some fail and down when all do.
type Probe struct {
	Name string `toml:"name"`
	// Kind is http (2xx from URL), postgres (Query through DSN) or tcp
	// (a connection to Addr).
	Kind    string   `toml:"kind"`
	URL     string   `toml:"url"`
	DSN     string   `toml:"dsn"`
	DSNFile string   `toml:"dsn_file"`
	Query   string   `toml:"query"`
	Addr    string   `toml:"addr"`
	Timeout duration `toml:"timeout"`
}

// duration decodes "90s"-style strings.
type duration struct{ time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

// LoadConfig reads and checks a status.toml.
func LoadConfig(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, err
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, fmt.Errorf("%s: unknown settings: %v", path, un)
	}
	if err := c.resolve(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func readSecret(value, file string) (string, error) {
	if file == "" {
		return value, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// resolve fills defaults, reads secret files and validates.
func (c *Config) resolve() error {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.Data == "" {
		c.Data = "/var/lib/pgdock-status/status.db"
	}
	if c.Title == "" {
		c.Title = "PGDock status"
	}
	if c.Interval.Duration == 0 {
		c.Interval.Duration = time.Minute
	}
	if c.FailAfter == 0 {
		c.FailAfter = 3
	}
	if c.RecoverAfter == 0 {
		c.RecoverAfter = 2
	}
	if c.HeartbeatTTL.Duration == 0 {
		c.HeartbeatTTL.Duration = 5 * time.Minute
	}
	var errs []error
	var err error
	if c.PushSecret, err = readSecret(c.PushSecret, c.PushSecretFile); err != nil {
		errs = append(errs, fmt.Errorf("push_secret_file: %w", err))
	} else if c.PushSecret != "" && len(c.PushSecret) < 32 {
		errs = append(errs, errors.New("push_secret must be at least 32 characters"))
	}
	if u, err := url.Parse(c.PublicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, errors.New("public_url must be the page's http(s) URL"))
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if c.Interval.Duration < time.Second || c.FailAfter < 1 || c.RecoverAfter < 1 {
		errs = append(errs, errors.New("interval must be at least 1s and fail_after and recover_after at least 1"))
	}
	if c.SMTP != nil {
		if c.SMTP.Password, err = readSecret(c.SMTP.Password, c.SMTP.PasswordFile); err != nil {
			errs = append(errs, fmt.Errorf("smtp.password_file: %w", err))
		}
		mc := c.SMTP.mail()
		if err := mc.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("smtp: %w", err))
		}
		c.SMTP.Port, c.SMTP.TLS = mc.Port, mc.TLS
	}
	if len(c.Components) == 0 {
		errs = append(errs, errors.New("no components: add [[component]] sections"))
	}
	seen := map[string]bool{}
	for i := range c.Components {
		comp := &c.Components[i]
		if !statusapi.ValidID(comp.ID) || seen[comp.ID] {
			errs = append(errs, fmt.Errorf("component %d: id %q is missing, not valid or repeated", i+1, comp.ID))
		}
		seen[comp.ID] = true
		if comp.Name == "" {
			comp.Name = comp.ID
		}
		if comp.Heartbeat == (len(comp.Probes) > 0) {
			errs = append(errs, fmt.Errorf("component %s: give it probes or heartbeat = true, not both", comp.ID))
		}
		for j := range comp.Probes {
			if err := comp.Probes[j].resolve(); err != nil {
				errs = append(errs, fmt.Errorf("component %s, probe %d: %w", comp.ID, j+1, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (s *SMTP) mail() mail.Config {
	return mail.Config{Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password, From: s.From, TLS: s.TLS}
}

func (p *Probe) resolve() error {
	if p.Timeout.Duration == 0 {
		p.Timeout.Duration = 10 * time.Second
	}
	if p.Name == "" {
		p.Name = p.Kind
	}
	switch p.Kind {
	case "http":
		if u, err := url.Parse(p.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("an http probe needs an http(s) url")
		}
	case "postgres":
		dsn, err := readSecret(p.DSN, p.DSNFile)
		if err != nil {
			return fmt.Errorf("dsn_file: %w", err)
		}
		if dsn == "" {
			return errors.New("a postgres probe needs dsn or dsn_file")
		}
		p.DSN = dsn
		if p.Query == "" {
			p.Query = "SELECT 1"
		}
	case "tcp":
		if p.Addr == "" {
			return errors.New("a tcp probe needs addr")
		}
	default:
		return fmt.Errorf("kind %q is not http, postgres or tcp", p.Kind)
	}
	return nil
}
