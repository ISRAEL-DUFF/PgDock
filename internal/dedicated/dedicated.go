// Package dedicated runs the dedicated tier (spec §4.2): one Postgres
// container per project, created on a node by its agent, with WAL-G
// continuous archiving, daily base backups, and point-in-time recovery into
// a new project (§6.4, §6.5).
package dedicated

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/walg"
)

// Secrets supplies backup storage and the backup key (the backup service).
type Secrets interface {
	StorageTarget(ctx context.Context) (uuid.UUID, storage.Target, error)
	BackupKey(ctx context.Context) ([]byte, error)
}

// Config tunes the tier.
type Config struct {
	// AdminVia is how the control plane reaches instances: "network" (the
	// host the agent reports, e.g. a container name on a shared Docker
	// network; the default) or "published" (the port the agent publishes on
	// the node).
	AdminVia string
	// RetainFull is how many full base backups WAL-G keeps (spec §6.4).
	RetainFull int
	// ReadyTimeout bounds waiting for a restored instance to promote.
	ReadyTimeout time.Duration
	// AfterFreeze, if set, runs during a promotion right after writes are
	// frozen; an error fails the promotion there (tests use it to exercise
	// the rollback).
	AfterFreeze func(ctx context.Context) error
}

// Service manages dedicated instances.
type Service struct {
	db       *pgxpool.Pool
	keyring  *crypto.Keyring
	nodes    *nodes.Service
	projects *provision.Service
	secrets  Secrets
	cfg      Config
	log      *slog.Logger
}

// New returns a Service.
func New(db *pgxpool.Pool, keyring *crypto.Keyring, ns *nodes.Service, ps *provision.Service, secrets Secrets, cfg Config, log *slog.Logger) *Service {
	if cfg.AdminVia == "" {
		cfg.AdminVia = "network"
	}
	if cfg.RetainFull <= 0 {
		cfg.RetainFull = 7
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 15 * time.Minute
	}
	return &Service{db: db, keyring: keyring, nodes: ns, projects: ps, secrets: secrets, cfg: cfg, log: log}
}

var _ provision.InstanceManager = (*Service)(nil)

// Profiles are the instance sizes on offer.
var Profiles = []provision.Profile{
	{Name: "small", CPUs: 1, MemoryMB: 1024},
	{Name: "medium", CPUs: 2, MemoryMB: 4096},
	{Name: "large", CPUs: 4, MemoryMB: 8192},
}

// DefaultProfile and DefaultVolumeGB apply when a request names none.
const (
	DefaultProfile  = "small"
	DefaultVolumeGB = 20
)

// ProfileByName finds a profile.
func ProfileByName(name string) (provision.Profile, bool) {
	for _, p := range Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return provision.Profile{}, false
}

// settings is postgresql.conf for a profile (spec §4.2: its own tuning).
func settings(p provision.Profile) map[string]string {
	mb := func(n int) string { return strconv.Itoa(n) + "MB" }
	return map[string]string{
		"max_connections":            "100",
		"shared_buffers":             mb(p.MemoryMB / 4),
		"effective_cache_size":       mb(p.MemoryMB * 3 / 4),
		"maintenance_work_mem":       mb(min(max(p.MemoryMB/16, 64), 2048)),
		"work_mem":                   "8MB",
		"wal_compression":            "on",
		"checkpoint_timeout":         "15min",
		"max_wal_size":               "2GB",
		"shared_preload_libraries":   "pg_stat_statements",
		"log_min_duration_statement": "5s",
	}
}

