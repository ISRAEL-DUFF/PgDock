package cli

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalFlagsAnywhere(t *testing.T) {
	a := &App{}
	rest := a.globalFlags([]string{"sql", "--json", "blog", "--context=home", "-c", "select 1", "--server", "https://x"})
	if !a.json || a.contextName != "home" || a.server != "https://x" {
		t.Fatalf("flags: %+v", a)
	}
	if strings.Join(rest, " ") != "sql blog -c select 1" {
		t.Fatalf("rest: %q", rest)
	}
}

func TestExitCodes(t *testing.T) {
	for err, want := range map[error]int{
		nil:                                     ExitOK,
		usageErrorf("x"):                        ExitUsage,
		opFailed{"x"}:                           ExitOpFailed,
		&apiError{Status: http.StatusForbidden}: ExitPermission,
		&apiError{Status: http.StatusUnauthorized}: ExitPermission,
		&apiError{Status: http.StatusNotFound}:     ExitError,
		errors.New("x"):                            ExitError,
	} {
		if got := exitCode(err); got != want {
			t.Errorf("%v: %d, want %d", err, got, want)
		}
	}
}

func TestParseExpiry(t *testing.T) {
	for in, want := range map[string]int{"90d": 90, "48h": 2, "30": 30} {
		if got, err := parseExpiry(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	if _, err := parseExpiry("5h"); err == nil {
		t.Error("5h accepted")
	}
}

func TestContextsAndTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"PGDOCK_CONFIG_DIR": dir}
	a := &App{Getenv: func(k string) string { return env[k] }}
	cfg := &Config{Current: "home", Contexts: map[string]*Context{
		"home": {Server: "https://home.example/", Token: "pgd_home", Org: "o1"},
		"work": {Server: "https://work.example", TokenCmd: "echo pgd_work"},
	}}
	if err := a.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "config.toml")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	tg, err := a.resolve()
	if err != nil || tg.Server != "https://home.example" || tg.Token != "pgd_home" {
		t.Fatalf("current: %+v %v", tg, err)
	}
	a.contextName = "work"
	if tg, err = a.resolve(); err != nil || tg.Token != "pgd_work" {
		t.Fatalf("token_cmd: %+v %v", tg, err)
	}
	// PGDOCK_TOKEN wins, for CI.
	env["PGDOCK_TOKEN"], env["PGDOCK_SERVER"] = "pgd_ci", "https://ci.example"
	if tg, err = a.resolve(); err != nil || tg.Token != "pgd_ci" || tg.Server != "https://ci.example" || tg.Org != "" {
		t.Fatalf("environment: %+v %v", tg, err)
	}
	a.contextName = "nope"
	if _, err := a.resolve(); exitCode(err) != ExitUsage {
		t.Fatalf("unknown context: %v", err)
	}
}

func TestUsage(t *testing.T) {
	var out bytes.Buffer
	a := &App{Stdout: &out, Stderr: &out, Getenv: func(string) string { return "" }}
	if code := a.Run(nil); code != ExitUsage || !strings.Contains(out.String(), "projects") {
		t.Fatalf("no args: %d %s", code, out.String())
	}
	out.Reset()
	if code := a.Run([]string{"projects", "help"}); code != ExitOK || !strings.Contains(out.String(), "delete") {
		t.Fatalf("help: %d %s", code, out.String())
	}
	if code := a.Run([]string{"projects", "delete", "x"}); code != ExitUsage {
		t.Fatalf("not logged in: %d", code)
	}
}
