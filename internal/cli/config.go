package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is ~/.config/pgdock/config.toml: named contexts, so one CLI can
// talk to more than one PGDock (V2 §7.1).
type Config struct {
	Current  string              `toml:"current"`
	Contexts map[string]*Context `toml:"contexts"`
}

// Context is one server and the organisation its token acts in.
type Context struct {
	Server  string `toml:"server"`
	Org     string `toml:"org,omitempty"`      // organisation id
	OrgName string `toml:"org_name,omitempty"` // for display
	User    string `toml:"user,omitempty"`     // email, for display
	Token   string `toml:"token,omitempty"`
	// TokenCmd prints the token, e.g. "pass show pgdock/home".
	TokenCmd string `toml:"token_cmd,omitempty"`
	TokenID  string `toml:"token_id,omitempty"`
	// CAFile verifies a server whose certificate isn't publicly trusted.
	CAFile string `toml:"ca_file,omitempty"`
}

// configDir is $PGDOCK_CONFIG_DIR, $XDG_CONFIG_HOME/pgdock, or
// ~/.config/pgdock.
func (a *App) configDir() (string, error) {
	if d := a.getenv("PGDOCK_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := a.getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "pgdock"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "pgdock"), nil
}

func (a *App) configPath() (string, error) {
	d, err := a.configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.toml"), nil
}

func (a *App) loadConfig() (*Config, error) {
	cfg := &Config{Contexts: map[string]*Context{}}
	p, err := a.configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(b), cfg); err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*Context{}
	}
	return cfg, nil
}

// saveConfig writes the config readable only by its owner: it holds tokens.
func (a *App) saveConfig(cfg *Config) error {
	p, err := a.configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (c *Config) names() []string {
	out := make([]string, 0, len(c.Contexts))
	for n := range c.Contexts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// target is what a command talks to: a server and a token.
type target struct {
	Name   string // context name ("" from the environment)
	Server string
	Token  string
	Org    string
	CAFile string
}

// resolve picks the server and token: PGDOCK_TOKEN (and PGDOCK_SERVER)
// win, for CI; then --context, PGDOCK_CONTEXT, or the current context.
func (a *App) resolve() (target, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return target{}, err
	}
	name := a.contextName
	if name == "" {
		name = a.getenv("PGDOCK_CONTEXT")
	}
	if name == "" {
		name = cfg.Current
	}
	var t target
	if c, ok := cfg.Contexts[name]; ok {
		t = target{Name: name, Server: c.Server, Token: c.Token, Org: c.Org, CAFile: c.CAFile}
		if t.Token == "" && c.TokenCmd != "" {
			out, err := exec.Command("sh", "-c", c.TokenCmd).Output()
			if err != nil {
				return t, fmt.Errorf("token_cmd for context %q: %w", name, err)
			}
			t.Token = strings.TrimSpace(string(out))
		}
	} else if a.contextName != "" {
		return t, usageErrorf("no context named %q (see pgdock context list)", a.contextName)
	}
	if v := a.getenv("PGDOCK_SERVER"); v != "" {
		t.Server = v
	}
	if a.server != "" {
		t.Server = a.server
	}
	if v := a.getenv("PGDOCK_TOKEN"); v != "" {
		t.Token, t.Org = v, "" // the token's own org (from whoami)
	}
	if v := a.getenv("PGDOCK_CA_FILE"); v != "" {
		t.CAFile = v
	}
	if t.Server == "" {
		return t, usageErrorf("not logged in: run pgdock login --server https://… (or set PGDOCK_SERVER and PGDOCK_TOKEN)")
	}
	if t.Token == "" {
		return t, usageErrorf("no token for %s: run pgdock login, or set PGDOCK_TOKEN", t.Server)
	}
	t.Server = strings.TrimRight(t.Server, "/")
	return t, nil
}

// ---- Cached personal database credentials ----------------------------------

// creds caches personal database URLs (the password is shown once by the
// server), so connect doesn't rotate them every time.
type credsFile struct {
	Projects map[string]credsEntry `toml:"projects"`
}

type credsEntry struct {
	Server     string `toml:"server"`
	PooledURL  string `toml:"pooled_url"`
	SessionURL string `toml:"session_url"`
}

func (a *App) credsPath() (string, error) {
	d, err := a.configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "credentials.toml"), nil
}

func (a *App) loadCreds() (*credsFile, error) {
	f := &credsFile{Projects: map[string]credsEntry{}}
	p, err := a.credsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(b), f); err != nil {
		return nil, err
	}
	if f.Projects == nil {
		f.Projects = map[string]credsEntry{}
	}
	return f, nil
}

func (a *App) saveCreds(f *credsFile) error {
	p, err := a.credsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return err
	}
	return os.WriteFile(p, buf.Bytes(), 0o600)
}