// Validate implements provision.InstanceManager.
func (s *Service) Validate(ctx context.Context, p *provision.CreateParams) (provision.Profile, error) {
	if p.Profile == "" {
		p.Profile = DefaultProfile
	}
	prof, ok := ProfileByName(p.Profile)
	if !ok {
		return prof, fmt.Errorf("%w: unknown profile %q", provision.ErrInvalid, p.Profile)
	}
	if p.VolumeGB == 0 {
		p.VolumeGB = DefaultVolumeGB
	}
	if p.VolumeGB < 1 || p.VolumeGB > 16384 {
		return prof, fmt.Errorf("%w: volume must be 1 to 16384 GB", provision.ErrInvalid)
	}
	// WAL-G needs somewhere to archive to from the first second.
	if _, _, err := s.secrets.StorageTarget(ctx); err != nil {
		return prof, fmt.Errorf("%w: dedicated projects archive WAL continuously: %w", provision.ErrConflict, err)
	}
	if _, err := s.secrets.BackupKey(ctx); err != nil {
		return prof, fmt.Errorf("%w: dedicated projects archive WAL continuously: %w", provision.ErrConflict, err)
	}
	q := store.New(s.db)
	if p.NodeID == nil {
		n, err := q.PickDedicatedNode(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return prof, fmt.Errorf("%w: no healthy node with an agent accepts dedicated instances", provision.ErrNoCapacity)
		}
		if err != nil {
			return prof, err
		}
		p.NodeID = &n.ID
		return prof, nil
	}
	n, err := q.GetNode(ctx, *p.NodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return prof, fmt.Errorf("%w: no such node", provision.ErrInvalid)
	}
	if err != nil {
		return prof, err
	}
	if n.Role != "dedicated" && n.Role != "both" {
		return prof, fmt.Errorf("%w: node %s does not take dedicated instances (role %s)", provision.ErrInvalid, n.Name, n.Role)
	}
	if n.AgentCertFp == nil {
		return prof, fmt.Errorf("%w: node %s has no agent", provision.ErrConflict, n.Name)
	}
	return prof, nil
}

// pitrParams is the "pitr" entry of a restore operation's params.
type pitrParams struct {
	SourceProject  uuid.UUID `json:"source_project"`
	SourceInstance uuid.UUID `json:"source_instance"`
	BackupID       uuid.UUID `json:"backup_id"`
	BackupName     string    `json:"backup_name"`
	TargetTime     string    `json:"target_time,omitempty"`
}

func opPITR(op store.Operation) (*pitrParams, error) {
	var p struct {
		PITR *pitrParams `json:"pitr"`
	}
	if len(op.Params) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(op.Params, &p); err != nil {
		return nil, jobs.Permanent(err)
	}
	return p.PITR, nil
}

// walgFor is where an instance archives (its walg_prefix).
func (s *Service) walgFor(ctx context.Context, inst store.Instance) (agentapi.WALG, error) {
	if inst.WalgPrefix == nil {
		return agentapi.WALG{}, fmt.Errorf("instance %s has no WAL-G prefix", inst.ID)
	}
	_, target, err := s.secrets.StorageTarget(ctx)
	if err != nil {
		return agentapi.WALG{}, jobs.Permanent(err)
	}
	bk, err := s.secrets.BackupKey(ctx)
	if err != nil {
		return agentapi.WALG{}, jobs.Permanent(err)
	}
	key, err := walg.PGPKey(bk)
	if err != nil {
		return agentapi.WALG{}, err
	}
	return agentapi.WALG{Storage: target, Prefix: *inst.WalgPrefix, PGPKey: key}, nil
}

func randomPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func profileOf(inst store.Instance) provision.Profile {
	p := provision.Profile{Name: DefaultProfile, CPUs: 1, MemoryMB: 1024}
	if inst.Profile != nil {
		if pp, ok := ProfileByName(*inst.Profile); ok {
			p = pp
		}
	}
	if inst.MemLimitMb != nil {
		p.MemoryMB = int(*inst.MemLimitMb)
	}
	if f, err := inst.CpuLimit.Float64Value(); err == nil && f.Valid {
		p.CPUs = f.Float64
	}
	return p
}

