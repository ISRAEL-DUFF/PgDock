package backup

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Retention is how many daily and weekly backups to keep (spec §4.3: 7
// daily + 4 weekly for the shared tier).
type Retention struct {
	Daily  int
	Weekly int
}

// DefaultRetention is the shared-tier policy.
var DefaultRetention = Retention{Daily: 7, Weekly: 4}

// Item is a succeeded backup, for retention.
type Item struct {
	ID         uuid.UUID
	FinishedAt time.Time
}

// Expired returns the backups the policy drops. It keeps the newest backup
// of each of the Daily most recent days that have one, then the newest of
// each of the next Weekly ISO weeks older than those days. The newest
// backup is always kept. Dates are UTC.
func (r Retention) Expired(items []Item) []uuid.UUID {
	// Newest first.
	sorted := append([]Item(nil), items...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].FinishedAt.After(sorted[j-1].FinishedAt); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	keep := map[uuid.UUID]bool{}
	days := map[string]bool{}
	weeks := map[string]bool{}
	var oldestKeptDay time.Time
	for _, it := range sorted {
		d := it.FinishedAt.UTC().Format("2006-01-02")
		if len(days) < r.Daily && !days[d] {
			days[d] = true
			keep[it.ID] = true
			oldestKeptDay = it.FinishedAt
		}
	}
	for _, it := range sorted {
		if keep[it.ID] || len(weeks) >= r.Weekly {
			continue
		}
		// Weekly slots start below the daily window.
		if !oldestKeptDay.IsZero() && !it.FinishedAt.Before(dayStart(oldestKeptDay)) {
			continue
		}
		y, w := it.FinishedAt.UTC().ISOWeek()
		wk := fmt.Sprintf("%d-W%02d", y, w)
		if !weeks[wk] {
			weeks[wk] = true
			keep[it.ID] = true
		}
	}
	if len(sorted) > 0 {
		keep[sorted[0].ID] = true
	}
	var out []uuid.UUID
	for _, it := range sorted {
		if !keep[it.ID] {
			out = append(out, it.ID)
		}
	}
	return out
}

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
