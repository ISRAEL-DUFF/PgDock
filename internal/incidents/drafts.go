package incidents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// Proposed maintenance (V4.1 §8.2): when the window gate holds work back,
// PGDock drafts the announcement it needs. A draft is not announced: it
// isn't on the status page, emails no one, and excludes nothing, until
// the admin confirms it.

// DraftLead is how far ahead a proposal's window is at least: the SLA's
// notice plus a day for the admin to confirm it.
const DraftLead = NoticeHours*time.Hour + 24*time.Hour

// Proposal is work that needs an announced window.
type Proposal struct {
	// Reason is what the work is (e.g. "minor_upgrade"); one draft per
	// reason and window gathers its projects.
	Reason     string
	Title      string
	Body       string
	Start, End time.Time
	Projects   []uuid.UUID
}

// Propose drafts maintenance for the projects that don't have any planned
// (announced or drafted) yet, adding them to the window's open draft for
// the same reason when there is one. A project whose draft for this window
// was discarded isn't proposed again for it. It returns the draft, if any
// project needed one.
func (s *Service) Propose(ctx context.Context, p Proposal, now time.Time) (*Incident, error) {
	p.Start, p.End = p.Start.UTC().Truncate(time.Minute), p.End.UTC().Truncate(time.Minute)
	if !p.End.After(p.Start) {
		return nil, fmt.Errorf("%w: the window must end after it starts", ErrInvalid)
	}
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		var need []uuid.UUID
		for _, pid := range p.Projects {
			planned, err := q.MaintenancePlannedFor(ctx, store.MaintenancePlannedForParams{ProjectID: pid, Now: now, WindowStart: p.Start})
			if err != nil {
				return err
			}
			if !planned {
				need = append(need, pid)
			}
		}
		if len(need) == 0 {
			return nil
		}
		draft, err := q.OpenDraftFor(ctx, store.OpenDraftForParams{ProposedFor: &p.Reason, ScheduledStart: &p.Start})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			title, body := announcementText(Maintenance{Title: p.Title, Body: p.Body, Start: p.Start, End: p.End})
			draft, err = q.InsertMaintenanceDraft(ctx, store.InsertMaintenanceDraftParams{Title: title, Components: []string{ComponentDedicated},
				ScheduledStart: p.Start, ScheduledEnd: &p.End, ProposedFor: &p.Reason})
			if err != nil {
				return err
			}
			if _, err := q.InsertIncidentUpdate(ctx, store.InsertIncidentUpdateParams{IncidentID: draft.ID, Status: "identified", Body: body}); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		id = draft.ID
		for _, pid := range need {
			if err := q.InsertIncidentScope(ctx, store.InsertIncidentScopeParams{IncidentID: id, ProjectID: &pid}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || id == uuid.Nil {
		return nil, err
	}
	in, err := s.Get(ctx, id)
	return &in, err
}

// Confirm announces a draft: its notice counts from now, it goes to the
// status page as upcoming, and the owners and admins it covers are
// emailed.
func (s *Service) Confirm(ctx context.Context, id uuid.UUID) (Announced, error) {
	q := store.New(s.db)
	if _, err := q.ConfirmDraft(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		return Announced{}, fmt.Errorf("%w: not a draft whose window is still ahead", ErrNotFound)
	} else if err != nil {
		return Announced{}, err
	}
	out := Announced{}
	var err error
	if out.Incident, err = s.Get(ctx, id); err != nil {
		return out, err
	}
	if out.Scope, err = q.IncidentScope(ctx, id); err != nil {
		return out, err
	}
	out.ExcludedFrom = out.AnnouncedAt.Add(NoticeHours * time.Hour).Truncate(time.Minute)
	if out.ExcludedFrom.Before(*out.ScheduledStart) {
		out.ExcludedFrom = *out.ScheduledStart
	}
	out.ShortNotice = out.ExcludedFrom.After(*out.ScheduledStart)
	out.Emailed = s.emailMaintenance(ctx, out, firstBody(out.Incident))
	return out, nil
}

// Discard throws a draft away; the work it was for keeps waiting, and
// isn't proposed again for that window.
func (s *Service) Discard(ctx context.Context, id uuid.UUID) (Incident, error) {
	if _, err := store.New(s.db).DiscardDraft(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		return Incident{}, fmt.Errorf("%w: not a draft", ErrNotFound)
	} else if err != nil {
		return Incident{}, err
	}
	return s.Get(ctx, id)
}

// firstBody is an announcement's first update: the text its email carries.
func firstBody(in Incident) string {
	if len(in.Updates) == 0 {
		return ""
	}
	first := in.Updates[0]
	for _, u := range in.Updates {
		if u.PostedAt.Before(first.PostedAt) || (u.PostedAt.Equal(first.PostedAt) && u.ID < first.ID) {
			first = u
		}
	}
	return first.Body
}

// Preview is the email an announcement sends, and who it reaches.
type Preview struct {
	Subject       string
	Body          string
	Organisations int
	Addresses     int
}

// PreviewDraft is the email confirming draft id would send.
func (s *Service) PreviewDraft(ctx context.Context, id uuid.UUID) (Preview, error) {
	in, err := s.Get(ctx, id)
	if err != nil {
		return Preview{}, err
	}
	if in.ScheduledStart == nil {
		return Preview{}, fmt.Errorf("%w: not a maintenance announcement", ErrInvalid)
	}
	scope, err := store.New(s.db).IncidentScope(ctx, id)
	if err != nil {
		return Preview{}, err
	}
	var m Maintenance
	for _, sc := range scope {
		if sc.ProjectID != nil {
			m.Projects = append(m.Projects, *sc.ProjectID)
		}
		if sc.NodeID != nil {
			m.Nodes = append(m.Nodes, *sc.NodeID)
		}
	}
	if in.RegionID != nil {
		m.Region = *in.RegionID
	}
	return s.preview(ctx, in.Title, *in.ScheduledStart, firstBody(in), m)
}

// PreviewAnnouncement is the email announcing in would send (the schedule
// form's preview, V4.1 §8.3).
func (s *Service) PreviewAnnouncement(ctx context.Context, in Maintenance) (Preview, error) {
	in.Start, in.End = in.Start.UTC().Truncate(time.Minute), in.End.UTC().Truncate(time.Minute)
	title, body := announcementText(in)
	return s.preview(ctx, title, in.Start, body, in)
}

func (s *Service) preview(ctx context.Context, title string, start time.Time, body string, scope Maintenance) (Preview, error) {
	out := Preview{}
	out.Subject, out.Body = s.maintenanceEmail(title, start, body)
	rows, err := store.New(s.db).MaintenanceRecipients(ctx, store.MaintenanceRecipientsParams{
		ProjectIds: nonNil(scope.Projects), NodeIds: nonNil(scope.Nodes), Region: nonEmpty(scope.Region)})
	if err != nil {
		return out, err
	}
	var orgs, addrs []string
	for _, r := range rows {
		if o := r.OrgID.String(); !slices.Contains(orgs, o) {
			orgs = append(orgs, o)
		}
		if !slices.Contains(addrs, r.Email) {
			addrs = append(addrs, r.Email)
		}
	}
	out.Organisations, out.Addresses = len(orgs), len(addrs)
	return out, nil
}

func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
