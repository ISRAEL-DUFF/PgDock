// Package incidents is PGDock's side of the status page (V3 §2.6): the
// platform admin's incidents, pushed to pgdock-status with a signature,
// and the heartbeat that reports the components pgdock-status can't probe
// from outside.
package incidents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/statusapi"
	"github.com/israel-duff/pgdock/internal/store"
)

// DefaultComponents are the status page's component IDs
// (deploy/status/status.example.toml).
var DefaultComponents = []string{"dashboard", "edge-pooler", "shared-tier", "dedicated", "backups", "webhooks-jobs"}

// Heartbeat components: the ones pgdock-server reports.
const (
	ComponentDedicated    = "dedicated"
	ComponentBackups      = "backups"
	ComponentWebhooksJobs = "webhooks-jobs"
)

// Config connects PGDock to its status page.
type Config struct {
	// URL and Secret reach pgdock-status; empty URL keeps incidents local.
	URL, Secret string
	// Components the admin can attach incidents to (DefaultComponents).
	Components []string
	// Region is the default region of new incidents.
	Region string
}

// Service manages incidents.
type Service struct {
	db        *pgxpool.Pool
	cfg       Config
	client    *statusapi.Client
	log       *slog.Logger
	mailer    Mailer
	publicURL string
}

// New returns the incidents service.
func New(db *pgxpool.Pool, cfg Config, log *slog.Logger) *Service {
	if len(cfg.Components) == 0 {
		cfg.Components = DefaultComponents
	}
	s := &Service{db: db, cfg: cfg, log: log}
	if cfg.URL != "" {
		s.client = &statusapi.Client{URL: cfg.URL, Secret: cfg.Secret}
	}
	return s
}

// Components are the IDs incidents may name.
func (s *Service) Components() []string { return s.cfg.Components }

// StatusURL is the status page, if one is configured.
func (s *Service) StatusURL() string { return s.cfg.URL }

// ErrInvalid wraps input the caller got wrong; ErrNotFound means no such
// incident.
var (
	ErrInvalid  = errors.New("invalid incident")
	ErrNotFound = errors.New("no such incident")
)

// Incident is an incident with its updates.
type Incident struct {
	store.Incident
	Updates []store.IncidentUpdatesRow
}

// Input is a new incident: its fields and its first update.
type Input struct {
	Title      string
	Components []string
	Region     string
	Severity   string
	Status     string
	Body       string
	By         *uuid.UUID
}

func (s *Service) check(title string, components []string, severity, status string) error {
	var errs []string
	if t := strings.TrimSpace(title); t == "" || len(t) > 200 {
		errs = append(errs, "the title must be 1–200 characters")
	}
	if len(components) == 0 {
		errs = append(errs, "choose at least one affected component")
	}
	for _, c := range components {
		if !slices.Contains(s.cfg.Components, c) {
			errs = append(errs, fmt.Sprintf("%q is not a status page component (%s)", c, strings.Join(s.cfg.Components, ", ")))
		}
	}
	if !slices.Contains(statusapi.Severities, severity) {
		errs = append(errs, "severity must be one of "+strings.Join(statusapi.Severities, ", "))
	}
	if !slices.Contains(statusapi.Statuses, status) {
		errs = append(errs, "status must be one of "+strings.Join(statusapi.Statuses, ", "))
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(errs, "; "))
	}
	return nil
}

func checkBody(body string) error {
	if b := strings.TrimSpace(body); b == "" || len(b) > 5000 {
		return fmt.Errorf("%w: the update must be 1–5000 characters", ErrInvalid)
	}
	return nil
}

