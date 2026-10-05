package dedicated

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/store"
)

// Availability is an HA project's measured availability over a calendar
// month (V3 §2.7): minutes in which the pooler endpoint failed to accept a
// connection and run a query from every vantage point that probed it.
type Availability struct {
	Month       string // YYYY-MM, UTC
	Measured    int
	Unavailable int
	// Percent is nil until something was measured.
	Percent *float64
	Recent  []store.AvailabilityMinute
}

// Availability reports the month containing now.
func (s *Service) Availability(ctx context.Context, projectID uuid.UUID, now time.Time) (Availability, error) {
	now = now.UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	q := store.New(s.db)
	sum, err := q.AvailabilitySummary(ctx, store.AvailabilitySummaryParams{ProjectID: projectID, FromTs: from, ToTs: to})
	if err != nil {
		return Availability{}, err
	}
	out := Availability{Month: from.Format("2006-01"), Measured: int(sum.Measured), Unavailable: int(sum.Unavailable)}
	if sum.Measured > 0 {
		p := 100 * float64(sum.Measured-sum.Unavailable) / float64(sum.Measured)
		out.Percent = &p
	}
	out.Recent, err = q.RecentOutageMinutes(ctx, store.RecentOutageMinutesParams{ProjectID: projectID, Since: from, Lim: 60})
	return out, err
}
