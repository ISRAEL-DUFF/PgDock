package signature

import (
	"strings"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"id":"evt_1"}`)
	one := Sign("whsec_a", now, body)
	if strings.Count(one, "v1=") != 1 {
		t.Fatalf("one secret: %s", one)
	}
	if err := Verify("whsec_a", one, body, now, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	// During a rotation's overlap: either secret verifies, another doesn't.
	both := SignAll([]string{"whsec_new", "whsec_old"}, now, body)
	if !strings.HasPrefix(both, first(one)) || strings.Count(both, "v1=") != 2 {
		t.Fatalf("two secrets: %s", both)
	}
	for _, s := range []string{"whsec_new", "whsec_old"} {
		if err := Verify(s, both, body, now, 5*time.Minute); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if Verify("whsec_other", both, body, now, 5*time.Minute) == nil {
		t.Fatal("another secret verified")
	}
	if Verify("whsec_new", both, []byte(`{"id":"evt_2"}`), now, 5*time.Minute) == nil {
		t.Fatal("a changed body verified")
	}
	if Verify("whsec_new", both, body, now.Add(6*time.Minute), 5*time.Minute) == nil {
		t.Fatal("an old timestamp verified")
	}
	if Verify("whsec_a", "t=1800000000", body, now, 5*time.Minute) == nil {
		t.Fatal("a header without v1 verified")
	}
}

func first(header string) string {
	t, _, _ := strings.Cut(header, ",")
	return t
}
