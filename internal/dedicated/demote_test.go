package dedicated_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/store"
)

func TestDemotedSettings(t *testing.T) {
	cur := store.DefaultDedicatedSettings(20)
	next := dedicated.DemotedSettings(cur, 0, false)
	want := store.DefaultSharedSettings()
	want.ConsoleReadOnly = true // kept unless asked (V2 §5.5)
	if next != want {
		t.Fatalf("demoted settings: %+v, want %+v", next, want)
	}
	if got := dedicated.DemotedSettings(cur, 0, true); got.ConsoleReadOnly {
		t.Fatal("console_writable did not turn the read-only console off")
	}
	if got := dedicated.DemotedSettings(cur, 10, false); got.ConnectionLimit != 10 {
		t.Fatalf("the plan's connection limit is not applied: %d", got.ConnectionLimit)
	}
	r := dedicated.Resets(cur, next)
	for _, w := range []string{"connection limit 90 → 20", "pool size 20 → 5", "statement timeout off → 60s", "idle-in-transaction timeout off → 60s"} {
		if !slices.Contains(r, w) {
			t.Errorf("resets lack %q: %v", w, r)
		}
	}
	if slices.ContainsFunc(r, func(s string) bool { return strings.HasPrefix(s, "SQL console") }) {
		t.Errorf("an unchanged console is listed: %v", r)
	}
}
