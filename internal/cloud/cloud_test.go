package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHetznerAgainstFake(t *testing.T) {
	fake := NewFakeHetzner("tok")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	h := &HetznerProvider{API: srv.URL + "/v1", Token: "tok"}

	cat, err := h.PriceCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cat {
		if p.Type == "cx11" {
			t.Error("a deprecated type is in the catalog")
		}
	}
	p, err := Cheapest(cat, "fsn1", 4, 8, 100)
	if err != nil || p.Type != "cpx31" || p.MonthlyMinor != 1511 || p.Currency != "EUR" {
		t.Fatalf("cheapest: %+v %v", p, err)
	}
	if _, err := Cheapest(cat, "fsn1", 64, 1, 1); err == nil {
		t.Error("an impossible size fit")
	}

	s, err := h.CreateServer(ctx, ServerSpec{Name: "pgd-eu-1", Type: "cpx31", Location: "fsn1", Image: "ubuntu-24.04", UserData: "#cloud-config\n", Labels: map[string]string{"pgdock": "node"}})
	if err != nil || s.ID == "" || s.Type != "cpx31" || s.PrivateIP == "" {
		t.Fatalf("create: %+v %v", s, err)
	}
	if _, err := h.CreateServer(ctx, ServerSpec{Name: "pgd-eu-1", Type: "cpx31"}); err == nil {
		t.Error("a duplicate name was created")
	}
	list, err := h.ListServers(ctx, Filter{Labels: map[string]string{"pgdock": "node"}})
	if err != nil || len(list) != 1 || list[0].ID != s.ID {
		t.Fatalf("list: %+v %v", list, err)
	}
	v, err := h.CreateVolume(ctx, VolumeSpec{Name: "data", SizeGB: 100, Location: "fsn1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AttachVolume(ctx, v.ID, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.AssignFloatingIP(ctx, "77", s.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.DeleteServer(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.DeleteServer(ctx, s.ID); err != nil {
		t.Errorf("deleting a deleted server: %v", err)
	}
	if list, _ := h.ListServers(ctx, Filter{}); len(list) != 0 {
		t.Errorf("after delete: %+v", list)
	}
	// Spread placement groups (V3.1 §2.3): created once, found after.
	g, err := h.EnsurePlacementGroup(ctx, "pgdock-ng-lagos", map[string]string{"pgdock-region": "ng-lagos"})
	if err != nil || g.ID == "" || g.Servers != 0 {
		t.Fatalf("placement group: %+v %v", g, err)
	}
	if again, err := h.EnsurePlacementGroup(ctx, "pgdock-ng-lagos", nil); err != nil || again.ID != g.ID {
		t.Fatalf("placement group again: %+v %v", again, err)
	}
	for i := range SpreadGroupLimit {
		if _, err := h.CreateServer(ctx, ServerSpec{Name: fmt.Sprintf("ng-%d", i), Type: "cpx11", PlacementGroup: g.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if full, _ := h.EnsurePlacementGroup(ctx, "pgdock-ng-lagos", nil); full.Servers != SpreadGroupLimit {
		t.Fatalf("full group: %+v", full)
	}
	if _, err := h.CreateServer(ctx, ServerSpec{Name: "ng-over", Type: "cpx11", PlacementGroup: g.ID}); err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("an 11th server in a spread group: %v", err)
	}
	if _, err := (ManualProvider{}).EnsurePlacementGroup(ctx, "x", nil); !errors.Is(err, ErrManual) {
		t.Errorf("manual placement group: %v", err)
	}
	bad := &HetznerProvider{API: srv.URL, Token: "wrong"}
	if _, err := bad.ListServers(ctx, Filter{}); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("bad token: %v", err)
	}
}

func TestManualProvider(t *testing.T) {
	m := ManualProvider{Catalog: []ServerPrice{{Type: "colo-1u", Location: "lagos", CPUs: 32, MemoryGB: 128, DiskGB: 2000, MonthlyMinor: 45000000, Currency: "NGN"}}}
	if _, err := m.CreateServer(context.Background(), ServerSpec{}); !errors.Is(err, ErrManual) {
		t.Errorf("create: %v", err)
	}
	cat, _ := m.PriceCatalog(context.Background())
	if p, ok := Price(cat, "colo-1u", "lagos"); !ok || p.Currency != "NGN" {
		t.Errorf("price: %+v", p)
	}
}

func TestCloudInit(t *testing.T) {
	b := Bootstrap{
		NodeName: "pgd-eu-2", ServerURL: "https://pgdock.example.com", Token: "pgdreg_abc-DEF_123",
		AgentImage: "ghcr.io/acme/pgdock-agent:3.0.0", PGImage: "ghcr.io/acme/pgdock-postgres:{major}-walg3.0.9",
		PrivateCIDR: "10.0.0.0/16", SSHKeys: []string{"ssh-ed25519 AAAA ops"},
		ServerCA: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
	}
	doc, err := b.CloudInit()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc, "#cloud-config\n") {
		t.Fatal("not a cloud-config document")
	}
	var parsed struct {
		Hostname   string `yaml:"hostname"`
		Packages   []string
		WriteFiles []struct {
			Path, Permissions, Content string
		} `yaml:"write_files"`
		Runcmd [][]string
		Keys   []string `yaml:"ssh_authorized_keys"`
	}
	if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("YAML: %v\n%s", err, doc)
	}
	if parsed.Hostname != "pgd-eu-2" || len(parsed.Runcmd) != 1 || len(parsed.Keys) != 1 {
		t.Errorf("parsed: %+v", parsed)
	}
	files := map[string]string{}
	for _, f := range parsed.WriteFiles {
		files[f.Path] = f.Content
		if f.Path == "/etc/pgdock/agent.env" && f.Permissions != "0600" {
			t.Errorf("agent.env permissions %s", f.Permissions)
		}
	}
	env := files["/etc/pgdock/agent.env"]
	for _, want := range []string{"PGDOCK_AGENT_TOKEN=pgdreg_abc-DEF_123", "PGDOCK_AGENT_DB_ALLOW=10.0.0.0/16", "PGDOCK_AGENT_SERVER_CA=/etc/pgdock/server-ca.pem"} {
		if !strings.Contains(env, want) {
			t.Errorf("agent.env lacks %s:\n%s", want, env)
		}
	}
	if !strings.Contains(files["/etc/ssh/sshd_config.d/90-pgdock.conf"], "PasswordAuthentication no") {
		t.Error("SSH isn't hardened")
	}
	if !strings.Contains(files["/etc/pgdock/server-ca.pem"], "BEGIN CERTIFICATE") {
		t.Error("the CA isn't written")
	}
	if !strings.Contains(files["/usr/local/sbin/pgdock-join"], "ufw --force enable") {
		t.Error("no firewall")
	}
	if got := TokenFromCloudInit(doc); got != b.Token {
		t.Errorf("token back: %q", got)
	}

	for name, mut := range map[string]func(*Bootstrap){
		"token with a quote": func(b *Bootstrap) { b.Token = "x'; rm -rf /" },
		"image with a space": func(b *Bootstrap) { b.AgentImage = "a b" },
		"bad CIDR":           func(b *Bootstrap) { b.PrivateCIDR = "10.0.0.0; reboot" },
		"bad name":           func(b *Bootstrap) { b.NodeName = "Bad_Name" },
	} {
		c := b
		mut(&c)
		if _, err := c.CloudInit(); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}

// TestCloudInitEdge: an edge node runs pgdock-edge beside the agent, with
// its secret in a root-only file (V4.1 §11).
func TestCloudInitEdge(t *testing.T) {
	b := Bootstrap{
		NodeName: "pgd-ng-lagos-4", ServerURL: "https://pgdock.example.com", Token: "pgdreg_abc",
		AgentImage: "ghcr.io/acme/pgdock-agent:3.0.0", PGImage: "ghcr.io/acme/pgdock-postgres:{major}",
		PrivateCIDR: "10.0.0.0/16", ServerCA: "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
		Edge: &EdgeBootstrap{Image: "ghcr.io/acme/pgdock-edge:4.1.0", ControlURL: "https://pgdock.example.com",
			Secret: strings.Repeat("s", 40), Domain: "api.pgdock.ng", Region: "ng-lagos"},
	}
	doc, err := b.CloudInit()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		WriteFiles []struct {
			Path, Permissions, Content string
		} `yaml:"write_files"`
	}
	if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("YAML: %v\n%s", err, doc)
	}
	files, perms := map[string]string{}, map[string]string{}
	for _, f := range parsed.WriteFiles {
		files[f.Path], perms[f.Path] = f.Content, f.Permissions
	}
	env := files["/etc/pgdock/edge.env"]
	for _, want := range []string{"PGDOCK_EDGE_NAME=pgd-ng-lagos-4", "PGDOCK_EDGE_SECRET=" + strings.Repeat("s", 40),
		"PGDOCK_EDGE_DOMAIN=api.pgdock.ng", "PGDOCK_EDGE_REGION=ng-lagos", "PGDOCK_EDGE_CONTROL_URL=https://pgdock.example.com"} {
		if !strings.Contains(env, want) {
			t.Errorf("edge.env lacks %s:\n%s", want, env)
		}
	}
	if perms["/etc/pgdock/edge.env"] != "0600" {
		t.Errorf("edge.env permissions %s", perms["/etc/pgdock/edge.env"])
	}
	if !strings.Contains(files["/etc/pgdock/agent.env"], "PGDOCK_AGENT_SERVER_CA=") || strings.Contains(env, "AGENT") {
		t.Errorf("the env files are mixed up:\nagent: %s\nedge: %s", files["/etc/pgdock/agent.env"], env)
	}
	join := files["/usr/local/sbin/pgdock-join"]
	for _, want := range []string{"ufw allow 8443/tcp", "--name pgdock-edge", "--env-file /etc/pgdock/edge.env", "ghcr.io/acme/pgdock-edge:4.1.0", "--name pgdock-agent"} {
		if !strings.Contains(join, want) {
			t.Errorf("the join script lacks %s:\n%s", want, join)
		}
	}
	for name, mut := range map[string]func(*EdgeBootstrap){
		"short secret":  func(e *EdgeBootstrap) { e.Secret = "short" },
		"quoted secret": func(e *EdgeBootstrap) { e.Secret = strings.Repeat("s", 40) + "'" },
		"bad domain":    func(e *EdgeBootstrap) { e.Domain = "api pgdock" },
		"bad image":     func(e *EdgeBootstrap) { e.Image = "a b" },
	} {
		e := *b.Edge
		mut(&e)
		c := b
		c.Edge = &e
		if _, err := c.CloudInit(); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
	plain := b
	plain.Edge = nil
	if doc, _ := plain.CloudInit(); strings.Contains(doc, "pgdock-edge") {
		t.Error("a database node runs the edge")
	}
}
