package schedjobs

import (
	"testing"
	"time"
)

func TestParseAndNext(t *testing.T) {
	utc := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		expr, tz, from, want string
	}{
		{"*/5 * * * *", "UTC", "2026-10-03 12:03", "2026-10-03 12:05"},
		{"0 3 * * *", "UTC", "2026-10-03 12:03", "2026-10-04 03:00"},
		{"30 9 * * mon-fri", "UTC", "2026-10-03 12:00", "2026-10-05 09:30"}, // Saturday → Monday
		{"0 0 1 * *", "UTC", "2026-10-03 00:00", "2026-11-01 00:00"},
		{"@hourly", "UTC", "2026-10-03 12:00", "2026-10-03 13:00"},
		{"0 9 * * *", "Europe/Berlin", "2026-10-03 06:00", "2026-10-03 07:00"}, // 09:00 CEST
		{"0 0 13 * 5", "UTC", "2026-10-03 00:00", "2026-10-09 00:00"},          // Friday or the 13th
		{"5/20 * * * *", "UTC", "2026-10-03 12:06", "2026-10-03 12:25"},
		{"0 12 * * 7", "UTC", "2026-10-03 13:00", "2026-10-04 12:00"}, // 7 = Sunday
	} {
		s, err := Parse(c.expr, c.tz)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := s.Next(utc(c.from)).UTC(); !got.Equal(utc(c.want)) {
			t.Errorf("%s in %s after %s: %s, want %s", c.expr, c.tz, c.from, got.Format("2006-01-02 15:04"), c.want)
		}
	}
	for _, bad := range []string{"* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "a b c d e", "5-1 * * * *"} {
		if _, err := Parse(bad, "UTC"); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	if _, err := Parse("* * * * *", "Mars/Olympus"); err == nil {
		t.Error("an unknown time zone parsed")
	}
	if s, _ := Parse("0 0 30 2 *", "UTC"); !s.Next(utc("2026-01-01 00:00")).IsZero() {
		t.Error("30 February has a next run")
	}
}

func TestMinGap(t *testing.T) {
	from := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	for expr, want := range map[string]time.Duration{
		"* * * * *": time.Minute, "*/5 * * * *": 5 * time.Minute, "0 * * * *": time.Hour,
		"0,1 * * * *": time.Minute, "0 3 * * *": 24 * time.Hour,
	} {
		s, err := Parse(expr, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if got := s.MinGap(from); got != want {
			t.Errorf("%s: %s, want %s", expr, got, want)
		}
	}
}
