package provision

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/pooler"
	"github.com/israel-duff/pgdock/internal/store"
)

// Operation kinds implemented here.
const (
	KindCreate = "create"
	KindDelete = "delete"
	KindRotate = "rotate"
)

// Project statuses.
const (
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusDeleting     = "deleting"
	StatusDeleted      = "deleted"
	StatusError        = "error"
)

var (
	// ErrNotFound means the project does not exist (or was deleted).
	ErrNotFound = errors.New("project not found")
	// ErrConflict means the project is busy or in the wrong state.
	ErrConflict = errors.New("conflict")
	// ErrNoCapacity means no shared instance can take a new project.
	ErrNoCapacity = errors.New("no shared cluster available")
)

// Config holds the connection details handed to clients and those the
// control plane uses to smoke-test the poolers.
type Config struct {
	// Public connection info, as clients see it (spec §5.1).
	DBHost      string
	SessionPort int // session-mode pooler, 5432 in production
	PooledPort  int // transaction-mode pooler, 6543 in production
	SSLMode     string

	// SmokeSessionAddr and SmokePooledAddr are host:port of the two poolers
	// as pgdock-server reaches them; they default to the public ones.
	SmokeSessionAddr string
	SmokePooledAddr  string
	// SmokeSSLMode is the sslmode for smoke tests (default "prefer").
	SmokeSSLMode string
	// AdminSSLMode is the sslmode for admin connections to clusters
	// (default "prefer").
	AdminSSLMode string
}

func (c *Config) setDefaults() {
	if c.SSLMode == "" {
		c.SSLMode = "require"
	}
	if c.SmokeSessionAddr == "" {
		c.SmokeSessionAddr = net.JoinHostPort(c.DBHost, strconv.Itoa(c.SessionPort))
	}
	if c.SmokePooledAddr == "" {
		c.SmokePooledAddr = net.JoinHostPort(c.DBHost, strconv.Itoa(c.PooledPort))
	}
	if c.SmokeSSLMode == "" {
		c.SmokeSSLMode = "prefer"
	}
	if c.AdminSSLMode == "" {
		c.AdminSSLMode = "prefer"
	}
}

// Service runs project provisioning.
type Service struct {
	db      *pgxpool.Pool
	keyring *crypto.Keyring
	pooler  *pooler.Manager
	cfg     Config
	log     *slog.Logger
}

// NewService returns a Service.
func NewService(db *pgxpool.Pool, keyring *crypto.Keyring, pm *pooler.Manager, cfg Config, log *slog.Logger) *Service {
	cfg.setDefaults()
	return &Service{db: db, keyring: keyring, pooler: pm, cfg: cfg, log: log}
}

// Kinds returns the operation kinds this service handles.
func (s *Service) Kinds() map[string]jobs.Kind {
	return map[string]jobs.Kind{
		KindCreate: {Handler: s.runCreate, OnFail: s.rollbackCreate, MaxAttempts: 3},
		KindRotate: {Handler: s.runRotate, OnFail: s.rollbackRotate, MaxAttempts: 3},
		KindDelete: {Handler: s.runDelete, OnFail: s.failDelete, MaxAttempts: 5},
	}
}

// Connection is how clients reach a project. URLs carry no password.
type Connection struct {
	Host        string
	SessionPort int
	PooledPort  int
	Database    string
	User        string
	SSLMode     string
}

// ConnectionFor returns the connection info for a project.
func (s *Service) ConnectionFor(p store.Project) Connection {
	return Connection{
		Host: s.cfg.DBHost, SessionPort: s.cfg.SessionPort, PooledPort: s.cfg.PooledPort,
		Database: p.DbName, User: p.OwnerRole, SSLMode: s.cfg.SSLMode,
	}
}

// PooledURL is the transaction-mode URL (app traffic, serverless).
func (c Connection) PooledURL(password string) string { return c.url(c.PooledPort, password) }

