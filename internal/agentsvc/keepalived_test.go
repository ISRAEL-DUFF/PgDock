package agentsvc

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validKeepalived() KeepalivedConfig {
	return KeepalivedConfig{
		Interface: "eth0", SelfIP: "10.0.0.11", PeerIP: "10.0.0.12", VIP: "203.0.113.10",
		RouterID: 51, Priority: 100, Password: "s3cr3t",
		CheckURL: "http://127.0.0.1:7071/ready", NotifyURL: "http://127.0.0.1:7071",
	}
}

func TestRenderKeepalived(t *testing.T) {
	out, err := RenderKeepalived(validKeepalived())
	if err != nil {
		t.Fatal(err)
	}
	conf := string(out)
	for _, want := range []string{
		"unicast_src_ip 10.0.0.11", "    10.0.0.12\n", "virtual_router_id 51", "priority 100", "nopreempt",
		"203.0.113.10/32 dev eth0", "http://127.0.0.1:7071/ready", "http://127.0.0.1:7071/vrrp/MASTER",
		`auth_pass "s3cr3t"`, "fall 2",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("keepalived.conf lacks %q:\n%s", want, conf)
		}
	}

	noVIP := validKeepalived()
	noVIP.VIP = ""
	out, err = RenderKeepalived(noVIP)
	if err != nil || strings.Contains(string(out), "virtual_ipaddress") {
		t.Fatalf("without a VIP: %v\n%s", err, out)
	}

	bad := map[string]func(*KeepalivedConfig){
		"interface injection": func(c *KeepalivedConfig) { c.Interface = "eth0 }\nglobal_defs {" },
		"self is not an IP":   func(c *KeepalivedConfig) { c.SelfIP = "host-a" },
		"same peer":           func(c *KeepalivedConfig) { c.PeerIP = c.SelfIP },
		"router ID":           func(c *KeepalivedConfig) { c.RouterID = 0 },
		"priority":            func(c *KeepalivedConfig) { c.Priority = 255 },
		"long password":       func(c *KeepalivedConfig) { c.Password = "123456789" },
		"quoted password":     func(c *KeepalivedConfig) { c.Password = `a"b` },
		"public check URL":    func(c *KeepalivedConfig) { c.CheckURL = "http://10.0.0.11:7071/ready" },
		"bad VIP":             func(c *KeepalivedConfig) { c.VIP = "nope" },
	}
	for name, mutate := range bad {
		c := validKeepalived()
		mutate(&c)
		if _, err := RenderKeepalived(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSeedPoolerDir(t *testing.T) {
	dir := t.TempDir()
	if err := SeedPoolerDir(dir, 0o640); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "databases.ini")); string(b) != "[databases]\n" {
		t.Fatalf("routes: %q", b)
	}
	if _, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")); err != nil {
		t.Fatalf("placeholder certificate: %v", err)
	}
	// A real configuration already there is kept.
	if err := os.WriteFile(filepath.Join(dir, "databases.ini"), []byte("[databases]\nreal = host=x\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SeedPoolerDir(dir, 0o640); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "databases.ini")); !strings.Contains(string(b), "real") {
		t.Fatal("seeding overwrote the configuration")
	}
}
