package support

import "time"

// WAT is West Africa Time (UTC+1, no daylight saving): support's business
// hours are Nigerian business hours (V3 §7.1).
var WAT = time.FixedZone("WAT", 60*60)

// Business hours: 09:00 to 17:00 WAT, Monday to Friday. Public holidays
// aren't counted.
const (
	dayStart = 9
	dayEnd   = 17
)

// Target is a plan's response target (V3 §7.1).
type Target struct {
	// BusinessHours of business time to the first response; zero means
	// best effort (no target).
	BusinessHours int
	// Anytime is a target in calendar time (urgent tickets on paid plans).
	Anytime time.Duration
}

// TargetFor is the response target for a ticket of priority from an org
// on plan: Free is best effort, Pro one business day, Team four business
// hours, and an urgent ticket on a paid plan (an HA incident) an hour, at
// any time.
func TargetFor(plan, priority string) Target {
	paid := plan == "pro" || plan == "team"
	switch {
	case paid && priority == PriorityUrgent:
		return Target{Anytime: time.Hour}
	case plan == "team":
		return Target{BusinessHours: 4}
	case plan == "pro":
		return Target{BusinessHours: dayEnd - dayStart} // one business day
	}
	return Target{}
}

// RespondBy is when a ticket opened at opened must first be answered, or
// nil for best effort.
func (t Target) RespondBy(opened time.Time) *time.Time {
	switch {
	case t.Anytime > 0:
		at := opened.Add(t.Anytime)
		return &at
	case t.BusinessHours > 0:
		at := addBusinessTime(opened, time.Duration(t.BusinessHours)*time.Hour)
		return &at
	}
	return nil
}

// addBusinessTime adds d of business time to t.
func addBusinessTime(t time.Time, d time.Duration) time.Time {
	cur := t.In(WAT)
	for d > 0 {
		cur = nextBusinessMoment(cur)
		end := time.Date(cur.Year(), cur.Month(), cur.Day(), dayEnd, 0, 0, 0, WAT)
		left := end.Sub(cur)
		if left >= d {
			return cur.Add(d).UTC()
		}
		d -= left
		cur = end
	}
	return cur.UTC()
}

// nextBusinessMoment is t, or the next moment business hours are open.
func nextBusinessMoment(t time.Time) time.Time {
	for {
		start := time.Date(t.Year(), t.Month(), t.Day(), dayStart, 0, 0, 0, WAT)
		end := time.Date(t.Year(), t.Month(), t.Day(), dayEnd, 0, 0, 0, WAT)
		weekday := t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
		switch {
		case weekday && t.Before(start):
			return start
		case weekday && t.Before(end):
			return t
		}
		t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, WAT)
	}
}