// SessionURL is the session-mode URL (migrations, session features).
func (c Connection) SessionURL(password string) string { return c.url(c.SessionPort, password) }

func (c Connection) url(port int, password string) string {
	userinfo := c.User
	if password != "" {
		userinfo += ":" + password // URL-safe by construction
	}
	return fmt.Sprintf("postgresql://%s@%s/%s?sslmode=%s",
		userinfo, net.JoinHostPort(c.Host, strconv.Itoa(port)), c.Database, c.SSLMode)
}

// CreateParams describes a new project.
type CreateParams struct {
	Name        string
	Description *string
	CreatedBy   *uuid.UUID
}

// Created is returned once by Create. Password is never stored; this is the
// only time it exists outside the client.
type Created struct {
	Project   store.Project
	Operation store.Operation
	Password  string
}

// opSecrets is the encrypted handoff in operations.params.secrets, scrubbed
// when the operation finishes. The create and rotate flows need the
// plaintext password only to smoke-test the pooler.
type opSecrets struct {
	Password string `json:"password"` // base64 of the sealed password
}

type createParams struct {
	Secrets opSecrets `json:"secrets"`
}

type rotateParams struct {
	Verifier         string    `json:"verifier"`
	PreviousVerifier string    `json:"previous_verifier"`
	Secrets          opSecrets `json:"secrets"`
}

func passwordAAD(projectID uuid.UUID, purpose string) []byte {
	return []byte("operations.secrets.password:" + purpose + ":" + projectID.String())
}

func (s *Service) sealPassword(projectID uuid.UUID, purpose, password string) (opSecrets, error) {
	ct, err := s.keyring.Encrypt([]byte(password), passwordAAD(projectID, purpose))
	if err != nil {
		return opSecrets{}, err
	}
	return opSecrets{Password: base64.StdEncoding.EncodeToString(ct)}, nil
}

