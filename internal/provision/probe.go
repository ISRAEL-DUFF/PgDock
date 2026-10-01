package provision

import (
	"context"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/store"
)

// probePrefix names the throwaway tenants of the isolation check. Real
// databases end in _xxxx (a 4-character suffix), so these 8-character
// ones never collide with a project.
const probePrefix = "pgdock_isocheck_"

// ProbeName matches a probe's database or role name.
var ProbeName = regexp.MustCompile(`^pgdock_isocheck_[a-z0-9]{8}(_owner)?$`)

// NewProbe provisions a throwaway tenant on a shared instance exactly as a
// project's role and database are (spec §7.1 isolation check), without a
// projects row or a pooler route. Drop it with DropProbe.
func (s *Service) NewProbe(ctx context.Context, instanceID uuid.UUID, log *jobs.StepLogger) (store.Project, string, error) {
	suffix, err := randomSuffix(8)
	if err != nil {
		return store.Project{}, "", err
	}
	password, err := GeneratePassword()
	if err != nil {
		return store.Project{}, "", err
	}
	verifier, err := crypto.SCRAMVerifier(password)
	if err != nil {
		return store.Project{}, "", err
	}
	db := probePrefix + suffix
	p := store.Project{ID: uuid.New(), DbName: db, OwnerRole: db + "_owner", InstanceID: instanceID, Tier: TierShared, ScramVerifier: verifier}

	conn, err := s.connectInstance(ctx, instanceID, "postgres")
	if err != nil {
		return p, "", err
	}
	var taken bool
	err = conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1) OR EXISTS (SELECT 1 FROM pg_database WHERE datname = $2)`,
		p.OwnerRole, p.DbName).Scan(&taken)
	_ = conn.Close(context.Background())
	if err != nil {
		return p, "", err
	}
	if taken {
		return p, "", fmt.Errorf("probe name %s is taken", db)
	}
	if err := s.ensureRole(ctx, p, store.DefaultSharedSettings(), log); err != nil {
		return p, "", err
	}
	if err := s.ensureDatabase(ctx, p, log); err != nil {
		return p, "", err
	}
	if err := s.hardenDatabase(ctx, p, log); err != nil {
		return p, "", err
	}
	return p, password, nil
}

// DropProbes removes every probe tenant on an instance (this run's and any
// an interrupted run left behind).
func (s *Service) DropProbes(ctx context.Context, instanceID uuid.UUID) error {
	conn, err := s.connectInstance(ctx, instanceID, "postgres")
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE 'pgdock\_isocheck\_%'`)
	if err != nil {
		return err
	}
	dbs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, db := range dbs {
		if !ProbeName.MatchString(db) {
			continue
		}
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident(db)+" WITH (FORCE)"); err != nil {
			return err
		}
	}
	rows, err = conn.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname LIKE 'pgdock\_isocheck\_%'`)
	if err != nil {
		return err
	}
	roles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, r := range roles {
		if !ProbeName.MatchString(r) {
			continue
		}
		if _, err := conn.Exec(ctx, "DROP ROLE IF EXISTS "+ident(r)); err != nil {
			return err
		}
	}
	return nil
}
