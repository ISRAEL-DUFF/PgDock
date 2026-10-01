// Package backup runs logical backups, restores, the weekly restore test,
// and metadata self-backups (spec §6.3, §6.5, §11.6), and imports from
// existing databases (§6.8), through node agents and S3 storage.
package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/backupfmt"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
)

// Operation kinds.
const (
	KindBackup         = "backup"
	KindRestore        = "restore"
	KindRestoreTest    = "restore_test"
	KindMetadataBackup = "metadata_backup"
	KindImport         = "import"
	KindBaseBackup     = "base_backup"
)

// Backup kinds (backups.kind).
const (
	Logical  = "logical"
	Final    = "final"
	Safety   = "safety"
	Metadata = "metadata"
)

// Errors for the API layer.
var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Config tunes scheduling and retention.
type Config struct {
	// Hour (UTC) the nightly window opens; Jitter spreads projects across
	// it (spec §6.3: "with jitter to spread the load").
	Hour   int
	Jitter time.Duration
	// Retention for nightly logical backups.
	Retention Retention
	// KeepSpecial is how long final and safety backups are kept (spec
	// §6.2: 30 days).
	KeepSpecial time.Duration
	// MetadataPG is the metadata DB as the agent reaches it, for
	// self-backups (spec §11.6). Zero disables them.
	MetadataPG agentapi.PGConn
}

func (c *Config) setDefaults() {
	if c.Jitter <= 0 {
		c.Jitter = 2 * time.Hour
	}
	if c.Retention == (Retention{}) {
		c.Retention = DefaultRetention
	}
	if c.KeepSpecial <= 0 {
		c.KeepSpecial = 30 * 24 * time.Hour
	}
}

// Service is the backup service.
type Service struct {
	db        *pgxpool.Pool
	keyring   *crypto.Keyring
	nodes     *nodes.Service
	projects  *provision.Service
	cfg       Config
	log       *slog.Logger
	ephemeral *Ephemeral
	// Dedicated runs base backups and point-in-time recovery; nil without
	// the dedicated tier.
	Dedicated *dedicated.Service
}

// NewService returns a Service and hooks final backups into deletes.
func NewService(db *pgxpool.Pool, keyring *crypto.Keyring, ns *nodes.Service, ps *provision.Service, cfg Config, log *slog.Logger) *Service {
	cfg.setDefaults()
	s := &Service{db: db, keyring: keyring, nodes: ns, projects: ps, cfg: cfg, log: log, ephemeral: NewEphemeral()}
	ps.FinalBackup = s.finalBackup
	return s
}

// Kinds returns the operation kinds this service runs.
func (s *Service) Kinds() map[string]jobs.Kind {
	return map[string]jobs.Kind{
		KindBackup:         {Handler: s.runBackup, MaxAttempts: 3, Timeout: 6 * time.Hour},
		KindRestore:        {Handler: s.runRestore, OnFail: s.failRestore, MaxAttempts: 2, Timeout: 6 * time.Hour},
		KindRestoreTest:    {Handler: s.runRestoreTest, MaxAttempts: 1, Timeout: 6 * time.Hour},
		KindMetadataBackup: {Handler: s.runMetadataBackup, MaxAttempts: 3, Timeout: time.Hour},
		KindImport:         {Handler: s.runImport, OnFail: s.projects.Rollback, MaxAttempts: 1, Timeout: 12 * time.Hour},
		KindBaseBackup:     {Handler: s.runBaseBackup, MaxAttempts: 3, Timeout: 12 * time.Hour},
	}
}

// objectKey is where a backup object lives under the target's prefix.
func objectKey(kind string, projectID *uuid.UUID, at time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	ts := at.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
	if projectID == nil {
		return fmt.Sprintf("metadata/%s.dump.enc", ts)
	}
	dir := Logical
	if kind != Logical {
		dir = kind
	}
	return fmt.Sprintf("projects/%s/%s/%s.dump.enc", projectID, dir, ts)
}

