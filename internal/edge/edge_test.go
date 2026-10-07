package edge

import (
	"testing"
	"time"
)

func TestRefFromHost(t *testing.T) {
	e := New(Config{Domain: "api.pgdock.ng."})
	for host, want := range map[string]string{
		"k7f3m2q9.api.pgdock.ng":      "k7f3m2q9",
		"K7F3M2Q9.API.pgdock.ng:443":  "k7f3m2q9",
		"k7f3m2q9.api.pgdock.ng.":     "k7f3m2q9",
		"a.k7f3m2q9.api.pgdock.ng":    "",
		"api.pgdock.ng":               "",
		"k7f3m2q9.api.pgdock.ng.evil": "",
		"k7f3m2q9.xapi.pgdock.ng":     "",
	} {
		got, ok := e.refFromHost(host)
		if ok != (want != "") || got != want {
			t.Errorf("%s: got %q %v, want %q", host, got, ok, want)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter()
	now := time.Unix(1_800_000_000, 0)
	l.now = func() time.Time { return now }
	allowed := 0
	for range 20 {
		if l.allow("k", 60) { // 1 a second, burst 10
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("burst: %d", allowed)
	}
	now = now.Add(3 * time.Second)
	allowed = 0
	for range 5 {
		if l.allow("k", 60) {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("after 3 s: %d", allowed)
	}
	if !l.allow("other", 60) {
		t.Fatal("keys share a bucket")
	}
}
