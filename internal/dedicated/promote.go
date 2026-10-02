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
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/pgverify"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// KindPromote moves a shared project onto a dedicated instance (spec §6.6).
const KindPromote = "promote"

// RetainSource is how long a promoted project's shared copy is kept,
// read-only, before it is dropped (spec §6.6 step 8).
const RetainSource = 48 * time.Hour

// freezeWait bounds how long PAUSE may wait for in-flight transactions
// before the pooler drops the remaining clients instead.
const freezeWait = 10 * time.Second

// Throughput assumed for the downtime estimate (dump and restore stream
// through each other, so roughly the slower of the two).
const estimateBytesPerSecond = 40 << 20

// Estimate is the expected write-freeze of a promotion.
type Estimate struct {
	SizeBytes int64
	Downtime  time.Duration
}

// EstimatePromotion sizes a shared project's database and estimates the
// freeze (spec §6.6 step 1: "estimated downtime from the DB size").
func (s *Service) EstimatePromotion(ctx context.Context, p store.Project) (Estimate, error) {
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, "postgres")
	if err != nil {
		return Estimate{}, err
	}
	defer conn.Close(context.Background())
	var size int64
	if err := conn.QueryRow(ctx, `SELECT pg_database_size($1)`, p.DbName).Scan(&size); err != nil {
		return Estimate{}, err
	}
	// Fixed costs: pausing, terminating, verifying, and the route swap.
	d := 5*time.Second + time.Duration(float64(size)/estimateBytesPerSecond*float64(time.Second))
	return Estimate{SizeBytes: size, Downtime: d.Round(time.Second)}, nil
}

// PromoteParams asks for a promotion.
type PromoteParams struct {
	ProjectID uuid.UUID
	NodeID    *uuid.UUID
	Profile   string
	VolumeGB  int
	CreatedBy *uuid.UUID
}

type promoteParams struct {
	TargetInstance uuid.UUID `json:"target_instance"`
	SourceInstance uuid.UUID `json:"source_instance"`
}

// Promote validates a promotion, records the target instance, and queues
// the operation. The project stays served throughout, except for the
// write freeze while the data moves.
func (s *Service) Promote(ctx context.Context, p PromoteParams) (store.Operation, error) {
	cp := provision.CreateParams{NodeID: p.NodeID, Profile: p.Profile, VolumeGB: p.VolumeGB}
	prof, err := s.Validate(ctx, &cp)
	if err != nil {
		return store.Operation{}, err
	}
	return s.projects.EnqueueExclusiveTx(ctx, p.ProjectID, []string{provision.StatusActive}, provision.StatusPromoting, KindPromote, p.CreatedBy,
		func(tx pgx.Tx, pr store.Project) (any, error) {
			if pr.Tier != provision.TierShared {
				return nil, fmt.Errorf("%w: only shared projects can be promoted", provision.ErrInvalid)
			}
			target := uuid.New()
			prefix := "instances/" + target.String() + "/wal-g"
			mem := int32(prof.MemoryMB)
			vol := int32(cp.VolumeGB)
			if _, err := store.New(tx).InsertInstance(ctx, store.InsertInstanceParams{
				ID: target, NodeID: *cp.NodeID, Kind: provision.TierDedicated, CpuLimit: numeric(prof.CPUs),
				MemLimitMb: &mem, VolumeGb: &vol, Profile: &prof.Name, WalgPrefix: &prefix,
			}); err != nil {
				return nil, err
			}
			return promoteParams{TargetInstance: target, SourceInstance: pr.InstanceID}, nil
		})
}

// numeric converts a float for a numeric column.
func numeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(strconv.FormatFloat(f, 'f', -1, 64))
	return n
}

func opPromote(op store.Operation) (promoteParams, error) {
	var p promoteParams
	if err := json.Unmarshal(op.Params, &p); err != nil || p.TargetInstance == uuid.Nil {
		return p, jobs.Permanent(fmt.Errorf("promote params: %w", err))
	}
	return p, nil
}

