package settings

import (
	"context"
	"testing"
)

func TestValidateHost(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"db.example.com", "db.example.com"},
		{" DB.Example.COM.", "db.example.com"},
		{"10.0.0.5", "10.0.0.5"},
		{"localhost", "localhost"},
	} {
		if got, err := ValidateHost(c.in); err != nil || got != c.want {
			t.Errorf("ValidateHost(%q) = %q, %v", c.in, got, err)
		}
	}
	for _, in := range []string{"", "db example.com", "-db.example.com", "db..example.com", "db.example.com:5432", "x/y"} {
		if _, err := ValidateHost(in); err == nil {
			t.Errorf("ValidateHost(%q) accepted", in)
		}
	}
}

func TestCheckDNSLoopback(t *testing.T) {
	c := CheckDNS(context.Background(), "127.0.0.1", "", nil)
	if !c.PointsHere || len(c.Addresses) != 1 {
		t.Fatalf("%+v", c)
	}
	// An address that is certainly not this machine.
	c = CheckDNS(context.Background(), "192.0.2.1", "", nil)
	if c.PointsHere {
		t.Fatalf("TEST-NET address reported as local: %+v", c)
	}
}