// dumpTo dumps pg through agent into a new backups row of kind, returning
// the finished row.
func (s *Service) dumpTo(ctx context.Context, agent *nodes.Agent, pg agentapi.PGConn, kind string, projectID *uuid.UUID, opID *uuid.UUID, log *jobs.StepLogger) (store.Backup, error) {
	targetID, target, err := s.StorageTarget(ctx)
	if err != nil {
		return store.Backup{}, jobs.Permanent(err)
	}
	bk, err := s.BackupKey(ctx)
	if err != nil {
		return store.Backup{}, jobs.Permanent(err)
	}
	fileKey, wrapped, err := backupfmt.NewFileKey(bk)
	if err != nil {
		return store.Backup{}, err
	}
	var expires *time.Time
	if kind == Final || kind == Safety {
		t := time.Now().Add(s.cfg.KeepSpecial)
		expires = &t
	}
	key := objectKey(kind, projectID, time.Now())
	q := store.New(s.db)
	row, err := q.InsertBackup(ctx, store.InsertBackupParams{
		ProjectID: projectID, Kind: kind, ObjectKey: key, StorageTargetID: &targetID,
		OperationID: opID, KeyWrapped: wrapped, ExpiresAt: expires,
	})
	if err != nil {
		return store.Backup{}, err
	}
	if err := log.Info(ctx, "dump", "dumping %s with the agent on %s", pg.Database, agent.Node.Name); err != nil {
		return store.Backup{}, err
	}
	res, err := agent.Dump(ctx, agentapi.DumpRequest{PG: pg, Upload: agentapi.Upload{
		Storage: target, ObjectKey: key, FileKey: fileKey, WrappedKey: wrapped,
	}})
	if err != nil {
		_ = q.FailBackup(context.WithoutCancel(ctx), store.FailBackupParams{ID: row.ID, Error: err.Error()})
		return store.Backup{}, err
	}
	size, sum := res.SizeBytes, res.SHA256
	row, err = q.FinishBackup(ctx, store.FinishBackupParams{ID: row.ID, SizeBytes: &size, Checksum: &sum})
	if err != nil {
		return store.Backup{}, err
	}
	return row, log.Info(ctx, "upload", "%s backup uploaded: %d bytes encrypted (%d bytes of dump) in %s, sha256 %s…",
		kind, res.SizeBytes, res.DumpBytes, (time.Duration(res.DurationMS) * time.Millisecond).Round(time.Millisecond), res.SHA256[:12])
}

// restoreFrom restores backup b into database on instance with role
// owning the objects (empty: the admin).
func (s *Service) restoreFrom(ctx context.Context, b store.Backup, instanceID uuid.UUID, database, role string, log *jobs.StepLogger) error {
	if b.Status != "succeeded" {
		return jobs.Permanent(fmt.Errorf("backup %s is %s", b.ID, b.Status))
	}
	_, target, err := s.StorageTarget(ctx)
	if err != nil {
		return jobs.Permanent(err)
	}
	bk, err := s.BackupKey(ctx)
	if err != nil {
		return jobs.Permanent(err)
	}
	fileKey, err := backupfmt.UnwrapKey(bk, b.KeyWrapped)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("backup %s: %w", b.ID, err))
	}
	agent, err := s.nodes.ForInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	pg, err := s.projects.AgentConn(ctx, instanceID, database)
	if err != nil {
		return err
	}
	if err := log.Info(ctx, "restore", "restoring backup from %s into %s", b.StartedAt.UTC().Format(time.RFC3339), database); err != nil {
		return err
	}
	res, err := agent.Restore(ctx, agentapi.RestoreRequest{
		Download: agentapi.Download{Storage: target, ObjectKey: b.ObjectKey, FileKey: fileKey},
		PG:       pg, Options: agentapi.RestoreOptions{Role: role},
	})
	if err != nil {
		return err
	}
	return log.Info(ctx, "restore", "restored in %s", (time.Duration(res.DurationMS) * time.Millisecond).Round(time.Millisecond))
}

// applyRetention drops nightly backups the policy no longer keeps.
func (s *Service) applyRetention(ctx context.Context, projectID *uuid.UUID, kind string, log *jobs.StepLogger) error {
	q := store.New(s.db)
	rows, err := q.RetentionCandidates(ctx, store.RetentionCandidatesParams{Kind: kind, ProjectID: projectID})
	if err != nil {
		return err
	}
	items := make([]Item, 0, len(rows))
	byID := map[uuid.UUID]store.Backup{}
	for _, r := range rows {
		if r.FinishedAt != nil {
			items = append(items, Item{ID: r.ID, FinishedAt: *r.FinishedAt})
			byID[r.ID] = r
		}
	}
	expired := s.cfg.Retention.Expired(items)
	if len(expired) == 0 {
		return nil
	}
	var deleted int
	for _, id := range expired {
		if err := s.deleteObject(ctx, byID[id]); err != nil {
			_ = log.Warn(ctx, "retention", "could not delete %s: %v", byID[id].ObjectKey, err)
			continue
		}
		deleted++
	}
	return log.Info(ctx, "retention", "dropped %d backup(s) outside %d daily + %d weekly", deleted, s.cfg.Retention.Daily, s.cfg.Retention.Weekly)
}

