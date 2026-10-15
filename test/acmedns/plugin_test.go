// Package acmedns_test checks what Caddy's acme-dns plugin does with the config
// that deploy/compose/enable-services.sh writes: it parses that file's
// `dns acmedns { … }` block with the plugin's own Caddyfile parser, then asks
// it to present a challenge to a fake acme-dns server, and looks at the request.
//
// This exists because the plugin expands {env.X} in the password only: a config
// that used it for the username, the subdomain or the server URL was accepted
// by `caddy validate` and then failed at the first certificate.
package acmedns_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/caddy-dns/acmedns"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/libdns/libdns"
)

// block returns the `acmedns { … }` block of a generated Caddy file.
func block(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	in := false
	for _, l := range strings.Split(string(raw), "\n") {
		s := strings.TrimSpace(l)
		switch {
		case !in && strings.HasPrefix(s, "dns acmedns {"):
			in = true
			out = append(out, "acmedns {")
		case in:
			out = append(out, s)
			if s == "}" {
				return strings.Join(out, "\n")
			}
		}
	}
	t.Fatalf("no `dns acmedns {` block in %s", file)
	return ""
}

func TestGeneratedConfigPresentsTheChallengeWithTheRightCredentials(t *testing.T) {
	file := os.Getenv("ACMEDNS_CADDY_FILE")
	if file == "" {
		t.Skip("ACMEDNS_CADDY_FILE not set: run `make test-acmedns`")
	}
	wantUser, wantSub, wantPass := os.Getenv("ACMEDNS_EXPECT_USER"), os.Getenv("ACMEDNS_EXPECT_SUBDOMAIN"), os.Getenv("ACMEDNS_PASSWORD")
	if wantUser == "" || wantSub == "" || wantPass == "" {
		t.Fatal("ACMEDNS_EXPECT_USER, ACMEDNS_EXPECT_SUBDOMAIN and ACMEDNS_PASSWORD are needed")
	}

	p := &acmedns.Provider{}
	if err := p.UnmarshalCaddyfile(caddyfile.NewTestDispenser(block(t, file))); err != nil {
		t.Fatalf("the plugin can't parse the generated block: %v", err)
	}
	// What the plugin would call: the server URL as written, before we point it at a fake.
	if !strings.HasPrefix(p.ServerURL, "https://") {
		t.Fatalf("server_url is %q: it must be the address itself (the plugin doesn't expand {env.X} there)", p.ServerURL)
	}

	type req struct{ user, key, sub, txt, path string }
	got := make(chan req, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct{ Subdomain, Txt string }
		_ = json.Unmarshal(b, &body)
		got <- req{r.Header.Get("X-Api-User"), r.Header.Get("X-Api-Key"), body.Subdomain, body.Txt, r.URL.Path}
		_, _ = w.Write([]byte(`{"txt":"` + body.Txt + `"}`))
	}))
	defer srv.Close()
	p.ServerURL = srv.URL

	if err := p.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	rec := libdns.RR{Type: "TXT", Name: "_acme-challenge.api", Data: "the-challenge-value"}
	if _, err := p.AppendRecords(context.Background(), "xenibase.ng.", []libdns.Record{rec}); err != nil {
		t.Fatalf("presenting the challenge: %v", err)
	}
	r := <-got
	if r.path != "/update" || r.txt != "the-challenge-value" {
		t.Errorf("request %+v: want POST /update with the challenge value", r)
	}
	if r.user != wantUser {
		t.Errorf("X-Api-User = %q, want %q", r.user, wantUser)
	}
	if r.sub != wantSub {
		t.Errorf("subdomain = %q, want %q", r.sub, wantSub)
	}
	if r.key != wantPass {
		t.Errorf("X-Api-Key = %q, want the password from the environment", r.key)
	}
}