// promotedSettings are the project's settings on its dedicated instance:
// its timeouts stay; limits, pool, disk warning, and the console follow the
// dedicated defaults (spec §4.3; §8.5: promotion turns the read-only
// console on).
func promotedSettings(cur store.ProjectSettings, volumeGB int) store.ProjectSettings {
	d := store.DefaultDedicatedSettings(volumeGB)
	cur.ConnectionLimit = max(cur.ConnectionLimit, d.ConnectionLimit)
	cur.PoolSize = max(cur.PoolSize, d.PoolSize)
	cur.DiskWarnBytes = d.DiskWarnBytes
	cur.ConsoleReadOnly = true
	return cur
}

// runPromote implements spec §6.6 steps 2-8.
func (s *Service) runPromote(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	params, err := opPromote(op)
	if err != nil {
		return err
	}
	q := store.New(s.db)
	p, err := q.GetProject(ctx, *op.ProjectID)
	if err != nil {
		return jobs.Permanent(err)
	}
	if p.InstanceID == params.TargetInstance {
		// A retry after the cutover committed: only finishing steps remain.
		return s.finishPromotion(ctx, p, log, time.Time{})
	}
	if p.Status != provision.StatusPromoting {
		return jobs.Permanent(fmt.Errorf("project is %s, not promoting", p.Status))
	}
	inst, err := q.GetInstance(ctx, params.TargetInstance)
	if err != nil {
		return jobs.Permanent(err)
	}
	cur, err := store.DecodeProjectSettings(p.Settings)
	if err != nil {
		return jobs.Permanent(err)
	}
	vol := DefaultVolumeGB
	if inst.VolumeGb != nil {
		vol = int(*inst.VolumeGb)
	}
	settings, err := json.Marshal(promotedSettings(cur, vol))
	if err != nil {
		return err
	}
	// The project as it will be on the target: same role, verifier, and
	// database name on the new instance.
	pt := p
	pt.InstanceID, pt.Tier, pt.Settings = inst.ID, provision.TierDedicated, settings

	// Step 2: the instance, and the role with the same SCRAM verifier.
	if err := s.Ensure(ctx, store.Operation{}, pt, log); err != nil {
		return err
	}
	if err := s.projects.Prepare(ctx, pt, log); err != nil {
		return err
	}
	// A retry may find a partial copy: start from an empty database.
	if op.Attempts > 1 {
		if err := s.projects.RecreateDatabase(ctx, pt, log); err != nil {
			return err
		}
	}
	// Members' logins and the read-only role, with the same verifiers, so
	// the copy's grants land and every member's credentials keep working
	// (V2 §3.5).
	if err := s.projects.SyncMemberRoles(ctx, pt, log); err != nil {
		return err
	}
	src, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return err
	}
	defer src.Close(context.Background())
	if err := s.placeholderRoles(ctx, src, pt, p.OwnerRole, log); err != nil {
		return err
	}

	// Step 3: freeze.
	start := time.Now()
	if err := s.freeze(ctx, p, log); err != nil {
		return err
	}
	if s.cfg.AfterFreeze != nil {
		if err := s.cfg.AfterFreeze(ctx); err != nil {
			return jobs.Permanent(err)
		}
	}

	// Step 4: copy, owners and grants as they are.
	agent, err := s.nodes.ForNode(ctx, inst.NodeID)
	if err != nil {
		return err
	}
	from, err := s.projects.AgentConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return err
	}
	to, err := s.projects.AgentConn(ctx, inst.ID, p.DbName)
	if err != nil {
		return err
	}
	res, err := agent.Copy(ctx, agentapi.CopyRequest{Source: from, Target: to, Restore: agentapi.RestoreOptions{KeepOwners: true}})
	if err != nil {
		return jobs.Permanent(fmt.Errorf("copy: %w", err))
	}
	if err := log.Info(ctx, "copy", "pg_dump | pg_restore on %s in %s", agent.Node.Name,
		(time.Duration(res.DurationMS) * time.Millisecond).Round(time.Millisecond)); err != nil {
		return err
	}

	// Step 5: verify.
	dst, err := s.projects.AdminConn(ctx, inst.ID, p.DbName)
	if err != nil {
		return err
	}
	defer dst.Close(context.Background())
	v, err := pgverify.Compare(ctx, src, dst, nil)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	if !v.OK() {
		return jobs.Permanent(fmt.Errorf("verification failed: %s", v.Summary()))
	}
	if err := log.Info(ctx, "verify", "verified: %d table(s), %d row(s), and %d sequence(s) match", v.Tables, v.Rows, v.Sequences); err != nil {
		return err
	}
	if err := s.projects.SyncMemberRoles(ctx, pt, nil); err != nil {
		return err
	}

	// Step 6: switch the route. The commit is the point of no return.
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tq := store.New(tx)
		if err := tq.CutOverProject(ctx, store.CutOverProjectParams{ID: p.ID, InstanceID: inst.ID, Settings: settings}); err != nil {
			return err
		}
		_, err := tq.InsertRetiredDatabase(ctx, store.InsertRetiredDatabaseParams{
			ProjectID: p.ID, InstanceID: p.InstanceID, DbName: p.DbName, OwnerRole: p.OwnerRole,
			Reason: "promotion", DropAfter: time.Now().Add(RetainSource),
		})
		return err
	})
	if err != nil {
		return err
	}
	if err := log.Info(ctx, "cutover", "route now points at the dedicated instance"); err != nil {
		return err
	}
	p, err = q.GetProject(ctx, p.ID)
	if err != nil {
		return err
	}
	return s.finishPromotion(ctx, p, log, start)
}

