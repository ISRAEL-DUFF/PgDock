package schedjobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed 5-field cron expression (minute hour day-of-month
// month day-of-week) in a time zone.
type Schedule struct {
	minute, hour, dom, month, dow uint64 // bit sets
	domStar, dowStar              bool
	loc                           *time.Location
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// Parse reads a cron expression in time zone tz (an IANA name).
func Parse(expr, tz string) (Schedule, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		return Schedule{}, fmt.Errorf("unknown time zone %q", tz)
	}
	expr = strings.TrimSpace(strings.ToLower(expr))
	if m, ok := macros[expr]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("a cron expression has 5 fields (minute hour day month weekday), not %d", len(f))
	}
	s := Schedule{loc: loc, domStar: f[2] == "*" || f[2] == "?", dowStar: f[4] == "*" || f[4] == "?"}
	specs := []struct {
		dst      *uint64
		min, max int
		names    map[string]int
		what     string
	}{
		{&s.minute, 0, 59, nil, "minute"}, {&s.hour, 0, 23, nil, "hour"}, {&s.dom, 1, 31, nil, "day of month"},
		{&s.month, 1, 12, monthNames, "month"}, {&s.dow, 0, 7, dayNames, "day of week"},
	}
	for i, sp := range specs {
		bits, err := field(f[i], sp.min, sp.max, sp.names)
		if err != nil {
			return Schedule{}, fmt.Errorf("%s: %w", sp.what, err)
		}
		*sp.dst = bits
	}
	if s.dow&(1<<7) != 0 { // 7 is Sunday too
		s.dow |= 1
	}
	return s, nil
}

func field(f string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(f, ",") {
		step := 1
		if r, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step %q", st)
			}
			part, step = r, n
		}
		a, b := lo, hi
		switch {
		case part == "*" || part == "?":
		case strings.Contains(part, "-"):
			x, y, _ := strings.Cut(part, "-")
			var err error
			if a, err = value(x, names); err != nil {
				return 0, err
			}
			if b, err = value(y, names); err != nil {
				return 0, err
			}
		default:
			v, err := value(part, names)
			if err != nil {
				return 0, err
			}
			a, b = v, v
			if step > 1 { // "5/15" means from 5 to the end
				b = hi
			}
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func value(s string, names map[string]int) (int, error) {
	if v, ok := names[s]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return v, nil
}

func (s Schedule) matchDay(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	default: // both restricted: either matches (cron semantics)
		return dom || dow
	}
}

// Next is the first run strictly after t, or the zero time if there is
// none within five years (e.g. 30 February).
func (s Schedule) Next(t time.Time) time.Time {
	t = t.In(s.loc).Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(5, 0, 0)
	for t.Before(end) {
		if s.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, s.loc)
			continue
		}
		if !s.matchDay(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, s.loc)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, s.loc)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// Upcoming lists the next n runs after t.
func (s Schedule) Upcoming(t time.Time, n int) []time.Time {
	var out []time.Time
	for range n {
		t = s.Next(t)
		if t.IsZero() {
			break
		}
		out = append(out, t)
	}
	return out
}

// MinGap is the shortest time between two runs over the next 1,000 runs
// (bounded at a year): the interval the plan's minimum is checked against.
func (s Schedule) MinGap(from time.Time) time.Duration {
	prev := s.Next(from)
	if prev.IsZero() {
		return 0
	}
	gap := time.Duration(1<<63 - 1)
	end := prev.AddDate(1, 0, 0)
	for range 1000 {
		next := s.Next(prev)
		if next.IsZero() || next.After(end) {
			break
		}
		gap = min(gap, next.Sub(prev))
		prev = next
	}
	return gap
}
