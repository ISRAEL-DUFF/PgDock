package freetier

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

const sweepLockKey int64 = 0x7067646f636b16 // "pgdock\x16"

// Swept counts what one sweep did.
type Swept struct {
	Warned, Paused, Archived, Noticed, Deleted, Resumed int
}

// Run sweeps every interval until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("free tier sweep", "err", err)
		}
		t.Reset(interval)
	}
}

// Sweep does what is due (V3 §4): projects of organisations that now pay
// wake; idle Free projects are warned, then paused; long-paused ones are
// archived; long-archived ones get their notices, then are deleted. One
// server sweeps at a time.
func (s *Service) Sweep(ctx context.Context) (Swept, error) {
	var out Swept
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", sweepLockKey).Scan(&got); err != nil || !got {
		return out, err
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", sweepLockKey) }()

	q := store.New(s.db)
	now := s.now()
	var errs []error
	try := func(err error) {
		if err != nil && !errors.Is(err, ErrConflict) {
			errs = append(errs, err)
		}
	}

	// Paid plans are never paused or archived (V3 §4.3).
	paid, err := q.SleepingPaidProjects(ctx)
	try(err)
	for _, p := range paid {
		if _, err := s.Resume(ctx, p.ID, nil); err == nil {
			out.Resumed++
		} else {
			try(err)
		}
	}

	// Without a waker, a paused project could only fail its clients.
	if pm := s.projects.Pooler(); pm != nil && pm.WakerSet() {
		idle, err := q.FreeProjectsIdleSince(ctx, ptr(now.Add(-(s.cfg.PauseAfter - s.cfg.Warn))))
		try(err)
		for _, p := range idle {
			switch {
			case p.PauseWarnedAt == nil:
				s.notify(ctx, p, fmt.Sprintf("[PGDock] %s will be paused in %s", p.Name, hours(s.cfg.Warn)), fmt.Sprintf(
					"%s has had no client connections since %s. Free projects are paused after %d days without connections, so it will be paused in about %s.\n\n"+
						"Connecting to it before then keeps it active. A paused project keeps its data and resumes on the next connection.\n\n%s",
					p.Name, lastActive(p).UTC().Format("2 Jan 2006"), days(s.cfg.PauseAfter), hours(s.cfg.Warn), s.projectLink(p)))
				try(q.MarkPauseWarned(ctx, p.ID))
				out.Warned++
			case now.Sub(lastActive(p)) >= s.cfg.PauseAfter && now.Sub(*p.PauseWarnedAt) >= s.cfg.Warn:
				if _, err := s.enqueue(ctx, p.ID, nil, func(store.Project) (string, error) { return KindPause, nil }); err == nil {
					out.Paused++
				} else {
					try(err)
				}
			}
		}
	}

	if _, _, err := s.backups.StorageTarget(ctx); err == nil {
		long, err := q.FreeProjectsPausedSince(ctx, ptr(now.Add(-s.cfg.ArchiveAfter)))
		try(err)
		for _, p := range long {
			if _, err := s.enqueue(ctx, p.ID, nil, func(store.Project) (string, error) { return KindArchive, nil }); err == nil {
				out.Archived++
			} else {
				try(err)
			}
		}
	}

	archived, err := q.ArchivedProjects(ctx)
	try(err)
	for _, p := range archived {
		n, d, err := s.sweepArchived(ctx, p, now)
		try(err)
		out.Noticed += n
		out.Deleted += d
	}
	return out, errors.Join(errs...)
}

// sweepArchived sends an archived project's deletion notices and deletes
// it when due.
func (s *Service) sweepArchived(ctx context.Context, p store.Project, now time.Time) (noticed, deleted int, err error) {
	if p.ArchivedAt == nil {
		return 0, 0, nil
	}
	due := p.ArchivedAt.Add(s.cfg.DeleteAfter)
	last := s.cfg.NoticeDays[len(s.cfg.NoticeDays)-1]
	for _, d := range s.cfg.NoticeDays {
		sent := p.ArchiveNoticeDays != nil && int(*p.ArchiveNoticeDays) <= d
		if sent || now.Before(due.AddDate(0, 0, -d)) {
			continue
		}
		s.notify(ctx, p, fmt.Sprintf("[PGDock] %s will be deleted in %d days", p.Name, d), fmt.Sprintf(
			"%s has been archived since %s. Archived Free projects are deleted after %d days, so it will be deleted on %s.\n\n"+
				"Connecting to it, or restoring it from the dashboard, before then keeps it:\n%s",
			p.Name, p.ArchivedAt.UTC().Format("2 Jan 2006"), days(s.cfg.DeleteAfter), due.UTC().Format("2 Jan 2006"), s.projectLink(p)))
		if err := store.New(s.db).SetArchiveNotice(ctx, store.SetArchiveNoticeParams{ID: p.ID, Days: ptr(int32(d))}); err != nil {
			return noticed, 0, err
		}
		p.ArchiveNoticeDays = ptr(int32(d))
		noticed++
		break // one notice per sweep: the most urgent one due
	}
	if now.Before(due) || p.ArchiveNoticeDays == nil || int(*p.ArchiveNoticeDays) > last {
		return noticed, 0, nil
	}
	// Its database is gone already: no final backup to take.
	if _, err := s.projects.Delete(ctx, p.ID, p.Name, true, nil); err != nil {
		if errors.Is(err, provision.ErrConflict) {
			s.log.Info("archived project not deleted yet", "project_id", p.ID, "reason", err)
			return noticed, 0, nil
		}
		return noticed, 0, err
	}
	if p.ArchiveBackupID != nil {
		if err := store.New(s.db).ExpireBackupAt(ctx, store.ExpireBackupAtParams{ID: *p.ArchiveBackupID, ExpiresAt: ptr(now.Add(30 * 24 * time.Hour))}); err != nil {
			return noticed, 1, err
		}
	}
	return noticed, 1, nil
}

func hours(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d days", days(d))
	}
	return fmt.Sprintf("%d hours", int(d.Round(time.Hour).Hours()))
}