// Create opens an incident with its first update.
func (s *Service) Create(ctx context.Context, in Input) (Incident, error) {
	in.Components = dedupe(in.Components)
	if err := s.check(in.Title, in.Components, in.Severity, in.Status); err != nil {
		return Incident{}, err
	}
	if err := checkBody(in.Body); err != nil {
		return Incident{}, err
	}
	region := in.Region
	if region == "" {
		region = s.cfg.Region
	}
	var id uuid.UUID
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		inc, err := q.InsertIncident(ctx, store.InsertIncidentParams{
			Title: strings.TrimSpace(in.Title), Components: in.Components, RegionID: nonEmpty(region),
			Severity: in.Severity, Status: in.Status, CreatedBy: in.By,
		})
		if err != nil {
			return err
		}
		id = inc.ID
		if in.Status == "resolved" {
			now := time.Now()
			if _, err := q.UpdateIncident(ctx, store.UpdateIncidentParams{ID: id, Title: inc.Title, Components: inc.Components,
				RegionID: inc.RegionID, Severity: inc.Severity, Status: inc.Status, ResolvedAt: &now}); err != nil {
				return err
			}
		}
		_, err = q.InsertIncidentUpdate(ctx, store.InsertIncidentUpdateParams{IncidentID: id, Status: in.Status, Body: strings.TrimSpace(in.Body), PostedBy: in.By})
		return err
	})
	if err != nil {
		return Incident{}, err
	}
	return s.Get(ctx, id)
}

// Change edits an incident's title, components or severity.
type Change struct {
	Title      *string
	Components []string
	Severity   *string
}

// Edit applies c.
func (s *Service) Edit(ctx context.Context, id uuid.UUID, c Change) (Incident, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Incident{}, err
	}
	p := store.UpdateIncidentParams{ID: id, Title: cur.Title, Components: cur.Components, RegionID: cur.RegionID,
		Severity: cur.Severity, Status: cur.Status, ResolvedAt: cur.ResolvedAt}
	if c.Title != nil {
		p.Title = strings.TrimSpace(*c.Title)
	}
	if c.Components != nil {
		p.Components = dedupe(c.Components)
	}
	if c.Severity != nil {
		p.Severity = *c.Severity
	}
	if err := s.check(p.Title, p.Components, p.Severity, p.Status); err != nil {
		return Incident{}, err
	}
	if _, err := store.New(s.db).UpdateIncident(ctx, p); err != nil {
		return Incident{}, err
	}
	return s.Get(ctx, id)
}

// Post adds an update; its status becomes the incident's. Resolving sets
// resolved_at, and any other status reopens a resolved incident.
func (s *Service) Post(ctx context.Context, id uuid.UUID, status, body string, by *uuid.UUID) (Incident, error) {
	if !slices.Contains(statusapi.Statuses, status) {
		return Incident{}, fmt.Errorf("%w: status must be one of %s", ErrInvalid, strings.Join(statusapi.Statuses, ", "))
	}
	if err := checkBody(body); err != nil {
		return Incident{}, err
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		cur, err := q.GetIncident(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		resolved := cur.ResolvedAt
		switch {
		case status == "resolved" && resolved == nil:
			now := time.Now()
			resolved = &now
		case status != "resolved":
			resolved = nil
		}
		if _, err := q.UpdateIncident(ctx, store.UpdateIncidentParams{ID: id, Title: cur.Title, Components: cur.Components,
			RegionID: cur.RegionID, Severity: cur.Severity, Status: status, ResolvedAt: resolved}); err != nil {
			return err
		}
		_, err = q.InsertIncidentUpdate(ctx, store.InsertIncidentUpdateParams{IncidentID: id, Status: status, Body: strings.TrimSpace(body), PostedBy: by})
		return err
	})
	if err != nil {
		return Incident{}, err
	}
	return s.Get(ctx, id)
}

// Get returns one incident.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Incident, error) {
	q := store.New(s.db)
	inc, err := q.GetIncident(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Incident{}, ErrNotFound
	}
	if err != nil {
		return Incident{}, err
	}
	ups, err := q.IncidentUpdates(ctx, id)
	if err != nil {
		return Incident{}, err
	}
	return Incident{Incident: inc, Updates: ups}, nil
}