func (s *Service) deleteObject(ctx context.Context, b store.Backup) error {
	_, target, err := s.StorageTarget(ctx)
	if err != nil {
		return err
	}
	c, err := storage.New(target)
	if err != nil {
		return err
	}
	if err := c.Delete(ctx, b.ObjectKey); err != nil {
		return err
	}
	return store.New(s.db).MarkBackupDeleted(ctx, b.ID)
}

// jitter is a project's stable offset into the nightly window.
func (s *Service) jitter(id uuid.UUID) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write(id[:])
	return time.Duration(h.Sum64() % uint64(s.cfg.Jitter))
}

// windowStart is the most recent start of the nightly window at or before now.
func (s *Service) windowStart(now time.Time) time.Time {
	now = now.UTC()
	w := time.Date(now.Year(), now.Month(), now.Day(), s.cfg.Hour, 0, 0, 0, time.UTC)
	if w.After(now) {
		w = w.AddDate(0, 0, -1)
	}
	return w
}

// DueAt is when a project's backup for the window containing now is due.
func (s *Service) DueAt(id uuid.UUID, now time.Time) time.Time {
	return s.windowStart(now).Add(s.jitter(id))
}

// Schedule enqueues whatever is due at now: nightly backups (jittered),
// the metadata self-backup, the weekly restore test, and expiry of final
// and safety backups. It is safe to call often and from several servers.
func (s *Service) Schedule(ctx context.Context, now time.Time) error {
	if _, _, err := s.StorageTarget(ctx); err != nil {
		return nil //nolint:nilerr // nothing to do until storage is configured
	}
	if _, err := s.BackupKey(ctx); err != nil {
		return nil //nolint:nilerr // likewise for the backup key
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('pgdock_backup_scheduler'))`).Scan(&got); err != nil || !got {
			return err
		}
		q := store.New(tx)
		window := s.windowStart(now)
		due, err := q.ProjectsDueForBackup(ctx, window)
		if err != nil {
			return err
		}
		for _, p := range due {
			if now.Before(window.Add(s.jitter(p.ID))) {
				continue
			}
			id := p.ID
			kind, params := KindBackup, map[string]any{"kind": Logical, "scheduled": true}
			if p.Tier == provision.TierDedicated {
				// Daily base backup; WAL is archived continuously (spec §6.4).
				kind, params = KindBaseBackup, map[string]any{"scheduled": true}
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.EnqueueParams{Kind: kind, ProjectID: &id, Params: params}); err != nil {
				return err
			}
		}
		if s.cfg.MetadataPG.Host != "" {
			if last, err := q.LastOperationOfKind(ctx, KindMetadataBackup); errors.Is(err, pgx.ErrNoRows) || (err == nil && last.CreatedAt.Before(window)) {
				if _, err := jobs.Enqueue(ctx, tx, jobs.EnqueueParams{Kind: KindMetadataBackup}); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		last, err := q.LastOperationOfKind(ctx, KindRestoreTest)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && now.Sub(last.CreatedAt) >= 7*24*time.Hour) {
			if _, err := q.RandomProjectWithBackup(ctx); err == nil {
				if _, err := jobs.Enqueue(ctx, tx, jobs.EnqueueParams{Kind: KindRestoreTest}); err != nil {
					return err
				}
			}
		} else if err != nil {
			return err
		}
		return nil
	})
}

// Run schedules every minute and expires old final and safety backups
// hourly, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	lastExpiry := time.Time{}
	for {
		if err := s.Schedule(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.Warn("backup scheduler", "err", err)
		}
		if time.Since(lastExpiry) > time.Hour {
			s.expireSpecial(ctx)
			lastExpiry = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) expireSpecial(ctx context.Context) {
	rows, err := store.New(s.db).ExpiredBackups(ctx)
	if err != nil {
		return
	}
	for _, b := range rows {
		if err := s.deleteObject(ctx, b); err != nil {
			s.log.Warn("expire backup", "backup_id", b.ID, "err", err)
		}
	}
}
