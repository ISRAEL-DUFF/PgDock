package dedicated

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/logical"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// KindUpgrade moves a project to a newer Postgres major by a logical move
// into an instance of that version (V3 §2.4).
const KindUpgrade = "major_upgrade"

// UpgradePlan is a major upgrade's preflight.
type UpgradePlan struct {
	From, To  int
	SizeBytes int64
	Checks    []Check
	// Target says where the project would go: a shared cluster's node, or
	// a new dedicated instance on the project's node.
	TargetNode string
	TargetID   *uuid.UUID
	CopyMode   string
	CopyReason string
	Downtime   time.Duration
}

// Blocked lists the checks that stop the upgrade.
func (pl UpgradePlan) Blocked() []Check {
	var out []Check
	for _, c := range pl.Checks {
		if c.Status == CheckBlocked {
			out = append(out, c)
		}
	}
	return out
}

// Upgrade check names.
const (
	CheckVersion      = "version"
	CheckTarget       = "target"
	CheckReplication  = "replication"
	CheckSchemaTrial  = "schema"
	CheckExtensionsUp = "extensions"
)

// UpgradePreflight checks an upgrade of p to Postgres `to` without changing
// anything. On a shared target it restores the schema into a scratch
// database of the new version, so every incompatibility shows up here
// rather than during the upgrade.
func (s *Service) UpgradePreflight(ctx context.Context, p store.Project, to int) (UpgradePlan, error) {
	q := store.New(s.db)
	src, err := q.GetInstance(ctx, p.InstanceID)
	if err != nil {
		return UpgradePlan{}, err
	}
	plan := UpgradePlan{From: int(src.PgVersion), To: to}
	add := func(name, status, format string, a ...any) {
		plan.Checks = append(plan.Checks, Check{Name: name, Status: status, Message: fmt.Sprintf(format, a...)})
	}
	if src.HaEnabled {
		add(CheckTarget, CheckBlocked, "turn HA off before a major upgrade, and on again after it")
		return plan, nil
	}
	if v, err := s.projects.CheckPGVersion(to); err != nil {
		add(CheckVersion, CheckBlocked, "%v", err)
		return plan, nil
	} else if v <= plan.From {
		add(CheckVersion, CheckBlocked, "the project already runs Postgres %d; it can only move to a newer major", plan.From)
		return plan, nil
	}
	add(CheckVersion, CheckOK, "Postgres %d → %d", plan.From, to)

	db, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return plan, fmt.Errorf("%w: the project's database isn't reachable: %w", provision.ErrUnreachable, err)
	}
	defer db.Close(context.Background())

	var target *pgx.Conn
	if p.Tier == provision.TierShared {
		clusters, err := q.SharedClustersForOrg(ctx, store.SharedClustersForOrgParams{OrgID: &p.OrgID, PgVersion: int32(to), Region: p.Region})
		if err != nil {
			return plan, err
		}
		if len(clusters) == 0 {
			add(CheckTarget, CheckBlocked, "no shared cluster runs Postgres %d yet (Admin → Nodes → Add shared cluster)", to)
		} else {
			c := clusters[0]
			for _, x := range clusters[1:] {
				if x.Projects < c.Projects {
					c = x
				}
			}
			plan.TargetNode, plan.TargetID = c.NodeName, &c.ID
			add(CheckTarget, CheckOK, "moves to the Postgres %d shared cluster on node %s", to, c.NodeName)
			if target, err = s.projects.AdminConn(ctx, c.ID, "postgres"); err != nil {
				add(CheckTarget, CheckBlocked, "the shared cluster on %s isn't reachable: %v", c.NodeName, err)
				target = nil
			} else {
				defer target.Close(context.Background())
			}
		}
	} else {
		n, err := q.GetNode(ctx, src.NodeID)
		if err != nil {
			return plan, err
		}
		plan.TargetNode = n.Name
		add(CheckTarget, CheckOK, "a new Postgres %d instance of the same size on node %s; the old one is kept stopped for 48 hours", to, n.Name)
	}

	rep, err := logical.Preflight(ctx, db, target)
	if err != nil {
		return plan, err
	}
	plan.SizeBytes = rep.SizeBytes
	est, err := estimate(ctx, db)
	if err != nil {
		return plan, err
	}
	plan.Downtime, plan.CopyMode, plan.CopyReason = est.Downtime, est.Mode, est.Reason
	if !rep.OK() {
		// Extensions the new version lacks stop the upgrade; the rest only
		// mean a dump/restore during the pause.
		for _, b := range rep.Blockers {
			if b.Check == "extensions" {
				add(CheckExtensionsUp, CheckBlocked, "%s", b.Detail)
			}
		}
		add(CheckReplication, CheckWarning, "writes pause for the whole copy (about %s): %s", plan.Downtime, rep.Reason())
	} else {
		add(CheckReplication, CheckOK, "logical replication; writes pause for a few seconds")
	}

	if target != nil && len(plan.Blocked()) == 0 {
		errs, err := s.schemaTrial(ctx, p, db, *plan.TargetID)
		switch {
		case err != nil:
			add(CheckSchemaTrial, CheckBlocked, "couldn't test the schema on Postgres %d: %v", to, err)
		case len(errs) > 0:
			add(CheckSchemaTrial, CheckBlocked, "the schema doesn't restore on Postgres %d: %s", to, strings.Join(errs, "; "))
		default:
			add(CheckSchemaTrial, CheckOK, "the schema restores cleanly on Postgres %d", to)
		}
	} else if p.Tier == provision.TierDedicated {
		add(CheckSchemaTrial, CheckOK, "the schema is restored first on the new instance; any error stops the upgrade with the project untouched")
	}
	return plan, nil
}

