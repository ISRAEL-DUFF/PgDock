package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Entry points for the API layer.

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// ready checks that backups can run at all.
func (s *Service) ready(ctx context.Context) error {
	if _, _, err := s.StorageTarget(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrConflict, err)
	}
	if _, err := s.BackupKey(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrConflict, err)
	}
	return nil
}

// BackupNow queues a logical backup of a project.
func (s *Service) BackupNow(ctx context.Context, projectID uuid.UUID, by *uuid.UUID) (store.Operation, error) {
	if err := s.ready(ctx); err != nil {
		return store.Operation{}, err
	}
	return s.projects.EnqueueExclusive(ctx, projectID, []string{provision.StatusActive}, "", KindBackup,
		backupParams{Kind: Logical}, by, nil)
}

// RestoreParams describes a restore request.
type RestoreParams struct {
	BackupID uuid.UUID
	Mode     string
	// Name of the new project (ModeNew).
	Name string
	// Confirm must equal the project's name (ModeInPlace).
	Confirm   string
	CreatedBy *uuid.UUID
}

// Restore queues a restore. For ModeNew it also returns the new project's
// one-time credentials.
func (s *Service) Restore(ctx context.Context, p RestoreParams) (store.Operation, *provision.Created, error) {
	if err := s.ready(ctx); err != nil {
		return store.Operation{}, nil, err
	}
	b, err := store.New(s.db).GetBackup(ctx, p.BackupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Operation{}, nil, ErrNotFound
	}
	if err != nil {
		return store.Operation{}, nil, err
	}
	if b.Status != "succeeded" || b.ProjectID == nil || b.Kind == Metadata {
		return store.Operation{}, nil, fmt.Errorf("%w: only succeeded project backups can be restored", ErrInvalid)
	}
	params := restoreParams{Mode: p.Mode, BackupID: b.ID}
	switch p.Mode {
	case "", ModeNew:
		params.Mode = ModeNew
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return store.Operation{}, nil, fmt.Errorf("%w: name the new project", ErrInvalid)
		}
		c, err := s.projects.Create(ctx, provision.CreateParams{
			Name: name, CreatedBy: p.CreatedBy, Kind: KindRestore,
			Params: map[string]any{"mode": params.Mode, "backup_id": params.BackupID},
		})
		if err != nil {
			return store.Operation{}, nil, err
		}
		return c.Operation, &c, nil
	case ModeInPlace:
		op, err := s.projects.EnqueueExclusive(ctx, *b.ProjectID, []string{provision.StatusActive}, provision.StatusRestoring,
			KindRestore, params, p.CreatedBy, func(pr store.Project) error {
				if p.Confirm != pr.Name {
					return fmt.Errorf("%w: confirm must match the project name exactly", provision.ErrInvalid)
				}
				return nil
			})
		return op, nil, err
	default:
		return store.Operation{}, nil, fmt.Errorf("%w: mode must be new or in_place", ErrInvalid)
	}
}

// TestRestoreNow queues a restore test, of projectID or a random project.
func (s *Service) TestRestoreNow(ctx context.Context, projectID *uuid.UUID, by *uuid.UUID) (store.Operation, error) {
	if err := s.ready(ctx); err != nil {
		return store.Operation{}, err
	}
	if projectID != nil {
		if _, err := s.projects.Get(ctx, *projectID); err != nil {
			return store.Operation{}, err
		}
	}
	return jobs.Enqueue(ctx, s.db, jobs.EnqueueParams{Kind: KindRestoreTest, ProjectID: projectID, CreatedBy: by})
}

// Overview summarises backup health.
type Overview struct {
	StorageConfigured  bool
	Key                KeyInfo
	AgentAvailable     bool
	LastRestoreTest    *store.Operation
	LastMetadataBackup *store.Backup
}

// Overview reports backup configuration and the latest checks.
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	var o Overview
	_, _, err := s.StorageTarget(ctx)
	o.StorageConfigured = err == nil
	if o.Key, err = s.KeyInfo(ctx); err != nil {
		return o, err
	}
	_, err = s.nodes.Any(ctx)
	o.AgentAvailable = err == nil
	q := store.New(s.db)
	if op, err := q.LastOperationOfKind(ctx, KindRestoreTest); err == nil {
		o.LastRestoreTest = &op
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return o, err
	}
	if rows, err := q.RetentionCandidates(ctx, store.RetentionCandidatesParams{Kind: Metadata}); err == nil && len(rows) > 0 {
		o.LastMetadataBackup = &rows[0]
	} else if err != nil {
		return o, err
	}
	return o, nil
}
