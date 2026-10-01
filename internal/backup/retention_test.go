package backup

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRetentionKeepsSevenDailyFourWeekly(t *testing.T) {
	now := time.Date(2026, 10, 1, 3, 30, 0, 0, time.UTC)
	var items []Item
	byID := map[uuid.UUID]time.Time{}
	// 60 nightly backups, plus a second backup on the most recent day.
	for d := 0; d < 60; d++ {
		it := Item{ID: uuid.New(), FinishedAt: now.AddDate(0, 0, -d)}
		items = append(items, it)
		byID[it.ID] = it.FinishedAt
	}
	extra := Item{ID: uuid.New(), FinishedAt: now.Add(-time.Hour)}
	items = append(items, extra)
	byID[extra.ID] = extra.FinishedAt

	expired := DefaultRetention.Expired(items)
	dropped := map[uuid.UUID]bool{}
	for _, id := range expired {
		dropped[id] = true
	}
	var kept []time.Time
	for _, it := range items {
		if !dropped[it.ID] {
			kept = append(kept, it.FinishedAt)
		}
	}
	if len(kept) != 11 {
		t.Fatalf("kept %d backups, want 7 daily + 4 weekly", len(kept))
	}
	// The 7 newest days are all kept (newest of the day).
	for d := 0; d < 7; d++ {
		if dropped[items[d].ID] {
			t.Errorf("daily backup from %d days ago dropped", d)
		}
	}
	if !dropped[extra.ID] {
		t.Error("an older same-day backup was kept instead of only the newest")
	}
	// Weekly ones are older than the daily window and in distinct ISO weeks.
	weeks := map[int]bool{}
	for _, k := range kept {
		if now.Sub(k) >= 7*24*time.Hour {
			_, w := k.ISOWeek()
			if weeks[w] {
				t.Errorf("two weekly backups in ISO week %d", w)
			}
			weeks[w] = true
		}
	}
	if len(weeks) != 4 {
		t.Fatalf("%d weekly backups, want 4", len(weeks))
	}
}

func TestRetentionFewBackups(t *testing.T) {
	now := time.Now()
	items := []Item{{ID: uuid.New(), FinishedAt: now}, {ID: uuid.New(), FinishedAt: now.Add(-time.Minute)}}
	if got := DefaultRetention.Expired(items); len(got) != 1 || got[0] != items[1].ID {
		t.Fatalf("two same-day backups: dropped %v", got)
	}
	if got := DefaultRetention.Expired(nil); len(got) != 0 {
		t.Fatal("nothing to drop")
	}
	if got := (Retention{}).Expired(items); len(got) != 1 {
		t.Fatalf("the newest backup must always survive: %v", got)
	}
}