// freeze stops writes to the shared copy: the role can no longer log in,
// the poolers hold its clients, and remaining sessions end (spec §6.6
// step 3; NOLOGIN instead of ALLOW_CONNECTIONS false, which would lock out
// pg_dump too).
func (s *Service) freeze(ctx context.Context, p store.Project, log *jobs.StepLogger) error {
	admin, err := s.projects.AdminConn(ctx, p.InstanceID, "postgres")
	if err != nil {
		return err
	}
	defer admin.Close(context.Background())
	logins, err := s.memberLogins(ctx, p)
	if err != nil {
		return err
	}
	for _, r := range append([]string{p.OwnerRole}, logins...) {
		if _, err := admin.Exec(ctx, "ALTER ROLE "+provision.Ident(r)+" NOLOGIN"); err != nil {
			return fmt.Errorf("freeze: %w", err)
		}
	}
	killed, err := s.projects.Pooler().Freeze(ctx, p.DbName, freezeWait)
	if err != nil {
		return fmt.Errorf("freeze pooler: %w", err)
	}
	var n int
	// The console's role too: it can SET ROLE to the owner while NOLOGIN.
	if err := admin.QueryRow(ctx, `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE datname = $1 AND usename = ANY($2)`,
		p.DbName, append([]string{p.OwnerRole, provision.ConsoleRole(p.DbName)}, logins...)).Scan(&n); err != nil {
		return fmt.Errorf("terminate sessions: %w", err)
	}
	msg := "writes frozen: poolers paused"
	if len(killed) > 0 {
		msg = fmt.Sprintf("writes frozen: poolers paused (%v: sessions dropped, reconnects wait)", strings.Join(killed, ", "))
	}
	return log.Info(ctx, "freeze", "%s, %d backend session(s) ended", msg, n)
}