// Ensure implements provision.InstanceManager: it creates the project's
// container (restoring it for a point-in-time recovery), records how to
// reach it, and hardens it.
func (s *Service) Ensure(ctx context.Context, op store.Operation, p store.Project, log *jobs.StepLogger) error {
	q := store.New(s.db)
	inst, err := q.GetInstance(ctx, p.InstanceID)
	if err != nil {
		return err
	}
	pitr, err := opPITR(op)
	if err != nil {
		return err
	}
	if inst.Status == "running" {
		if err := s.adoptRestore(ctx, inst, p, pitr, log); err != nil {
			return err
		}
		return log.Info(ctx, "instance", "instance already running")
	}

	// The superuser: generated, or for a restore the source's (it is in the
	// restored data).
	if len(inst.AdminSecret) == 0 {
		secret := provision.AdminSecret{User: "pgdock_admin", Password: randomPassword()}
		if pitr != nil {
			src, err := q.GetInstance(ctx, pitr.SourceInstance)
			if err != nil {
				return jobs.Permanent(fmt.Errorf("source instance: %w", err))
			}
			if secret, err = provision.OpenInstanceSecret(s.keyring, src.ID, src.AdminSecret); err != nil {
				return jobs.Permanent(err)
			}
		}
		sealed, err := provision.SealInstanceSecret(s.keyring, inst.ID, secret)
		if err != nil {
			return err
		}
		if err := q.SetInstanceAdminSecret(ctx, store.SetInstanceAdminSecretParams{ID: inst.ID, AdminSecret: sealed}); err != nil {
			return err
		}
		inst.AdminSecret = sealed
	}
	secret, err := provision.OpenInstanceSecret(s.keyring, inst.ID, inst.AdminSecret)
	if err != nil {
		return jobs.Permanent(err)
	}
	w, err := s.walgFor(ctx, inst)
	if err != nil {
		return err
	}
	prof := profileOf(inst)
	spec := agentapi.InstanceSpec{
		ID: inst.ID.String(), Kind: agentapi.InstanceDedicated, CPUs: prof.CPUs, MemoryMB: prof.MemoryMB,
		AdminUser: secret.User, AdminPassword: secret.Password, Settings: settings(prof), WALG: &w,
	}
	if pitr != nil {
		src, err := q.GetInstance(ctx, pitr.SourceInstance)
		if err != nil {
			return jobs.Permanent(fmt.Errorf("source instance: %w", err))
		}
		srcWALG, err := s.walgFor(ctx, src)
		if err != nil {
			return err
		}
		spec.Restore = &agentapi.WALGRestore{Source: srcWALG, BackupName: pitr.BackupName, TargetTime: pitr.TargetTime}
		target := "the end of the archive"
		if pitr.TargetTime != "" {
			target = pitr.TargetTime
		}
		if err := log.Info(ctx, "restore", "fetching base backup %s and replaying WAL to %s", pitr.BackupName, target); err != nil {
			return err
		}
	}
	agent, err := s.nodes.ForNode(ctx, inst.NodeID)
	if err != nil {
		return err
	}
	if err := log.Info(ctx, "instance", "starting a %s instance (%g CPU, %d MB) on node %s", prof.Name, prof.CPUs, prof.MemoryMB, agent.Node.Name); err != nil {
		return err
	}
	start := time.Now()
	res, err := agent.CreateInstance(ctx, spec)
	if err != nil {
		_ = q.SetInstanceStatus(context.WithoutCancel(ctx), store.SetInstanceStatusParams{ID: inst.ID, Status: "provisioning", Error: ptr(err.Error())})
		return err
	}
	if res.Host == "" {
		return jobs.Permanent(fmt.Errorf("agent on %s reports no address for the instance (set PGDOCK_AGENT_NETWORK or PGDOCK_AGENT_PUBLISH)", agent.Node.Name))
	}
	run := store.SetInstanceRunningParams{ID: inst.ID, ContainerID: &res.ContainerID, Host: &res.Host, Port: int32(res.Port)}
	if s.cfg.AdminVia == "published" && res.PublishedPort > 0 {
		host := res.PublishedHost
		if host == "" || host == "0.0.0.0" {
			host = agent.Node.PrivateAddr
		}
		port := int32(res.PublishedPort)
		run.AdminHost, run.AdminPort = &host, &port
	}
	if err := q.SetInstanceRunning(ctx, run); err != nil {
		return err
	}
	if err := log.Info(ctx, "instance", "container %s up in %s, archiving WAL to %s", res.Container,
		time.Since(start).Round(time.Second), walg.S3Prefix(w)); err != nil {
		return err
	}
	inst, err = q.GetInstance(ctx, inst.ID)
	if err != nil {
		return err
	}
	return s.adoptRestore(ctx, inst, p, pitr, log)
}

func ptr[T any](v T) *T { return &v }

