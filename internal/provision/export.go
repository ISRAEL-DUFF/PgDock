package provision

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/store"
)

// Building blocks for flows outside this package (restore, import) that
// provision a project and then fill it.

// ProjectFor loads an operation's project.
func (s *Service) ProjectFor(ctx context.Context, op store.Operation) (store.Project, error) {
	return s.project(ctx, op)
}

// Password opens the one-time password handed to an operation created by
// Create (any kind).
func (s *Service) Password(op store.Operation) (string, error) {
	if op.ProjectID == nil {
		return "", jobs.Permanent(fmt.Errorf("operation has no project"))
	}
	var params struct {
		Secrets opSecrets `json:"secrets"`
	}
	if err := json.Unmarshal(op.Params, &params); err != nil {
		return "", jobs.Permanent(err)
	}
	pw, err := s.openPassword(*op.ProjectID, op.Kind, params.Secrets)
	if err != nil {
		return "", jobs.Permanent(err)
	}
	return pw, nil
}

// Prepare creates and hardens a project's role and database (spec §6.1
// step 3). It is idempotent.
func (s *Service) Prepare(ctx context.Context, p store.Project, log *jobs.StepLogger) error {
	set, err := store.DecodeProjectSettings(p.Settings)
	if err != nil {
		return jobs.Permanent(err)
	}
	if err := s.ensureRole(ctx, p, set, log); err != nil {
		return err
	}
	if err := s.ensureDatabase(ctx, p, log); err != nil {
		return err
	}
	return s.hardenDatabase(ctx, p, log)
}

// Publish routes a prepared project through the poolers, smoke-tests it
// with password, and marks it active (spec §6.1 steps 4-6).
func (s *Service) Publish(ctx context.Context, p store.Project, password string, log *jobs.StepLogger) error {
	if err := s.SyncMemberRoles(ctx, p, log); err != nil {
		return err
	}
	if err := s.syncPooler(ctx, log, "pooler", "route and auth entry added"); err != nil {
		return err
	}
	if err := s.smokeTest(ctx, p, password, log); err != nil {
		return err
	}
	if err := store.New(s.db).SetProjectStatus(ctx, store.SetProjectStatusParams{ID: p.ID, Status: StatusActive}); err != nil {
		return err
	}
	s.Provisioned(ctx, p, log)
	return nil
}

// Rollback undoes a failed provisioning: route, database, and role. It has
// the jobs.Kind OnFail signature.
func (s *Service) Rollback(ctx context.Context, op store.Operation, log *jobs.StepLogger, cause error) error {
	return s.rollbackCreate(ctx, op, log, cause)
}

// AdminConn opens an admin connection to database on an instance.
func (s *Service) AdminConn(ctx context.Context, instanceID uuid.UUID, database string) (*pgx.Conn, error) {
	return s.connectInstance(ctx, instanceID, database)
}

// AdminConfig is the admin connection config for database on an instance,
// for callers that connect as another role at the same address.
func (s *Service) AdminConfig(ctx context.Context, instanceID uuid.UUID, database string) (*pgx.ConnConfig, error) {
	cfg, _, err := s.adminConfig(ctx, instanceID, database)
	return cfg, err
}

// AgentConn is the admin connection to database as the instance's node
// agent reaches it (the node-local address the poolers use).
func (s *Service) AgentConn(ctx context.Context, instanceID uuid.UUID, database string) (agentapi.PGConn, error) {
	t, err := s.instanceTarget(ctx, instanceID)
	if err != nil {
		return agentapi.PGConn{}, err
	}
	return agentapi.PGConn{
		Host: t.Host, Port: int(t.Port), User: t.Secret.User, Password: t.Secret.Password,
		SSLMode: s.cfg.AdminSSLMode, Database: database,
	}, nil
}

// RecreateDatabase drops a project's database and creates it empty and
// hardened again (in-place restore). Clients must already be held off.
func (s *Service) RecreateDatabase(ctx context.Context, p store.Project, log *jobs.StepLogger) error {
	conn, err := s.connectInstance(ctx, p.InstanceID, "postgres")
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(p.DbName)+" WITH (FORCE)"); err != nil {
		_ = conn.Close(ctx)
		return fmt.Errorf("drop database: %w", err)
	}
	_ = conn.Close(ctx)
	if err := s.ensureDatabase(ctx, p, log); err != nil {
		return err
	}
	return s.hardenDatabase(ctx, p, log)
}

// Pooler returns the pooler manager.
func (s *Service) Pooler() *pooler.Manager { return s.pooler }

// SyncPooler re-renders and reloads the poolers.
func (s *Service) SyncPooler(ctx context.Context, log *jobs.StepLogger, step, msg string) error {
	return s.syncPooler(ctx, log, step, msg)
}

// Ident quotes a Postgres identifier.
func Ident(name string) string { return ident(name) }

// ResetWebhooks runs after a project's database was filled from a copy (a
// restore, a branch, an import): PGDock's webhook schema that came with
// the data is dropped and, for a project with webhooks, rebuilt from its
// configuration (V2 §9.3).
func (s *Service) ResetWebhooks(ctx context.Context, p store.Project, _ *jobs.StepLogger) error {
	if s.RefreshWebhooks != nil {
		if err := s.RefreshWebhooks(ctx, p); err != nil {
			return fmt.Errorf("webhooks: %w", err)
		}
		return nil
	}
	conn, err := s.connectInstance(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	_, err = conn.Exec(ctx, "DROP SCHEMA IF EXISTS pgdock CASCADE")
	return err
}

// DropAutomation removes a deleted project's webhooks and jobs.
func (s *Service) DropAutomation(ctx context.Context, p store.Project) error {
	q := store.New(s.db)
	if err := q.DeleteProjectWebhooks(ctx, p.ID); err != nil {
		return err
	}
	return q.DeleteProjectJobs(ctx, p.ID)
}
