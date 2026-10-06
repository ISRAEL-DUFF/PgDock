package freetier

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/backup"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// setConnections lets the project's database accept connections or not,
// ending its sessions when it stops.
func (s *Service) setConnections(ctx context.Context, p store.Project, allow bool) error {
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, "postgres")
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, fmt.Sprintf("ALTER DATABASE %s WITH ALLOW_CONNECTIONS %t", provision.Ident(p.DbName), allow)); err != nil {
		return fmt.Errorf("allow connections %t: %w", allow, err)
	}
	if !allow {
		if _, err := conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, p.DbName); err != nil {
			return fmt.Errorf("end sessions: %w", err)
		}
	}
	return nil
}

// runPause pauses an idle Free project (V3 §4.2): the route to the waker
// first, so new connections wake it rather than fail, then the database
// stops accepting connections and its sessions end.
func (s *Service) runPause(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	p, err := s.projects.ProjectFor(ctx, op)
	if err != nil {
		return err
	}
	switch p.Lifecycle {
	case Archived:
		return jobs.Permanent(errors.New("the project is archived"))
	case Active:
		if seen := lastActive(p); s.now().Sub(seen) < s.cfg.PauseAfter {
			return log.Info(ctx, "done", "a client connected at %s; not pausing", seen.UTC().Format(time.RFC3339))
		}
		if p, err = store.New(s.db).SetProjectPaused(ctx, p.ID); err != nil {
			return err
		}
	}
	if err := s.projects.SyncPooler(ctx, log, "pooler", "the route points at the waker"); err != nil {
		return err
	}
	if err := s.setConnections(ctx, p, false); err != nil {
		return err
	}
	if err := log.Info(ctx, "database", "%s no longer accepts connections; its data is kept", p.DbName); err != nil {
		return err
	}
	s.notify(ctx, p, fmt.Sprintf("[PGDock] %s is paused", p.Name), fmt.Sprintf(
		"%s had no client connections for %d days, so it is paused. Its data is kept.\n\n"+
			"It resumes by itself the next time a client connects (the first connection is refused with a message; retry after about 30 seconds), or resume it now:\n%s\n\n"+
			"A Free project paused for %d days is archived; paid plans are never paused.",
		p.Name, days(s.cfg.PauseAfter), s.projectLink(p), days(s.cfg.ArchiveAfter)))
	return nil
}

func (s *Service) failPause(ctx context.Context, op store.Operation, log *jobs.StepLogger, cause error) error {
	p, err := s.projects.ProjectFor(ctx, op)
	if err != nil {
		return err
	}
	_ = log.Warn(ctx, "rollback", "pause failed (%v); the project stays active", cause)
	return s.wake(ctx, p, log)
}

// wake lets the database accept connections and routes clients back to it.
func (s *Service) wake(ctx context.Context, p store.Project, log *jobs.StepLogger) error {
	if err := s.setConnections(ctx, p, true); err != nil {
		return err
	}
	if _, err := store.New(s.db).SetProjectAwake(ctx, p.ID); err != nil {
		return err
	}
	return s.projects.SyncPooler(ctx, log, "pooler", "the route points at the database again")
}

// runResume resumes a paused project (seconds).
func (s *Service) runResume(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	p, err := s.projects.ProjectFor(ctx, op)
	if err != nil {
		return err
	}
	switch p.Lifecycle {
	case Active:
		return log.Info(ctx, "done", "the project is already active")
	case Archived:
		return jobs.Permanent(errors.New("the project is archived; it is restored by an unarchive"))
	}
	if err := s.wake(ctx, p, log); err != nil {
		return err
	}
	return log.Info(ctx, "done", "%s resumed", p.Name)
}