// unfreeze lets the shared copy serve again (rollback).
func (s *Service) unfreeze(ctx context.Context, p store.Project, log *jobs.StepLogger) error {
	admin, err := s.projects.AdminConn(ctx, p.InstanceID, "postgres")
	if err != nil {
		return err
	}
	defer admin.Close(context.Background())
	logins, err := s.memberLogins(ctx, p)
	if err != nil {
		return err
	}
	for _, r := range append([]string{p.OwnerRole}, logins...) {
		if _, err := admin.Exec(ctx, "ALTER ROLE "+provision.Ident(r)+" LOGIN"); err != nil {
			return err
		}
	}
	if err := s.projects.Pooler().Resume(ctx, p.DbName); err != nil && !isNotPaused(err) {
		return err
	}
	return log.Warn(ctx, "rollback", "shared copy writable again; route resumed")
}

func isNotPaused(err error) bool { return err != nil && strings.Contains(err.Error(), "is not paused") }

// finishPromotion re-renders the route, resumes the poolers, marks the
// project active, leaves the shared copy read-only, and takes the first
// base backup (spec §6.6 steps 6-8). It is idempotent.
func (s *Service) finishPromotion(ctx context.Context, p store.Project, log *jobs.StepLogger, frozeAt time.Time) error {
	if err := s.projects.SyncPooler(ctx, log, "pooler", "route switched to the dedicated instance"); err != nil {
		return err
	}
	if err := s.projects.Pooler().Resume(ctx, p.DbName); err != nil && !isNotPaused(err) {
		return fmt.Errorf("pooler RESUME: %w", err)
	}
	if err := store.New(s.db).SetProjectStatus(ctx, store.SetProjectStatusParams{ID: p.ID, Status: provision.StatusActive}); err != nil {
		return err
	}
	msg := "resumed; clients now reach the dedicated instance with the same URL"
	if !frozeAt.IsZero() {
		msg += fmt.Sprintf(" (writes were frozen for %s)", time.Since(frozeAt).Round(100*time.Millisecond))
	}
	if err := log.Info(ctx, "pooler", "%s", msg); err != nil {
		return err
	}
	if r, err := store.New(s.db).LiveRetiredForProject(ctx, p.ID); err == nil {
		if conn, err := s.projects.AdminConn(ctx, r.InstanceID, "postgres"); err == nil {
			_, err = conn.Exec(ctx, "ALTER DATABASE "+provision.Ident(r.DbName)+" SET default_transaction_read_only = on")
			_ = conn.Close(context.Background())
			if err == nil {
				_ = log.Info(ctx, "source", "shared copy kept read-only until %s, then dropped", r.DropAfter.UTC().Format(time.RFC3339))
			}
		}
	}
	s.projects.Provisioned(ctx, p, log)
	return log.Info(ctx, "done", "project promoted to the dedicated tier")
}

// failPromote is the rollback (spec §6.6): before the cutover, the shared
// copy serves again and the new instance goes; after it, there is nothing
// to undo, only the route to resume.
func (s *Service) failPromote(ctx context.Context, op store.Operation, log *jobs.StepLogger, _ error) error {
	params, err := opPromote(op)
	if err != nil {
		return err
	}
	q := store.New(s.db)
	p, err := q.GetProject(ctx, *op.ProjectID)
	if err != nil {
		return err
	}
	if p.InstanceID == params.TargetInstance {
		return s.finishPromotion(ctx, p, log, time.Time{})
	}
	if err := s.unfreeze(ctx, p, log); err != nil {
		return err
	}
	if err := q.SetProjectStatus(ctx, store.SetProjectStatusParams{ID: p.ID, Status: provision.StatusActive}); err != nil {
		return err
	}
	if inst, err := q.GetInstance(ctx, params.TargetInstance); err == nil {
		if err := s.destroyInstance(ctx, inst, log); err != nil {
			_ = log.Warn(ctx, "rollback", "could not remove the new instance: %v", err)
		}
	}
	return log.Warn(ctx, "rollback", "promotion rolled back; the project stays on the shared tier with no data lost")
}

