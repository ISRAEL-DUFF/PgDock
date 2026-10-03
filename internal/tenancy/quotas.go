package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/store"
)

// QuotaError is a creation-time or rate limit refusal (V2 §10.3): the API
// answers 409 quota_exceeded with the limit, the usage, and the maximum.
type QuotaError struct {
	Limit string
	Used  int64
	Max   int64
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("quota exceeded: %s is %d of %d", e.Limit, e.Used, e.Max)
}

// UnlimitedPlan is the plan whose dedicated allowance is unlimited (the
// platform admin's own organisation).
const UnlimitedPlan = "Unlimited"

// operationKinds count towards operations in flight: the heavy work an
// organisation can queue (V2 §10.3).
var operationKinds = []string{"backup", "base_backup", "restore", "import", "pitr", "export_project", "reclaim_space", "storage_switch", "branch_create", "branch_reset"}

func (s *Service) orgActive(o store.OrgWithPlanRow) error {
	if o.Status != OrgActive {
		return fmt.Errorf("%w: the organisation is %s", ErrConflict, o.Status)
	}
	return nil
}

// CheckCreateProject refuses a new project beyond the projects quota.
func (s *Service) CheckCreateProject(ctx context.Context, orgID uuid.UUID) error {
	l, o, err := s.Limits(ctx, orgID)
	if err != nil {
		return err
	}
	if err := s.orgActive(o); err != nil {
		return err
	}
	if limit, ok := l.Get(store.LimitProjects); ok {
		n, err := store.New(s.db).CountOrgProjects(ctx, orgID)
		if err != nil {
			return err
		}
		if int64(n) >= limit {
			return &QuotaError{Limit: store.LimitProjects, Used: int64(n), Max: limit}
		}
	}
	return nil
}

// CheckCreateBranch refuses a branch of a parent of parentBytes (-1: not
// measured) beyond the branches quota, or one that would not fit the
// per-project or total shared storage limits (V2 §8.2 step 1).
func (s *Service) CheckCreateBranch(ctx context.Context, orgID uuid.UUID, parentBytes float64) error {
	l, o, err := s.Limits(ctx, orgID)
	if err != nil {
		return err
	}
	if err := s.orgActive(o); err != nil {
		return err
	}
	q := store.New(s.db)
	if limit, ok := l.Get(store.LimitBranches); ok {
		n, err := q.CountOrgBranches(ctx, orgID)
		if err != nil {
			return err
		}
		if int64(n) >= limit {
			return &QuotaError{Limit: store.LimitBranches, Used: int64(n), Max: limit}
		}
	}
	if parentBytes <= 0 {
		return nil
	}
	mb := int64(parentBytes / (1 << 20))
	if limit, ok := l.Get(store.LimitProjectStorageMB); ok && mb > limit {
		return &QuotaError{Limit: store.LimitProjectStorageMB, Used: mb, Max: limit}
	}
	if limit, ok := l.Get(store.LimitSharedStorageMB); ok {
		used, err := q.OrgSharedStorage(ctx, orgID)
		if err != nil {
			return err
		}
		if total := int64((used + parentBytes) / (1 << 20)); total > limit {
			return &QuotaError{Limit: store.LimitSharedStorageMB, Used: int64(used / (1 << 20)), Max: limit}
		}
	}
	return nil
}

// CheckOperation refuses another heavy operation beyond operations in
// flight.
func (s *Service) CheckOperation(ctx context.Context, orgID uuid.UUID) error {
	l, o, err := s.Limits(ctx, orgID)
	if err != nil {
		return err
	}
	if err := s.orgActive(o); err != nil {
		return err
	}
	if limit, ok := l.Get(store.LimitOperationsInFlight); ok {
		n, err := store.New(s.db).CountOrgOperationsInFlight(ctx, store.CountOrgOperationsInFlightParams{OrgID: orgID, Kinds: operationKinds})
		if err != nil {
			return err
		}
		if int64(n) >= limit {
			return &QuotaError{Limit: store.LimitOperationsInFlight, Used: int64(n), Max: limit}
		}
	}
	return nil
}

// CheckBackupStorage refuses another backup to a platform target once the
// organisation's backups there fill its backup quota (V2 §10.3). Backups
// on the organisation's own targets never count.
func (s *Service) CheckBackupStorage(ctx context.Context, orgID uuid.UUID) error {
	l, _, err := s.Limits(ctx, orgID)
	if err != nil {
		return err
	}
	limit, ok := l.Get(store.LimitBackupStorageMB)
	if !ok {
		return nil
	}
	b, err := store.New(s.db).OrgPlatformBackupBytes(ctx, orgID)
	if err != nil {
		return err
	}
	if used := int64(b / (1 << 20)); used >= limit {
		return &QuotaError{Limit: store.LimitBackupStorageMB, Used: used, Max: limit}
	}
	return nil
}

