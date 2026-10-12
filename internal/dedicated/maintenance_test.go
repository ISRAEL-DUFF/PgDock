package dedicated_test

import (
	"context"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/dedicated"
)

func TestMaintenanceWindow(t *testing.T) {
	// Saturday 22:00 for 6 hours: it runs into Sunday.
	w := dedicated.MaintenanceWindow{Enabled: true, Weekday: time.Saturday, StartHour: 22, Hours: 6}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		now  string
		in   bool
		next string
	}{
		{"2026-10-03T21:59:00Z", false, "2026-10-03T22:00:00Z"}, // Saturday, just before
		{"2026-10-03T22:00:00Z", true, "2026-10-03T22:00:00Z"},
		{"2026-10-04T03:59:00Z", true, "2026-10-03T22:00:00Z"}, // Sunday morning, still in
		{"2026-10-04T04:00:00Z", false, "2026-10-10T22:00:00Z"},
		{"2026-10-07T12:00:00Z", false, "2026-10-10T22:00:00Z"},      // midweek
		{"2026-10-03T23:30:00+02:00", false, "2026-10-03T22:00:00Z"}, // 21:30 UTC
	} {
		now := at(c.now)
		if got := w.Contains(now); got != c.in {
			t.Errorf("Contains(%s) = %v, want %v", c.now, got, c.in)
		}
		if got := w.Next(now); !got.Equal(at(c.next)) {
			t.Errorf("Next(%s) = %s, want %s", c.now, got.Format(time.RFC3339), c.next)
		}
	}
	w.Enabled = false
	if w.Contains(at("2026-10-03T23:00:00Z")) {
		t.Error("a disabled window contains nothing")
	}
	for _, bad := range []dedicated.MaintenanceWindow{{Weekday: 7, Hours: 1}, {StartHour: 24, Hours: 1}, {Hours: 0}, {Hours: 25}} {
		if bad.Validate() == nil {
			t.Errorf("%+v validated", bad)
		}
	}
}

func TestNewerRelease(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"18.0", "18.1", true},
		{"18.9", "18.10", true},
		{"18.1", "18.1", false},
		{"18.2", "18.1", false},
		{"17.6", "18.0", false}, // another major is an upgrade, not a restart
		{"", "18.1", false},
		{"18", "18.1", false},
	} {
		if got := dedicated.NewerRelease(c.a, c.b); got != c.want {
			t.Errorf("NewerRelease(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestMaintenanceWindowSetting(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	w, err := e.svc.MaintenanceWindow(ctx)
	if err != nil || w != dedicated.DefaultWindow {
		t.Fatalf("unset window = %+v, %v; want the default", w, err)
	}
	want := dedicated.MaintenanceWindow{Enabled: false, Weekday: time.Wednesday, StartHour: 1, Hours: 2}
	if err := e.svc.SetMaintenanceWindow(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := e.svc.MaintenanceWindow(ctx); err != nil || got != want {
		t.Fatalf("window = %+v, %v; want %+v", got, err, want)
	}
	if err := e.svc.SetMaintenanceWindow(ctx, dedicated.MaintenanceWindow{Hours: 30}); err == nil {
		t.Fatal("an invalid window was stored")
	}
}