// placeholderRoles creates, on the target, NOLOGIN stand-ins for roles the
// source database's grants, policies, and owners name (e.g. an imported
// project's anon/authenticated), so the dump restores exactly.
func (s *Service) placeholderRoles(ctx context.Context, src *pgx.Conn, pt store.Project, owner string, log *jobs.StepLogger) error {
	rows, err := src.Query(ctx, `
		WITH ids AS (
		  SELECT (aclexplode(relacl)).grantee AS id FROM pg_class WHERE relacl IS NOT NULL
		  UNION SELECT (aclexplode(nspacl)).grantee FROM pg_namespace WHERE nspacl IS NOT NULL
		  UNION SELECT (aclexplode(proacl)).grantee FROM pg_proc WHERE proacl IS NOT NULL
		  UNION SELECT (aclexplode(typacl)).grantee FROM pg_type WHERE typacl IS NOT NULL
		  UNION SELECT relowner FROM pg_class
		  UNION SELECT nspowner FROM pg_namespace
		  UNION SELECT proowner FROM pg_proc
		)
		SELECT rolname FROM pg_roles WHERE oid IN (SELECT id FROM ids)
		UNION SELECT unnest(roles)::text FROM pg_policies`)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	dst, err := s.projects.AdminConn(ctx, pt.InstanceID, "postgres")
	if err != nil {
		return err
	}
	defer dst.Close(context.Background())
	var made []string
	for _, n := range names {
		if n == owner || n == "public" || strings.HasPrefix(n, "pg_") {
			continue
		}
		var exists bool
		if err := dst.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, n).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := dst.Exec(ctx, "CREATE ROLE "+provision.Ident(n)+" NOLOGIN NOINHERIT"); err != nil {
			return fmt.Errorf("placeholder role %s: %w", n, err)
		}
		made = append(made, n)
	}
	if len(made) > 0 {
		return log.Info(ctx, "roles", "created NOLOGIN stand-ins for %v on the instance", made)
	}
	return nil
}

// memberLogins lists the personal logins of p.
func (s *Service) memberLogins(ctx context.Context, p store.Project) ([]string, error) {
	users, err := store.New(s.db).ListProjectDBUsers(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.RoleName
	}
	return out, nil
}

// DropRetired drops shared copies whose retention ended (spec §6.6 step 8).
func (s *Service) DropRetired(ctx context.Context) error {
	q := store.New(s.db)
	due, err := q.DueRetiredDatabases(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range due {
		conn, err := s.projects.AdminConn(ctx, r.InstanceID, "postgres")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, err = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+provision.Ident(r.DbName)+" WITH (FORCE)")
		if err == nil {
			// The role only owned this database on the shared cluster.
			_, err = conn.Exec(ctx, "DROP ROLE IF EXISTS "+provision.Ident(r.OwnerRole))
		}
		if err == nil {
			err = provision.DropConsoleRole(ctx, conn, provision.ConsoleRole(r.DbName), r.DbName)
		}
		if err == nil {
			// Members' logins and the read-only role belonged to this copy.
			var names []string
			rows, qerr := conn.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname = $1 OR starts_with(rolname, $2)`,
				provision.ReadOnlyRole(r.DbName), r.DbName+"_u_")
			if qerr == nil {
				names, qerr = pgx.CollectRows(rows, pgx.RowTo[string])
			}
			err = qerr
			for _, n := range names {
				if err == nil {
					_, err = conn.Exec(ctx, "DROP ROLE IF EXISTS "+provision.Ident(n))
				}
			}
		}
		_ = conn.Close(context.Background())
		if err != nil {
			errs = append(errs, fmt.Errorf("drop %s: %w", r.DbName, err))
			continue
		}
		if err := q.MarkRetiredDropped(ctx, r.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		s.log.Info("dropped retired shared copy", "database", r.DbName, "project_id", r.ProjectID)
	}
	return errors.Join(errs...)
}
