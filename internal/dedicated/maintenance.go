package dedicated

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// MaintenanceWindow is the weekly window in which instances are restarted
// onto a newer Postgres minor release (V3 §2.4). Times are UTC.
type MaintenanceWindow struct {
	Enabled   bool         `json:"enabled"`
	Weekday   time.Weekday `json:"weekday"`    // 0 is Sunday
	StartHour int          `json:"start_hour"` // 0-23
	Hours     int          `json:"hours"`      // 1-24
}

// DefaultWindow is Sunday 02:00-06:00 UTC.
var DefaultWindow = MaintenanceWindow{Enabled: true, Weekday: time.Sunday, StartHour: 2, Hours: 4}

const windowKey = "maintenance_window"

// Validate checks the window's fields.
func (w MaintenanceWindow) Validate() error {
	switch {
	case w.Weekday < time.Sunday || w.Weekday > time.Saturday:
		return fmt.Errorf("%w: weekday must be 0 (Sunday) to 6 (Saturday)", provision.ErrInvalid)
	case w.StartHour < 0 || w.StartHour > 23:
		return fmt.Errorf("%w: start_hour must be 0 to 23", provision.ErrInvalid)
	case w.Hours < 1 || w.Hours > 24:
		return fmt.Errorf("%w: hours must be 1 to 24", provision.ErrInvalid)
	}
	return nil
}

// lastStart is the most recent start of the window at or before t.
func (w MaintenanceWindow) lastStart(t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), w.StartHour, 0, 0, 0, time.UTC)
	day = day.AddDate(0, 0, -((int(t.Weekday()) - int(w.Weekday) + 7) % 7))
	if day.After(t) {
		day = day.AddDate(0, 0, -7)
	}
	return day
}

// Contains reports whether t falls in an enabled window.
func (w MaintenanceWindow) Contains(t time.Time) bool {
	return w.Enabled && t.Before(w.lastStart(t).Add(time.Duration(w.Hours)*time.Hour))
}

// Next is the start of the window t is in, or else of the next one.
func (w MaintenanceWindow) Next(t time.Time) time.Time {
	s := w.lastStart(t)
	if w.Contains(t) {
		return s
	}
	return s.AddDate(0, 0, 7)
}

// MaintenanceWindow returns the configured window (DefaultWindow if none).
func (s *Service) MaintenanceWindow(ctx context.Context) (MaintenanceWindow, error) {
	raw, err := store.New(s.db).GetSetting(ctx, windowKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultWindow, nil
	}
	if err != nil {
		return MaintenanceWindow{}, err
	}
	w := DefaultWindow
	if err := json.Unmarshal(raw, &w); err != nil {
		return MaintenanceWindow{}, fmt.Errorf("maintenance window setting: %w", err)
	}
	return w, nil
}

// SetMaintenanceWindow stores the window.
func (s *Service) SetMaintenanceWindow(ctx context.Context, w MaintenanceWindow) error {
	if err := w.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return store.New(s.db).PutSetting(ctx, store.PutSettingParams{Key: windowKey, Value: raw})
}

// releaseCheckEvery is how often each instance's releases are refreshed.
const releaseCheckEvery = time.Hour

