package incidents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/mail"
	"github.com/israel-duff/pgdock/internal/store"
)

// Announced maintenance (V3.1 §4): a status page incident of severity
// maintenance with a scheduled window, a region (or all), and optionally
// the projects or nodes it covers. HA projects' availability minutes
// inside it are excluded from the SLA when it was announced at least
// NoticeHours before them.

// NoticeHours is the notice the SLA requires (V3 §2.7).
const NoticeHours = 72

// Mailer sends the announcement emails.
type Mailer interface {
	Send(ctx context.Context, m mail.Message) error
}

// SetMailer enables announcement emails; publicURL links to the dashboard.
func (s *Service) SetMailer(m Mailer, publicURL string) { s.mailer, s.publicURL = m, publicURL }

// Maintenance is an announcement to make.
type Maintenance struct {
	Title    string
	Body     string
	Region   string // "" for every region
	Start    time.Time
	End      time.Time
	Projects []uuid.UUID
	Nodes    []uuid.UUID
	By       *uuid.UUID
	// Replaces is an earlier announcement this one supersedes (it is
	// cancelled): a window is never edited after it is announced.
	Replaces *uuid.UUID
	// Now is the time of the announcement (time.Now when zero).
	Now time.Time
}

// Announced is the announcement made, and whether the SLA will exclude
// all of it.
type Announced struct {
	Incident
	Scope []store.IncidentScope
	// ShortNotice: the window starts sooner than NoticeHours from now, so
	// the minutes before ExcludedFrom aren't excluded.
	ShortNotice  bool
	ExcludedFrom time.Time
	Emailed      int
}

// Announce records a maintenance window, posts it to the status page as
// upcoming, and emails the owners and admins of the organisations it
// covers. The announcement time is fixed once made.
func (s *Service) Announce(ctx context.Context, in Maintenance) (Announced, error) {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	in.Start, in.End = in.Start.UTC().Truncate(time.Minute), in.End.UTC().Truncate(time.Minute)
	switch {
	case !in.Start.After(now):
		return Announced{}, fmt.Errorf("%w: the window must start in the future", ErrInvalid)
	case !in.End.After(in.Start):
		return Announced{}, fmt.Errorf("%w: the window must end after it starts", ErrInvalid)
	case in.End.Sub(in.Start) > 24*time.Hour:
		return Announced{}, fmt.Errorf("%w: a maintenance window is at most 24 hours", ErrInvalid)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Scheduled maintenance"
	}
	where := "all regions"
	if in.Region != "" {
		where = in.Region
	}
	body := strings.TrimSpace(in.Body)
	if body == "" {
		body = "Planned maintenance; affected databases may pause briefly while it runs."
	}
	body = fmt.Sprintf("Scheduled for %s to %s UTC (%s). %s", in.Start.Format("Mon 2 Jan 15:04"), in.End.Format("15:04"), where, body)
	components := []string{ComponentDedicated}
	if err := s.check(title, components, "maintenance", "identified"); err != nil {
		return Announced{}, err
	}
	if err := checkBody(body); err != nil {
		return Announced{}, err
	}
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		if in.Replaces != nil {
			if _, err := q.CancelMaintenance(ctx, *in.Replaces); errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: the announcement to replace isn't an open maintenance window", ErrInvalid)
			} else if err != nil {
				return err
			}
			if _, err := q.InsertIncidentUpdate(ctx, store.InsertIncidentUpdateParams{IncidentID: *in.Replaces, Status: "resolved",
				Body: "Rescheduled: see the new announcement.", PostedBy: in.By}); err != nil {
				return err
			}
		}
		inc, err := q.InsertMaintenance(ctx, store.InsertMaintenanceParams{Title: title, Components: components, RegionID: nonEmpty(in.Region),
			CreatedBy: in.By, ScheduledStart: in.Start, ScheduledEnd: &in.End, Replaces: in.Replaces})
		if err != nil {
			return err
		}
		id = inc.ID
		for _, p := range in.Projects {
			pid := p
			if err := q.InsertIncidentScope(ctx, store.InsertIncidentScopeParams{IncidentID: id, ProjectID: &pid}); err != nil {
				return err
			}
		}
		for _, n := range in.Nodes {
			nid := n
			if err := q.InsertIncidentScope(ctx, store.InsertIncidentScopeParams{IncidentID: id, NodeID: &nid}); err != nil {
				return err
			}
		}
		_, err = q.InsertIncidentUpdate(ctx, store.InsertIncidentUpdateParams{IncidentID: id, Status: "identified", Body: body, PostedBy: in.By})
		return err
	})
	if err != nil {
		return Announced{}, err
	}
	out := Announced{}
	if out.Incident, err = s.Get(ctx, id); err != nil {
		return out, err
	}
	q := store.New(s.db)
	if out.Scope, err = q.IncidentScope(ctx, id); err != nil {
		return out, err
	}
	announced := *out.AnnouncedAt
	out.ExcludedFrom = announced.Add(NoticeHours * time.Hour).Truncate(time.Minute)
	if out.ExcludedFrom.Before(in.Start) {
		out.ExcludedFrom = in.Start
	}
	out.ShortNotice = out.ExcludedFrom.After(in.Start)
	out.Emailed = s.emailMaintenance(ctx, out, body)
	return out, nil
}

