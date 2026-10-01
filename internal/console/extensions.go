package console

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// ErrNotAllowed means an extension is not on the project tier's allow-list
// or not installed on its server.
var ErrNotAllowed = errors.New("extension not allowed")

// Extension is one allow-listed extension and its state in a project.
type Extension struct {
	Name             string
	Tier             string // the lowest tier that allows it
	Allowed          bool
	Available        bool
	DefaultVersion   *string
	InstalledVersion *string
	Schema           *string
}

// Extensions lists every allow-listed extension (spec §7.4) and whether the
// project's tier allows it, its server has it, and it is enabled.
func (s *Service) Extensions(ctx context.Context, projectID uuid.UUID) ([]Extension, error) {
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.Status != provision.StatusActive {
		return nil, fmt.Errorf("%w (it is %s)", ErrNotActive, p.Status)
	}
	return s.extensions(ctx, p)
}

func (s *Service) extensions(ctx context.Context, p store.Project) ([]Extension, error) {
	all := provision.AllowedExtensions(provision.TierDedicated)
	allowed := provision.AllowedExtensions(p.Tier)
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(ctx, `
		SELECT u.name, a.name IS NOT NULL, a.default_version, e.extversion, n.nspname
		FROM unnest($1::text[]) WITH ORDINALITY AS u(name, ord)
		LEFT JOIN pg_available_extensions a ON a.name = u.name
		LEFT JOIN pg_extension e ON e.extname = u.name
		LEFT JOIN pg_namespace n ON n.oid = e.extnamespace
		ORDER BY u.ord`, all)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Extension
	for rows.Next() {
		var e Extension
		if err := rows.Scan(&e.Name, &e.Available, &e.DefaultVersion, &e.InstalledVersion, &e.Schema); err != nil {
			return nil, err
		}
		e.Tier = provision.TierShared
		if !slices.Contains(provision.SharedExtensions, e.Name) {
			e.Tier = provision.TierDedicated
		}
		e.Allowed = slices.Contains(allowed, e.Name)
		out = append(out, e)
	}
	return out, rows.Err()
}

// EnableExtension runs CREATE EXTENSION for an allow-listed extension in
// the project database and returns the updated list.
func (s *Service) EnableExtension(ctx context.Context, projectID uuid.UUID, name string) ([]Extension, error) {
	p, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if p.Status != provision.StatusActive {
		return nil, fmt.Errorf("%w (it is %s)", ErrNotActive, p.Status)
	}
	if !slices.Contains(provision.AllowedExtensions(p.Tier), name) {
		if slices.Contains(provision.DedicatedExtensions, name) {
			return nil, fmt.Errorf("%w: %s is available on the dedicated tier only", ErrNotAllowed, name)
		}
		return nil, fmt.Errorf("%w: %s is not on the allow-list", ErrNotAllowed, name)
	}
	exts, err := s.extensions(ctx, p)
	if err != nil {
		return nil, err
	}
	for _, e := range exts {
		if e.Name == name && !e.Available {
			return nil, fmt.Errorf("%w: %s is not installed on this project's server", ErrNotAllowed, name)
		}
	}
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())
	// Superuser-created, in public (owned by the project role) so the
	// project's search_path finds it.
	stmt := "CREATE EXTENSION IF NOT EXISTS " + provision.Ident(name)
	if _, err := conn.Exec(ctx, stmt+" WITH SCHEMA public"); err != nil {
		// Some extensions (pg_cron, timescaledb's catalog) pick their schema.
		if !strings.Contains(err.Error(), "must be installed in schema") {
			return nil, fmt.Errorf("enable %s: %w", name, err)
		}
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return nil, fmt.Errorf("enable %s: %w", name, err)
		}
	}
	if err := store.New(s.db).AddProjectExtension(ctx, store.AddProjectExtensionParams{ID: p.ID, Name: name}); err != nil {
		return nil, err
	}
	s.log.Info("extension enabled", "project_id", p.ID, "extension", name)
	return s.extensions(ctx, p)
}