// schemaTrial restores p's schema into a scratch database on target (a
// shared cluster of the new version) as a short-lived non-superuser login,
// and returns what failed. The database and login are dropped after.
func (s *Service) schemaTrial(ctx context.Context, p store.Project, src *pgx.Conn, target uuid.UUID) ([]string, error) {
	scratch := "pgdock_check_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	admin, err := s.projects.AdminConn(ctx, target, "postgres")
	if err != nil {
		return nil, err
	}
	defer admin.Close(context.Background())
	password := randomPassword()
	if _, err := admin.Exec(ctx, "CREATE ROLE "+provision.Ident(scratch)+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '"+password+"'"); err != nil {
		return nil, err
	}
	defer func() {
		c := context.WithoutCancel(ctx)
		_, _ = admin.Exec(c, "DROP DATABASE IF EXISTS "+provision.Ident(scratch)+" WITH (FORCE)")
		_, _ = admin.Exec(c, "DROP ROLE IF EXISTS "+provision.Ident(scratch))
	}()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+provision.Ident(scratch)+" OWNER "+provision.Ident(scratch)); err != nil {
		return nil, err
	}
	// Extensions first, as the superuser (the login may not create them).
	rows, err := src.Query(ctx, `SELECT extname FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	exts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	var problems []string
	if len(exts) > 0 {
		sc, err := s.projects.AdminConn(ctx, target, scratch)
		if err != nil {
			return nil, err
		}
		for _, e := range exts {
			if _, err := sc.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+provision.Ident(e)); err != nil {
				problems = append(problems, fmt.Sprintf("extension %s: %v", e, err))
			}
		}
		_ = sc.Close(context.Background())
	}
	from, err := s.projects.AgentConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return nil, err
	}
	to, err := s.projects.AgentConn(ctx, target, scratch)
	if err != nil {
		return nil, err
	}
	to.User, to.Password = scratch, password
	agent, err := s.nodes.ForInstance(ctx, target)
	if err != nil {
		return nil, err
	}
	res, err := agent.Copy(ctx, agentapi.CopyRequest{
		Source: from, Dump: agentapi.DumpOptions{ExcludeSchemas: []string{"pgdock", logical.Schema}, ExcludeExtensions: exts, SchemaOnly: true, NoOwner: true, NoACL: true},
		Target: to, Restore: agentapi.RestoreOptions{Role: scratch, AllowErrors: true},
	})
	if err != nil {
		return nil, err
	}
	for _, w := range res.Warnings {
		problems = append(problems, strings.TrimSpace(w))
	}
	return problems, nil
}

// UpgradeParams asks for a major upgrade.
type UpgradeParams struct {
	ProjectID uuid.UUID
	PgVersion int
	CreatedBy *uuid.UUID
}

// Upgrade runs the preflight again, refuses on a blocked check, and queues
// the upgrade.
func (s *Service) Upgrade(ctx context.Context, up UpgradeParams) (store.Operation, UpgradePlan, error) {
	p, err := store.New(s.db).GetProject(ctx, up.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Operation{}, UpgradePlan{}, provision.ErrNotFound
	}
	if err != nil {
		return store.Operation{}, UpgradePlan{}, err
	}
	plan, err := s.UpgradePreflight(ctx, p, up.PgVersion)
	if err != nil {
		return store.Operation{}, plan, err
	}
	if b := plan.Blocked(); len(b) > 0 {
		return store.Operation{}, plan, fmt.Errorf("%w: %s", provision.ErrConflict, summary(b))
	}
	op, err := s.projects.EnqueueExclusiveTx(ctx, up.ProjectID, []string{provision.StatusActive}, provision.StatusUpgrading, KindUpgrade, up.CreatedBy,
		func(tx pgx.Tx, pr store.Project) (any, error) {
			if pr.InstanceID != p.InstanceID {
				return nil, fmt.Errorf("%w: the project moved since the check; check again", provision.ErrConflict)
			}
			if pr.Tier == provision.TierShared {
				return moveParams{TargetInstance: *plan.TargetID, SourceInstance: pr.InstanceID}, nil
			}
			q := store.New(tx)
			src, err := q.GetInstance(ctx, pr.InstanceID)
			if err != nil {
				return nil, err
			}
			target := uuid.New()
			prefix := "instances/" + target.String() + "/wal-g"
			prof := profileOf(src)
			mem, vol := int32(prof.MemoryMB), int32(DefaultVolumeGB)
			if src.VolumeGb != nil {
				vol = *src.VolumeGb
			}
			if _, err := q.InsertInstance(ctx, store.InsertInstanceParams{
				ID: target, NodeID: src.NodeID, Kind: provision.TierDedicated, PgVersion: int32(up.PgVersion), CpuLimit: numeric(prof.CPUs),
				MemLimitMb: &mem, VolumeGb: &vol, Profile: &prof.Name, WalgPrefix: &prefix,
			}); err != nil {
				return nil, err
			}
			return moveParams{TargetInstance: target, SourceInstance: pr.InstanceID, NewInstance: true}, nil
		})
	return op, plan, err
}