func (s *Service) emailMaintenance(ctx context.Context, a Announced, body string) int {
	if s.mailer == nil {
		return 0
	}
	to, err := store.New(s.db).MaintenanceOrgEmails(ctx, a.ID)
	if err != nil || len(to) == 0 {
		if err != nil {
			s.log.Warn("maintenance emails", "incident", a.ID, "err", err)
		}
		return 0
	}
	text := body + "\n\nYour databases keep their data and connection strings. HA projects switch over to their standby instead of restarting.\n"
	if s.cfg.URL != "" {
		text += "\nStatus: " + s.cfg.URL + "\n"
	}
	if s.publicURL != "" {
		text += "Dashboard: " + strings.TrimRight(s.publicURL, "/") + "\n"
	}
	// One message per recipient: organisations don't see each other.
	sent := 0
	for _, addr := range to {
		if err := s.mailer.Send(ctx, mail.Message{To: []string{addr}, Subject: "[PGDock] " + a.Title + ": " + a.ScheduledStart.UTC().Format("Mon 2 Jan 15:04") + " UTC",
			Body: text, Headers: map[string]string{"X-PGDock-Event": "maintenance"}}); err != nil {
			s.log.Warn("maintenance email", "incident", a.ID, "err", err)
			continue
		}
		sent++
	}
	return sent
}

// CancelMaintenance calls off an announcement that hasn't ended. Minutes
// after it are no longer excluded.
func (s *Service) CancelMaintenance(ctx context.Context, id uuid.UUID, by *uuid.UUID) (Incident, error) {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.CancelMaintenance(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: not an open maintenance window", ErrNotFound)
		} else if err != nil {
			return err
		}
		_, err := q.InsertIncidentUpdate(ctx, store.InsertIncidentUpdateParams{IncidentID: id, Status: "resolved", Body: "Cancelled.", PostedBy: by})
		return err
	})
	if err != nil {
		return Incident{}, err
	}
	return s.Get(ctx, id)
}

// ListMaintenance is the announcements whose window ended after since.
func (s *Service) ListMaintenance(ctx context.Context, since time.Time) ([]Incident, error) {
	rows, err := store.New(s.db).ListMaintenance(ctx, store.ListMaintenanceParams{Since: since, Lim: 100})
	if err != nil {
		return nil, err
	}
	out := make([]Incident, 0, len(rows))
	for _, r := range rows {
		inc, err := s.Get(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, nil
}

// EndMaintenance resolves announcements whose window has passed, with an
// update, so the status page moves them to the past.
func (s *Service) EndMaintenance(ctx context.Context, now time.Time) error {
	q := store.New(s.db)
	done, err := q.MaintenanceDone(ctx, now)
	if err != nil {
		return err
	}
	for _, d := range done {
		if _, err := q.InsertIncidentUpdate(ctx, store.InsertIncidentUpdateParams{IncidentID: d.ID, Status: "resolved", Body: "Maintenance completed."}); err != nil {
			return err
		}
	}
	return nil
}
