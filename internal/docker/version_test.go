package docker

import "testing"

func TestPickAPIVersion(t *testing.T) {
	for _, c := range []struct{ min, max, want string }{
		{"1.24", "1.43", "v1.43"}, // Docker 24: the pinned version is in range
		{"", "", "v1.43"},         // daemon didn't say
		{"1.44", "1.52", "v1.44"}, // Docker 29: its minimum
		{"1.43", "1.52", "v1.43"},
	} {
		got, err := pickAPIVersion(c.min, c.max)
		if err != nil || got != c.want {
			t.Errorf("pickAPIVersion(%q, %q) = %q, %v; want %q", c.min, c.max, got, err, c.want)
		}
	}
	if _, err := pickAPIVersion("1.60", "1.52"); err == nil {
		t.Error("an inverted range should fail")
	}
}

func TestCompareVersions(t *testing.T) {
	if compareVersions("1.9", "1.43") >= 0 || compareVersions("1.43", "1.43") != 0 || compareVersions("1.52", "1.44") <= 0 {
		t.Error("versions compare numerically per component")
	}
}
