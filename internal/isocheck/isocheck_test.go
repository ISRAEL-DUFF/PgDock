package isocheck

import "testing"

func TestHBAFinding(t *testing.T) {
	for _, c := range []struct {
		typ, addr, mask, method string
		ok                      bool
	}{
		{"local", "", "", "trust", true},
		{"host", "172.31.250.11", "255.255.255.255", "scram-sha-256", true},
		{"host", "10.0.0.0", "255.0.0.0", "scram-sha-256", true},
		{"host", "127.0.0.1", "255.255.255.255", "scram-sha-256", true},
		{"host", "::1", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "scram-sha-256", true},
		{"host", "samehost", "", "scram-sha-256", true},
		{"host", "all", "", "scram-sha-256", false},
		{"host", "samenet", "", "scram-sha-256", false},
		{"host", "0.0.0.0", "0.0.0.0", "scram-sha-256", false},
		{"host", "10.0.0.5", "255.255.255.255", "md5", false},
		{"host", "10.0.0.5", "255.255.255.255", "trust", false},
		{"host", "203.0.113.7", "255.255.255.255", "scram-sha-256", false},
		{"hostssl", "203.0.113.7", "255.255.255.255", "scram-sha-256", true},
		{"host", "0.0.0.0", "0.0.0.0", "reject", true},
		{"host", "db.example.com", "", "scram-sha-256", false},
	} {
		msg, ok := hbaFinding(c.typ, c.addr, c.mask, c.method, "")
		if ok != c.ok {
			t.Errorf("%s %s/%s %s: ok=%v (%s), want %v", c.typ, c.addr, c.mask, c.method, ok, msg, c.ok)
		}
	}
	if _, ok := hbaFinding("host", "10.0.0.1", "", "scram-sha-256", "syntax error"); ok {
		t.Error("an invalid rule passed")
	}
}