// MaxConnections is the backend connections a project of orgID may have
// (0: unlimited).
func (s *Service) MaxConnections(ctx context.Context, orgID uuid.UUID) (int, error) {
	l, _, err := s.Limits(ctx, orgID)
	if err != nil {
		return 0, err
	}
	if limit, ok := l.Get(store.LimitProjectConnections); ok {
		return int(limit), nil
	}
	return 0, nil
}

// AcquireConsole takes one of the organisation's concurrent console query
// slots, or refuses with a QuotaError; release gives it back. The count is
// per server.
func (s *Service) AcquireConsole(ctx context.Context, orgID uuid.UUID) (release func(), err error) {
	l, _, err := s.Limits(ctx, orgID)
	if err != nil {
		return nil, err
	}
	limit, limited := l.Get(store.LimitConsoleQueries)
	s.consoleMu.Lock()
	defer s.consoleMu.Unlock()
	if limited && int64(s.console[orgID]) >= limit {
		return nil, &QuotaError{Limit: store.LimitConsoleQueries, Used: int64(s.console[orgID]), Max: limit}
	}
	s.console[orgID]++
	return func() {
		s.consoleMu.Lock()
		s.console[orgID]--
		s.consoleMu.Unlock()
	}, nil
}

// Dedicated is a dedicated instance an organisation asks for.
type Dedicated struct {
	CPUs     float64
	MemoryMB int
	DiskGB   int
}

// WithinAllowance reports whether orgID may run one more dedicated
// instance of size d without asking (V2 §10.6).
func (s *Service) WithinAllowance(ctx context.Context, orgID uuid.UUID, d Dedicated) (bool, error) {
	_, o, err := s.Limits(ctx, orgID)
	if err != nil {
		return false, err
	}
	if o.PlanName == UnlimitedPlan {
		return true, nil
	}
	a, err := store.DecodeDedicatedAllowance(o.DedicatedAllowance)
	if err != nil {
		return false, err
	}
	u, err := store.New(s.db).OrgDedicatedUse(ctx, orgID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	return int(u.Instances)+1 <= a.Instances &&
		u.Cpus+d.CPUs <= a.CPUs+1e-9 &&
		int(u.MemMb)+d.MemoryMB <= a.MemoryMB &&
		int(u.DiskGb)+d.DiskGB <= a.DiskGB, nil
}

// Quota is one limit with its current use, for the Usage & quotas page.
type Quota struct {
	Limit string
	Used  float64
	Max   *int64
}

// Quotas returns the organisation's limits and current use.
func (s *Service) Quotas(ctx context.Context, orgID uuid.UUID) ([]Quota, store.OrgWithPlanRow, error) {
	l, o, err := s.Limits(ctx, orgID)
	if err != nil {
		return nil, o, err
	}
	q := store.New(s.db)
	projects, err := q.CountOrgProjects(ctx, orgID)
	if err != nil {
		return nil, o, err
	}
	storage, err := q.OrgSharedStorage(ctx, orgID)
	if err != nil {
		return nil, o, err
	}
	branches, err := q.CountOrgBranches(ctx, orgID)
	if err != nil {
		return nil, o, err
	}
	ops, err := q.CountOrgOperationsInFlight(ctx, store.CountOrgOperationsInFlightParams{OrgID: orgID, Kinds: operationKinds})
	if err != nil {
		return nil, o, err
	}
	largest, err := q.OrgLargestProject(ctx, orgID)
	if err != nil {
		return nil, o, err
	}
	backups, err := q.OrgPlatformBackupBytes(ctx, orgID)
	if err != nil {
		return nil, o, err
	}
	s.consoleMu.Lock()
	console := s.console[orgID]
	s.consoleMu.Unlock()
	used := map[string]float64{
		store.LimitProjects:           float64(projects),
		store.LimitBranches:           float64(branches),
		store.LimitSharedStorageMB:    storage / (1 << 20),
		store.LimitProjectStorageMB:   largest / (1 << 20),
		store.LimitOperationsInFlight: float64(ops),
		store.LimitConsoleQueries:     float64(console),
		store.LimitBackupStorageMB:    backups / (1 << 20),
	}
	var out []Quota
	for _, k := range store.LimitKeys {
		qt := Quota{Limit: k, Used: used[k]}
		if v, ok := l.Get(k); ok {
			qt.Max = &v
		}
		out = append(out, qt)
	}
	return out, o, nil
}