// CheckReleases asks the agents which Postgres release each running
// instance runs and which its image now holds, for instances not checked
// since `before`. Instances without an agent (a cluster the server was
// configured with) are skipped.
func (s *Service) CheckReleases(ctx context.Context, before time.Time) error {
	q := store.New(s.db)
	insts, err := q.InstancesToCheckRelease(ctx, before)
	if err != nil {
		return err
	}
	var errs []error
	for _, inst := range insts {
		agent, err := s.nodes.ForNode(ctx, inst.NodeID)
		if errors.Is(err, nodes.ErrNoAgent) || errors.Is(err, nodes.ErrNotFound) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		res, err := agent.Instance(ctx, agentKey(inst))
		if err != nil {
			errs = append(errs, fmt.Errorf("instance %s: %w", inst.ID, err))
			continue
		}
		if err := q.SetInstanceRelease(ctx, store.SetInstanceReleaseParams{
			ID: inst.ID, PgRelease: nonEmpty(res.Version), PgReleaseAvailable: nonEmpty(res.ImageVersion),
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// NewerRelease reports whether release b ("18.2") is a later minor of the
// same major as a ("18.1").
func NewerRelease(a, b string) bool {
	am, an, ok1 := splitRelease(a)
	bm, bn, ok2 := splitRelease(b)
	return ok1 && ok2 && am == bm && bn > an
}

func splitRelease(r string) (major, minor int, ok bool) {
	ms, ns, found := strings.Cut(r, ".")
	if !found {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(ms)
	minor, err2 := strconv.Atoi(ns)
	return major, minor, err1 == nil && err2 == nil
}

// MaintenanceSweep refreshes releases and, inside the window, upgrades at
// most one instance that is behind its image, so restarts happen one at a
// time. It returns the instance it upgraded, if any.
func (s *Service) MaintenanceSweep(ctx context.Context, now time.Time) (*store.Instance, error) {
	if err := s.CheckReleases(ctx, now.Add(-releaseCheckEvery)); err != nil {
		s.log.Warn("checking Postgres releases", "err", err)
	}
	w, err := s.MaintenanceWindow(ctx)
	if err != nil || !w.Contains(now) {
		return nil, err
	}
	q := store.New(s.db)
	behind, err := q.InstancesBehind(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range behind {
		if !NewerRelease(*inst.PgRelease, *inst.PgReleaseAvailable) {
			continue
		}
		busy, err := q.InstanceBusy(ctx, inst.ID)
		if err != nil {
			return nil, err
		}
		if busy {
			continue // next sweep, once the move or restore is done
		}
		_, err = s.MinorUpgrade(ctx, inst)
		return &inst, err
	}
	return nil, nil
}

// MinorUpgrade restarts inst from its image's newer release on the same
// data. The poolers hold its projects' clients for the restart, so they
// wait rather than fail; session-pooler clients reconnect.
func (s *Service) MinorUpgrade(ctx context.Context, inst store.Instance) (uuid.UUID, error) {
	q := store.New(s.db)
	from, to := deref(inst.PgRelease), deref(inst.PgReleaseAvailable)
	run, err := q.StartMinorUpgrade(ctx, store.StartMinorUpgradeParams{InstanceID: inst.ID, FromRelease: from, ToRelease: to})
	if err != nil {
		return uuid.Nil, err
	}
	finish := func(pause *int32, failure error) (uuid.UUID, error) {
		var msg *string
		if failure != nil {
			m := failure.Error()
			msg = &m
		}
		if err := q.FinishMinorUpgrade(context.WithoutCancel(ctx), store.FinishMinorUpgradeParams{ID: run.ID, PauseMs: pause, Error: msg}); err != nil {
			return run.ID, errors.Join(failure, err)
		}
		return run.ID, failure
	}
	projects, err := q.MaintenanceProjects(ctx, inst.ID)
	if err != nil {
		return finish(nil, err)
	}
	var dbs []string
	for _, p := range projects {
		dbs = append(dbs, store.PoolerNames(p)...)
	}
	start := time.Now()
	killed, err := s.projects.Pooler().Freeze(ctx, freezeWait, dbs...)
	if err != nil {
		_ = s.projects.Pooler().Resume(context.WithoutCancel(ctx), dbs...)
		return finish(nil, fmt.Errorf("pause the poolers: %w", err))
	}
	res, rerr := s.Recreate(ctx, inst)
	if err := s.projects.Pooler().Resume(context.WithoutCancel(ctx), dbs...); err != nil && !isNotPaused(err) {
		rerr = errors.Join(rerr, fmt.Errorf("pooler RESUME: %w", err))
	}
	pause := int32(time.Since(start).Milliseconds())
	if rerr != nil {
		s.log.Error("minor upgrade failed", "instance", inst.ID, "from", from, "to", to, "err", rerr)
		return finish(&pause, rerr)
	}
	if err := q.SetInstanceRelease(ctx, store.SetInstanceReleaseParams{
		ID: inst.ID, PgRelease: nonEmpty(res.Version), PgReleaseAvailable: nonEmpty(res.ImageVersion),
	}); err != nil {
		return finish(&pause, err)
	}
	s.log.Info("minor upgrade", "instance", inst.ID, "kind", inst.Kind, "from", from, "to", res.Version,
		"projects", len(projects), "pause_ms", pause, "session_clients_dropped", len(killed) > 0)
	return finish(&pause, nil)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ErrUpToDate means the instance already runs its image's release.
var ErrUpToDate = errors.New("the instance already runs the newest release its image has")

// MinorUpgradeNow upgrades one instance outside the window, at the admin's
// request: it asks the agent for the releases first, and refuses when the
// instance is up to date or a project on it is busy. The returned run
// carries the error if the restart failed.
func (s *Service) MinorUpgradeNow(ctx context.Context, id uuid.UUID) (store.GetMinorUpgradeRow, error) {
	q := store.New(s.db)
	inst, err := q.GetInstance(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && inst.DeletedAt != nil) {
		return store.GetMinorUpgradeRow{}, provision.ErrNotFound
	}
	if err != nil {
		return store.GetMinorUpgradeRow{}, err
	}
	if inst.Status != "running" {
		return store.GetMinorUpgradeRow{}, fmt.Errorf("%w: the instance is %s", provision.ErrConflict, inst.Status)
	}
	agent, err := s.nodes.ForNode(ctx, inst.NodeID)
	if err != nil {
		return store.GetMinorUpgradeRow{}, err
	}
	res, err := agent.Instance(ctx, agentKey(inst))
	if err != nil {
		return store.GetMinorUpgradeRow{}, err
	}
	inst.PgRelease, inst.PgReleaseAvailable = nonEmpty(res.Version), nonEmpty(res.ImageVersion)
	if err := q.SetInstanceRelease(ctx, store.SetInstanceReleaseParams{ID: inst.ID, PgRelease: inst.PgRelease, PgReleaseAvailable: inst.PgReleaseAvailable}); err != nil {
		return store.GetMinorUpgradeRow{}, err
	}
	if !NewerRelease(res.Version, res.ImageVersion) {
		return store.GetMinorUpgradeRow{}, fmt.Errorf("%w: %w (%s)", provision.ErrConflict, ErrUpToDate, res.Version)
	}
	if busy, err := q.InstanceBusy(ctx, inst.ID); err != nil {
		return store.GetMinorUpgradeRow{}, err
	} else if busy {
		return store.GetMinorUpgradeRow{}, fmt.Errorf("%w: a project on the instance is busy (a move, restore, or queued operation); try again when it finishes", provision.ErrConflict)
	}
	runID, err := s.MinorUpgrade(ctx, inst)
	if runID == uuid.Nil {
		return store.GetMinorUpgradeRow{}, err
	}
	return q.GetMinorUpgrade(ctx, runID)
}

// RunMaintenance runs MaintenanceSweep now and then every interval until
// ctx ends.
func (s *Service) RunMaintenance(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.MaintenanceSweep(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.Warn("maintenance sweep failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