// List returns open incidents first, then the newest.
func (s *Service) List(ctx context.Context, limit int32) ([]Incident, error) {
	q := store.New(s.db)
	list, err := q.ListIncidents(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Incident, 0, len(list))
	for _, inc := range list {
		ups, err := q.IncidentUpdates(ctx, inc.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, Incident{Incident: inc, Updates: ups})
	}
	return out, nil
}

// wire is the incident as pgdock-status takes it.
func wire(in Incident) statusapi.Incident {
	w := statusapi.Incident{ID: in.ID.String(), Title: in.Title, Components: in.Components, Severity: in.Severity,
		Status: in.Status, StartedAt: in.StartedAt.UTC(), Updates: []statusapi.IncidentUpdate{}}
	if in.RegionID != nil {
		w.Region = *in.RegionID
	}
	if in.ResolvedAt != nil {
		t := in.ResolvedAt.UTC()
		w.ResolvedAt = &t
	}
	for _, u := range in.Updates {
		w.Updates = append(w.Updates, statusapi.IncidentUpdate{ID: strconv.FormatInt(u.ID, 10), Status: u.Status, Body: u.Body, PostedAt: u.PostedAt.UTC()})
	}
	return w
}

// Push sends every incident changed since its last push. A failure is
// recorded on the incident and retried on the next call.
func (s *Service) Push(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	q := store.New(s.db)
	list, err := q.UnpushedIncidents(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, inc := range list {
		full, err := s.Get(ctx, inc.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = s.client.PutIncident(pctx, wire(full))
		cancel()
		if err != nil {
			msg := err.Error()
			if len(msg) > 500 {
				msg = msg[:500]
			}
			_ = q.MarkIncidentPushFailed(ctx, store.MarkIncidentPushFailedParams{ID: inc.ID, PushError: &msg})
			errs = append(errs, fmt.Errorf("incident %s: %w", inc.ID, err))
			continue
		}
		if err := q.MarkIncidentPushed(ctx, store.MarkIncidentPushedParams{ID: inc.ID, UpdatedAt: inc.UpdatedAt}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Heartbeat reports the components pgdock-status can't probe.
func (s *Service) Heartbeat(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	hb, err := s.States(ctx)
	if err != nil {
		return err
	}
	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return s.client.Heartbeat(hctx, statusapi.Heartbeat{SentAt: time.Now().UTC(), Components: hb})
}

// Run pushes incidents every few seconds and a heartbeat every minute
// until ctx ends.
func (s *Service) Run(ctx context.Context, heartbeatEvery time.Duration) {
	if s.client == nil {
		// No status page: only announced maintenance needs closing.
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			if err := s.EndMaintenance(ctx, time.Now()); err != nil && ctx.Err() == nil {
				s.log.Warn("ending maintenance windows", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}
	push := time.NewTicker(10 * time.Second)
	defer push.Stop()
	beat := time.NewTicker(heartbeatEvery)
	defer beat.Stop()
	var lastPushErr, lastBeatErr string
	report := func(what string, err error, last *string) {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		switch {
		case msg != "" && msg != *last:
			s.log.Warn("status page "+what+" failed", "err", err)
		case msg == "" && *last != "":
			s.log.Info("status page " + what + " works again")
		}
		*last = msg
	}
	report("heartbeat", s.Heartbeat(ctx), &lastBeatErr)
	report("incident push", s.Push(ctx), &lastPushErr)
	for {
		select {
		case <-ctx.Done():
			return
		case <-push.C:
			if err := s.EndMaintenance(ctx, time.Now()); err != nil && ctx.Err() == nil {
				s.log.Warn("ending maintenance windows", "err", err)
			}
			report("incident push", s.Push(ctx), &lastPushErr)
		case <-beat.C:
			report("heartbeat", s.Heartbeat(ctx), &lastBeatErr)
		}
	}
}

func dedupe(xs []string) []string {
	out := []string{}
	for _, x := range xs {
		if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