// adminConn connects to database on inst as its superuser.
func (s *Service) adminConn(ctx context.Context, inst store.Instance, database string) (*pgx.Conn, error) {
	t, err := store.New(s.db).GetInstanceTarget(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	secret, err := provision.OpenInstanceSecret(s.keyring, inst.ID, t.AdminSecret)
	if err != nil {
		return nil, err
	}
	host, port := t.Host, int(t.Port)
	if t.AdminHost != nil && *t.AdminHost != "" {
		host = *t.AdminHost
	}
	if t.AdminPort != nil && *t.AdminPort > 0 {
		port = int(*t.AdminPort)
	}
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d sslmode=prefer", host, port))
	if err != nil {
		return nil, err
	}
	cfg.User, cfg.Password, cfg.Database = secret.User, secret.Password, database
	cfg.ConnectTimeout = 10 * time.Second
	cfg.RuntimeParams["application_name"] = "pgdock-server"
	return pgx.ConnectConfig(ctx, cfg)
}

// adoptRestore finishes a point-in-time recovery: wait for promotion,
// drop the recovery settings, and rename the source project's database
// and role to this project's (the password changes in Prepare). Then it
// hardens the cluster. Each step is idempotent.
func (s *Service) adoptRestore(ctx context.Context, inst store.Instance, p store.Project, pitr *pitrParams, log *jobs.StepLogger) error {
	conn, err := s.waitConn(ctx, inst)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if pitr != nil {
		deadline := time.Now().Add(s.cfg.ReadyTimeout)
		for {
			var inRecovery bool
			if err := conn.QueryRow(ctx, `SELECT pg_is_in_recovery()`).Scan(&inRecovery); err != nil {
				return err
			}
			if !inRecovery {
				break
			}
			if time.Now().After(deadline) {
				return jobs.Permanent(errors.New("the restored instance did not finish recovery in time"))
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		for _, stmt := range []string{
			"ALTER SYSTEM RESET restore_command", "ALTER SYSTEM RESET recovery_target_time",
			"ALTER SYSTEM RESET recovery_target_action", "SELECT pg_reload_conf()",
		} {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
		src, err := store.New(s.db).GetProject(ctx, pitr.SourceProject)
		if err != nil {
			return err
		}
		if err := renameIfPresent(ctx, conn, "DATABASE", "pg_database", "datname", src.DbName, p.DbName); err != nil {
			return err
		}
		if err := renameIfPresent(ctx, conn, "ROLE", "pg_roles", "rolname", src.OwnerRole, p.OwnerRole); err != nil {
			return err
		}
		// The source's console login came along in the base backup.
		if src.DbName != p.DbName {
			if err := provision.DropConsoleRole(ctx, conn, provision.ConsoleRole(src.DbName), p.DbName); err != nil {
				return err
			}
		}
		if err := log.Info(ctx, "restore", "recovery finished and promoted; %s is now %s, owned by %s", src.DbName, p.DbName, p.OwnerRole); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		"REVOKE CONNECT, TEMPORARY ON DATABASE postgres FROM PUBLIC",
		"REVOKE CONNECT, TEMPORARY ON DATABASE template1 FROM PUBLIC",
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("harden instance: %w", err)
		}
	}
	return nil
}

func renameIfPresent(ctx context.Context, conn *pgx.Conn, kind, catalog, column, from, to string) error {
	if from == to {
		return nil
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+catalog+" WHERE "+column+" = $1)", from).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil // renamed by an earlier attempt
	}
	if kind == "DATABASE" {
		if _, err := conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, from); err != nil {
			return err
		}
	}
	_, err := conn.Exec(ctx, "ALTER "+kind+" "+provision.Ident(from)+" RENAME TO "+provision.Ident(to))
	return err
}

// waitConn connects to the instance, retrying while it starts.
func (s *Service) waitConn(ctx context.Context, inst store.Instance) (*pgx.Conn, error) {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		conn, err := s.adminConn(ctx, inst, "postgres")
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect to instance %s: %w", inst.ID, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Provisioned implements provision.InstanceManager: the first base backup.
func (s *Service) Provisioned(ctx context.Context, p store.Project, log *jobs.StepLogger) error {
	_, err := s.BaseBackup(ctx, p, nil, log)
	return err
}

// BaseBackup takes a WAL-G base backup of a dedicated project (spec §6.4),
// applies retention, and records it.
func (s *Service) BaseBackup(ctx context.Context, p store.Project, opID *uuid.UUID, log *jobs.StepLogger) (store.Backup, error) {
	if p.Tier != provision.TierDedicated {
		return store.Backup{}, jobs.Permanent(errors.New("base backups are for dedicated projects"))
	}
	q := store.New(s.db)
	inst, err := q.GetInstance(ctx, p.InstanceID)
	if err != nil {
		return store.Backup{}, err
	}
	targetID, _, err := s.secrets.StorageTarget(ctx)
	if err != nil {
		return store.Backup{}, jobs.Permanent(err)
	}
	agent, err := s.nodes.ForNode(ctx, inst.NodeID)
	if err != nil {
		return store.Backup{}, err
	}
	if err := log.Info(ctx, "backup", "wal-g backup-push on %s", agent.Node.Name); err != nil {
		return store.Backup{}, err
	}
	res, err := agent.BaseBackup(ctx, inst.ID.String(), agentapi.WALGBackupRequest{RetainFull: s.cfg.RetainFull})
	if err != nil {
		return store.Backup{}, err
	}
	b := res.Backup
	size := b.CompressedSize
	row, err := q.InsertBaseBackup(ctx, store.InsertBaseBackupParams{
		ProjectID: &p.ID, ObjectKey: b.Name, StartedAt: b.StartTime, FinishedAt: &b.FinishTime,
		SizeBytes: &size, StorageTargetID: &targetID, OperationID: opID,
	})
	if err != nil {
		return store.Backup{}, err
	}
	// Retention happened in WAL-G; forget backups it no longer lists.
	if list, err := agent.BaseBackups(ctx, inst.ID.String()); err == nil {
		keep := map[string]bool{}
		for _, l := range list {
			keep[l.Name] = true
		}
		rows, _ := q.ListBaseBackups(ctx, &p.ID)
		for _, r := range rows {
			if !keep[r.ObjectKey] {
				_ = q.MarkBackupDeleted(ctx, r.ID)
			}
		}
	}
	return row, log.Info(ctx, "backup", "base backup %s: %s compressed (%s of data) in %s", b.Name,
		human(b.CompressedSize), human(b.UncompressedSize), (time.Duration(res.DurationMS) * time.Millisecond).Round(time.Millisecond))
}

func human(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	exp := int(math.Log(float64(n)) / math.Log(1024))
	exp = min(exp, 4)
	return fmt.Sprintf("%.1f %ciB", float64(n)/math.Pow(1024, float64(exp)), "KMGT"[exp-1])
}

// Destroy implements provision.InstanceManager: container, volume, and the
// WAL-G archive go. (The final logical backup is taken before this.)
func (s *Service) Destroy(ctx context.Context, p store.Project, log *jobs.StepLogger) error {
	inst, err := store.New(s.db).GetInstance(ctx, p.InstanceID)
	if err != nil {
		return err
	}
	return s.destroyInstance(ctx, inst, log)
}

// destroyInstance removes an instance's container, volume, and archive.
func (s *Service) destroyInstance(ctx context.Context, inst store.Instance, log *jobs.StepLogger) error {
	q := store.New(s.db)
	if inst.Status == "deleted" {
		return nil
	}
	agent, err := s.nodes.ForNode(ctx, inst.NodeID)
	if err != nil {
		return err
	}
	if err := agent.DestroyInstance(ctx, inst.ID.String()); err != nil {
		return err
	}
	if inst.WalgPrefix != nil {
		if _, target, err := s.secrets.StorageTarget(ctx); err == nil {
			if c, err := storage.New(target); err == nil {
				n, err := c.DeletePrefix(ctx, *inst.WalgPrefix)
				if err != nil {
					_ = log.Warn(ctx, "drop", "could not delete the WAL-G archive: %v", err)
				} else {
					_ = log.Info(ctx, "drop", "deleted the WAL-G archive (%d objects)", n)
				}
			}
		}
	}
	if err := q.MarkInstanceDeleted(ctx, inst.ID); err != nil {
		return err
	}
	return log.Info(ctx, "drop", "removed container and volume on node %s", agent.Node.Name)
}
