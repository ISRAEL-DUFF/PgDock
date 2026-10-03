package nodes

import "testing"

func TestCompatible(t *testing.T) {
	for _, c := range []struct {
		server, agent string
		ok            bool
	}{
		{"v1.0.0", "v1.4.2", true},
		{"v1.0.0", "1.0.0", true},
		{"v1.0.0", "v2.0.0", false},
		{"v2.1.0", "v1.9.9", false},
		{"v1.3.0", "v1.2.9", false},
		{"v1.3.0", "v1.3.0", true},
		{"v1.3.1", "v1.3.0", true},
		{"v1.10.0", "v1.9.0", false},
		{"dev", "v3.0.0", true},
		{"v1.0.0", "dev", true},
		{"v1.0.0", "abc123", true},
	} {
		if got := Compatible(c.server, c.agent); got != c.ok {
			t.Errorf("Compatible(%q, %q) = %v, want %v", c.server, c.agent, got, c.ok)
		}
	}
}