// runArchive archives a project paused for long (V3 §4.3): a final
// logical backup, verified by restoring it, then the database is dropped.
// Roles, credentials and the connection string are kept.
func (s *Service) runArchive(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	p, err := s.projects.ProjectFor(ctx, op)
	if err != nil {
		return err
	}
	switch p.Lifecycle {
	case Archived:
		return log.Info(ctx, "done", "the project is already archived")
	case Active:
		return jobs.Permanent(errors.New("the project is active again"))
	}
	q := store.New(s.db)
	opID := op.ID
	b, err := q.BackupForOperation(ctx, store.BackupForOperationParams{OperationID: &opID, Kind: backup.Archive})
	if errors.Is(err, pgx.ErrNoRows) {
		// The route still points at the waker, so no client reaches the
		// database while the dump runs.
		if err := s.setConnections(ctx, p, true); err != nil {
			return err
		}
		if b, err = s.backups.Archive(ctx, p, &opID, log); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := log.Info(ctx, "backup", "archive backup already taken by an earlier attempt"); err != nil {
		return err
	}
	// Archived first: should the drop fail, a later unarchive recreates the
	// database from the backup either way.
	if _, err := q.SetProjectArchived(ctx, store.SetProjectArchivedParams{ID: p.ID, BackupID: &b.ID}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, "postgres")
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+provision.Ident(p.DbName)+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	if err := log.Info(ctx, "database", "dropped %s; it is restored from the archive on the next connection", p.DbName); err != nil {
		return err
	}
	s.notify(ctx, p, fmt.Sprintf("[PGDock] %s is archived", p.Name), fmt.Sprintf(
		"%s was paused for %d days, so it is archived: its data is in a verified backup and its database is removed. Its connection string and credentials are kept.\n\n"+
			"The next connection restores it from the archive (minutes, not seconds), or restore it now:\n%s\n\n"+
			"An archived Free project is deleted after %d days archived; you will be told %d and %d days before.",
		p.Name, days(s.cfg.ArchiveAfter), s.projectLink(p), days(s.cfg.DeleteAfter), s.cfg.NoticeDays[0], s.cfg.NoticeDays[len(s.cfg.NoticeDays)-1]))
	return nil
}

func (s *Service) failArchive(ctx context.Context, op store.Operation, log *jobs.StepLogger, cause error) error {
	p, err := s.projects.ProjectFor(ctx, op)
	if err != nil {
		return err
	}
	if p.Lifecycle != Paused {
		return nil
	}
	_ = log.Warn(ctx, "rollback", "archive failed (%v); the project stays paused", cause)
	return s.setConnections(ctx, p, false)
}

// runUnarchive restores an archived project from its archive backup.
func (s *Service) runUnarchive(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	p, err := s.projects.ProjectFor(ctx, op)
	if err != nil {
		return err
	}
	if p.Lifecycle == Active {
		return log.Info(ctx, "done", "the project is already active")
	}
	if p.Lifecycle == Paused {
		return s.wake(ctx, p, log)
	}
	if p.ArchiveBackupID == nil {
		return jobs.Permanent(errors.New("the project has no archive backup"))
	}
	q := store.New(s.db)
	b, err := q.GetBackup(ctx, *p.ArchiveBackupID)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("load the archive backup: %w", err))
	}
	if err := log.Info(ctx, "restore", "restoring %s from its archive of %s", p.Name, b.StartedAt.UTC().Format("2006-01-02 15:04 MST")); err != nil {
		return err
	}
	if err := s.backups.RestoreArchive(ctx, p, b, log); err != nil {
		return err
	}
	if _, err := q.SetProjectAwake(ctx, p.ID); err != nil {
		return err
	}
	// The project's nightly backups take over; the archive goes in 30 days.
	if err := q.ExpireBackupAt(ctx, store.ExpireBackupAtParams{ID: b.ID, ExpiresAt: ptr(s.now().Add(30 * 24 * time.Hour))}); err != nil {
		return err
	}
	if err := s.projects.SyncPooler(ctx, log, "pooler", "the route points at the database again"); err != nil {
		return err
	}
	s.notify(ctx, p, fmt.Sprintf("[PGDock] %s is restored", p.Name), fmt.Sprintf(
		"%s is restored from its archive and accepts connections again.\n\n%s", p.Name, s.projectLink(p)))
	return log.Info(ctx, "done", "%s restored from its archive", p.Name)
}

func (s *Service) failUnarchive(ctx context.Context, _ store.Operation, log *jobs.StepLogger, cause error) error {
	// The project stays archived; the next connection or a resume from the
	// dashboard tries again (the restore recreates the database).
	_ = log.Warn(ctx, "rollback", "restore from the archive failed (%v); the project stays archived", cause)
	return nil
}

func lastActive(p store.Project) time.Time {
	if p.LastActiveAt != nil {
		return *p.LastActiveAt
	}
	return p.CreatedAt
}

func days(d time.Duration) int { return int(d.Round(time.Hour).Hours() / 24) }

func ptr[T any](v T) *T { return &v }