func (s *Service) openPassword(projectID uuid.UUID, purpose string, sec opSecrets) (string, error) {
	ct, err := base64.StdEncoding.DecodeString(sec.Password)
	if err != nil || sec.Password == "" {
		return "", errors.New("operation has no password handoff (already scrubbed?)")
	}
	pt, err := s.keyring.Decrypt(ct, passwordAAD(projectID, purpose))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// Create validates the request, records the project, and queues the create
// operation (spec §6.1).
func (s *Service) Create(ctx context.Context, p CreateParams) (Created, error) {
	name, err := ValidateName(p.Name)
	if err != nil {
		return Created{}, err
	}
	slug, err := Slugify(name)
	if err != nil {
		return Created{}, err
	}
	if p.Description != nil && len(*p.Description) > 1000 {
		return Created{}, fmt.Errorf("%w: description must be at most 1000 bytes", ErrInvalid)
	}
	password, err := GeneratePassword()
	if err != nil {
		return Created{}, err
	}
	verifier, err := crypto.SCRAMVerifier(password)
	if err != nil {
		return Created{}, err
	}
	settings, err := json.Marshal(store.DefaultSharedSettings())
	if err != nil {
		return Created{}, err
	}

	inst, err := store.New(s.db).PickSharedInstance(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Created{}, ErrNoCapacity
	}
	if err != nil {
		return Created{}, err
	}

	// The random suffix rarely collides; retry with a fresh one if it does.
	for attempt := 0; ; attempt++ {
		dbName, role, err := Names(slug)
		if err != nil {
			return Created{}, err
		}
		id := uuid.New()
		sec, err := s.sealPassword(id, KindCreate, password)
		if err != nil {
			return Created{}, err
		}

		var out Created
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			proj, err := store.New(tx).InsertProject(ctx, store.InsertProjectParams{
				ID: id, Name: name, Slug: slug, DbName: dbName, OwnerRole: role,
				ScramVerifier: verifier, InstanceID: inst.ID, Settings: settings,
				Description: p.Description, CreatedBy: p.CreatedBy,
			})
			if err != nil {
				return err
			}
			op, err := jobs.Enqueue(ctx, tx, jobs.EnqueueParams{
				Kind: KindCreate, ProjectID: &proj.ID, CreatedBy: p.CreatedBy,
				Params: createParams{Secrets: sec},
			})
			if err != nil {
				return err
			}
			out = Created{Project: proj, Operation: op, Password: password}
			return nil
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && attempt < 5 {
			continue
		}
		if err != nil {
			return Created{}, err
		}
		return out, nil
	}
}

// Rotated is returned once by Rotate.
type Rotated struct {
	Project   store.Project
	Operation store.Operation
	Password  string
}

// Rotate queues a password rotation (spec §5.3). The new password works once
// the operation succeeds; the old one stops working at the same moment.
func (s *Service) Rotate(ctx context.Context, projectID uuid.UUID, by *uuid.UUID) (Rotated, error) {
	password, err := GeneratePassword()
	if err != nil {
		return Rotated{}, err
	}
	verifier, err := crypto.SCRAMVerifier(password)
	if err != nil {
		return Rotated{}, err
	}
	sec, err := s.sealPassword(projectID, KindRotate, password)
	if err != nil {
		return Rotated{}, err
	}
	var out Rotated
	err = s.withIdleProject(ctx, projectID, []string{StatusActive}, func(tx pgx.Tx, p store.Project) error {
		op, err := jobs.Enqueue(ctx, tx, jobs.EnqueueParams{
			Kind: KindRotate, ProjectID: &p.ID, CreatedBy: by,
			Params: rotateParams{Verifier: verifier, PreviousVerifier: p.ScramVerifier, Secrets: sec},
		})
		out = Rotated{Project: p, Operation: op, Password: password}
		return err
	})
	return out, err
}

// Delete queues deletion (spec §6.2). confirmName must equal the project's
// name, as typed by the operator.
func (s *Service) Delete(ctx context.Context, projectID uuid.UUID, confirmName string, by *uuid.UUID) (store.Operation, error) {
	var out store.Operation
	err := s.withIdleProject(ctx, projectID, []string{StatusActive, StatusError}, func(tx pgx.Tx, p store.Project) error {
		if confirmName != p.Name {
			return fmt.Errorf("%w: confirm must match the project name exactly", ErrInvalid)
		}
		if err := store.New(tx).SetProjectStatus(ctx, store.SetProjectStatusParams{ID: p.ID, Status: StatusDeleting}); err != nil {
			return err
		}
		op, err := jobs.Enqueue(ctx, tx, jobs.EnqueueParams{Kind: KindDelete, ProjectID: &p.ID, CreatedBy: by})
		out = op
		return err
	})
	return out, err
}

// withIdleProject locks a live project, checks its status and that no other
// operation is pending on it, and runs f in the same transaction. One
// operation per project at a time keeps the flows from interleaving.
func (s *Service) withIdleProject(ctx context.Context, id uuid.UUID, allowed []string, f func(pgx.Tx, store.Project) error) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		p, err := q.GetLiveProjectForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		ok := false
		for _, st := range allowed {
			ok = ok || p.Status == st
		}
		if !ok {
			return fmt.Errorf("%w: project is %s", ErrConflict, p.Status)
		}
		busy, err := q.ProjectHasActiveOperation(ctx, &id)
		if err != nil {
			return err
		}
		if busy {
			return fmt.Errorf("%w: another operation is in progress on this project", ErrConflict)
		}
		return f(tx, p)
	})
}

// Get returns a live project.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (store.Project, error) {
	p, err := store.New(s.db).GetProject(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.DeletedAt != nil) {
		return store.Project{}, ErrNotFound
	}
	return p, err
}

// List returns live projects, newest first.
func (s *Service) List(ctx context.Context, status *string, limit int) ([]store.Project, error) {
	return store.New(s.db).ListLiveProjects(ctx, store.ListLiveProjectsParams{Status: status, MaxRows: int32(limit)})
}
