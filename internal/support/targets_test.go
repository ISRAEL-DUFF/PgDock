package support

import (
	"testing"
	"time"
)

func TestResponseTargets(t *testing.T) {
	wat := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, WAT)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		plan, priority, opened string
		want                   string // "" means best effort
	}{
		{"free", PriorityNormal, "2026-10-05 10:00", ""},
		{"free", PriorityUrgent, "2026-10-05 10:00", ""},
		// Team: four business hours, across the evening.
		{"team", PriorityNormal, "2026-10-05 10:00", "2026-10-05 14:00"},
		{"team", PriorityNormal, "2026-10-05 15:30", "2026-10-06 11:30"},
		// Opened at night or on a weekend: the clock starts at 09:00.
		{"team", PriorityHigh, "2026-10-05 22:00", "2026-10-06 13:00"},
		{"team", PriorityNormal, "2026-10-10 11:00", "2026-10-12 13:00"}, // Saturday
		// Pro: a business day (8 hours).
		{"pro", PriorityNormal, "2026-10-09 16:00", "2026-10-12 16:00"}, // Friday → Monday
		// Urgent on a paid plan: an hour, any time.
		{"pro", PriorityUrgent, "2026-10-10 03:00", "2026-10-10 04:00"},
	}
	for _, c := range cases {
		got := TargetFor(c.plan, c.priority).RespondBy(wat(c.opened))
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s %s %s: %v, want best effort", c.plan, c.priority, c.opened, got.In(WAT))
		case c.want != "" && (got == nil || !got.Equal(wat(c.want))):
			t.Errorf("%s %s %s: %v, want %s", c.plan, c.priority, c.opened, got, c.want)
		}
	}
}
