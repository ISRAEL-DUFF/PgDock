package dedicated

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Errors for PITR requests.
var (
	ErrNoBaseBackup = errors.New("no base backup old enough for that time")
	ErrFutureTarget = errors.New("the target time is in the future")
)

// Window is the range a project can be restored to.
type Window struct {
	From time.Time // the oldest base backup's end
	To   time.Time // now (WAL is archived continuously)
}

// PITRWindow reports how far back a dedicated project can be restored
// (spec §4.3: 7-day PITR).
func (s *Service) PITRWindow(ctx context.Context, p store.Project) (Window, bool, error) {
	rows, err := store.New(s.db).ListBaseBackups(ctx, store.ListBaseBackupsParams{ProjectID: &p.ID})
	if err != nil || len(rows) == 0 {
		return Window{}, false, err
	}
	return Window{From: *rows[0].FinishedAt, To: time.Now()}, true, nil
}

// PlanPITR checks a recovery of src to target (nil: the latest point) and
// returns the restore operation's "pitr" params. It first forces the
// source's current WAL segment into the archive, so the target is covered.
func (s *Service) PlanPITR(ctx context.Context, src store.Project, target *time.Time) (map[string]any, error) {
	if src.Tier != provision.TierDedicated {
		return nil, fmt.Errorf("%w: point-in-time recovery is for dedicated projects", provision.ErrInvalid)
	}
	now := time.Now()
	if target != nil && target.After(now.Add(time.Minute)) {
		return nil, fmt.Errorf("%w: %w", provision.ErrInvalid, ErrFutureTarget)
	}
	q := store.New(s.db)
	before := now
	if target != nil {
		before = *target
	}
	b, err := q.BaseBackupBefore(ctx, store.BaseBackupBeforeParams{ProjectID: &src.ID, Before: &before})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %w", provision.ErrInvalid, ErrNoBaseBackup)
	}
	if err != nil {
		return nil, err
	}
	inst, err := q.GetInstance(ctx, src.InstanceID)
	if err != nil {
		return nil, err
	}
	if err := s.flushWAL(ctx, inst); err != nil {
		return nil, fmt.Errorf("%w: archive the latest WAL first: %w", provision.ErrConflict, err)
	}
	pitr := map[string]any{
		"source_project": src.ID, "source_instance": inst.ID, "backup_id": b.ID, "backup_name": b.ObjectKey,
	}
	if target != nil {
		pitr["target_time"] = target.UTC().Format(time.RFC3339Nano)
	}
	return pitr, nil
}

// flushWAL switches to a new WAL segment and waits until the old one is
// archived, so everything committed so far is restorable.
func (s *Service) flushWAL(ctx context.Context, inst store.Instance) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	conn, err := s.adminConn(ctx, inst, "postgres")
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	// Recovery to a time stops at the first commit after it, and ends with
	// an error if there is none; a transaction with an XID commits "now",
	// so there always is one for a target in the past.
	if _, err := conn.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
		return err
	}
	// At a segment boundary pg_walfile_name names the segment just closed.
	var seg string
	if err := conn.QueryRow(ctx, `SELECT pg_walfile_name(pg_switch_wal())`).Scan(&seg); err != nil {
		return err
	}
	for {
		var last *string
		var failed *time.Time
		if err := conn.QueryRow(ctx, `SELECT last_archived_wal, last_failed_time FROM pg_stat_archiver`).Scan(&last, &failed); err != nil {
			return err
		}
		if last != nil && *last >= seg {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("segment %s not archived yet: %w", seg, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}
