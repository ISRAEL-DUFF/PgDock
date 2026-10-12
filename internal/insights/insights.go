// Package insights is query insights (V3 §8): pg_stat_statements snapshots
// turned into per-project deltas, top queries, EXPLAIN, the slow-query log,
// index suggestions, unused and duplicate indexes, bloat, and locks.
//
// The control plane reads pg_stat_statements as the instance admin, which
// sees every row, and keeps only each project's own; tenants never query
// the view on shared clusters.
package insights

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/console"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Errors.
var (
	ErrNotAvailable = errors.New("query insights are available on Pro, Team and dedicated projects")
	ErrNotFound     = errors.New("no such query")
	ErrInvalid      = errors.New("invalid request")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Config tunes the service.
type Config struct {
	// Interval is how often pg_stat_statements is snapshotted (default 5m).
	Interval time.Duration
	// SlowQuery is the slow-query log's threshold (default 1s).
	SlowQuery time.Duration
	// Plans are the plans whose shared projects get insights; "all" is
	// every plan. Dedicated projects always do.
	Plans []string
}

// Service collects and serves query insights.
type Service struct {
	db       *pgxpool.Pool
	projects *provision.Service
	console  *console.Service
	cfg      Config
	log      *slog.Logger
	// Now is the clock (tests move it).
	Now func() time.Time
}

// New returns the service. console runs EXPLAIN and the catalog reads as
// the project's role.
func New(db *pgxpool.Pool, projects *provision.Service, cons *console.Service, cfg Config, log *slog.Logger) *Service {
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.SlowQuery == 0 {
		cfg.SlowQuery = time.Second
	}
	if cfg.Plans == nil {
		cfg.Plans = []string{"pro", "team"}
	}
	return &Service{db: db, projects: projects, console: cons, cfg: cfg, log: log, Now: time.Now}
}

// SlowThreshold is the slow-query threshold.
func (s *Service) SlowThreshold() time.Duration { return s.cfg.SlowQuery }

// eligible: dedicated projects, and shared ones on an included plan.
func (s *Service) eligible(tier, plan string) bool {
	return tier == provision.TierDedicated || slices.Contains(s.cfg.Plans, "all") || slices.Contains(s.cfg.Plans, plan)
}

// Available reports whether p gets query insights.
func (s *Service) Available(ctx context.Context, p store.Project) (bool, error) {
	if p.Tier == provision.TierDedicated || slices.Contains(s.cfg.Plans, "all") {
		return true, nil
	}
	plan, err := store.New(s.db).ProjectPlan(ctx, p.OrgID)
	if err != nil {
		return false, err
	}
	return s.eligible(p.Tier, plan), nil
}

// Run collects every interval, rolls up and prunes hourly, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	var lastRollup time.Time
	for {
		if err := s.Collect(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("query insights: collect", "err", err)
		}
		if time.Since(lastRollup) > time.Hour {
			if err := s.Maintain(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("query insights: roll up", "err", err)
			}
			lastRollup = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Maintain folds 5-minute buckets older than a day into hours and drops
// what is older than 30 days.
func (s *Service) Maintain(ctx context.Context) error {
	q := store.New(s.db)
	now := s.Now()
	if err := q.RollUpQueryStats(ctx, now.Add(-24*time.Hour)); err != nil {
		return err
	}
	return q.PruneQueryInsights(ctx, now.Add(-30*24*time.Hour))
}
