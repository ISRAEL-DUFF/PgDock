package dedicated

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// KindRegionEtcdMoveAll moves every HA project of a region whose Patroni
// state is in another region's etcd onto the region's own cluster, one at
// a time (V4.1 §8.1): each pauses its own project for a restart, and no
// two run at once in the region.
const KindRegionEtcdMoveAll = "region_etcd_move_all"

type moveAllParams struct {
	Region   string      `json:"region"`
	Projects []uuid.UUID `json:"projects"`
}

// moveAllPoll is how often Move all checks on the move it is waiting for.
const moveAllPoll = 2 * time.Second

// HAOnOtherEtcd lists the region's HA projects whose state is in another
// region's etcd cluster.
func (s *Service) HAOnOtherEtcd(ctx context.Context, region string) ([]store.HAOnOtherEtcdRow, error) {
	return store.New(s.db).HAOnOtherEtcd(ctx, region)
}

// MoveAllToRegionEtcd queues moving them all, one after the other.
func (s *Service) MoveAllToRegionEtcd(ctx context.Context, region string, by *uuid.UUID) (store.Operation, error) {
	if s.Etcd == nil {
		return store.Operation{}, fmt.Errorf("%w: HA is not available on this server", provision.ErrNoDedicated)
	}
	if err := s.Etcd.Ready(ctx, region); err != nil {
		return store.Operation{}, err
	}
	q := store.New(s.db)
	if last, err := q.LatestRegionOperation(ctx, store.LatestRegionOperationParams{Kind: KindRegionEtcdMoveAll, Region: region}); err == nil && !jobs.IsTerminal(last.Status) {
		return store.Operation{}, fmt.Errorf("%w: the region's projects are already being moved (operation %s)", provision.ErrConflict, last.ID)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return store.Operation{}, err
	}
	list, err := q.HAOnOtherEtcd(ctx, region)
	if err != nil {
		return store.Operation{}, err
	}
	if len(list) == 0 {
		return store.Operation{}, fmt.Errorf("%w: every HA project in %s already uses its etcd cluster", provision.ErrConflict, region)
	}
	p := moveAllParams{Region: region}
	for _, x := range list {
		p.Projects = append(p.Projects, x.ID)
	}
	return jobs.Enqueue(ctx, s.db, jobs.EnqueueParams{Kind: KindRegionEtcdMoveAll, Params: p, CreatedBy: by})
}

// moveOne queues one project's move (MoveToRegionEtcd; tests swap it).
func (s *Service) moveOne(ctx context.Context, project uuid.UUID, by *uuid.UUID) (store.Operation, error) {
	if s.etcdMoveOne != nil {
		return s.etcdMoveOne(ctx, project, by)
	}
	return s.MoveToRegionEtcd(ctx, project, by)
}

// SetEtcdMoveOne replaces the per-project move Move all queues (tests).
func (s *Service) SetEtcdMoveOne(f func(ctx context.Context, project uuid.UUID, by *uuid.UUID) (store.Operation, error)) {
	s.etcdMoveOne = f
}

func (s *Service) runRegionEtcdMoveAll(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	var params moveAllParams
	if err := decodeJSON(op.Params, &params); err != nil {
		return jobs.Permanent(err)
	}
	failed := 0
	for i, pid := range params.Projects {
		step := fmt.Sprintf("project %d of %d", i+1, len(params.Projects))
		child, err := s.moveOne(ctx, pid, op.CreatedBy)
		if err != nil {
			// Moved meanwhile, deleted, or busy: say so and carry on.
			failed++
			if err := log.Warn(ctx, step, "%s: not moved: %v", pid, err); err != nil {
				return err
			}
			continue
		}
		if err := log.Info(ctx, step, "%s: moving (operation %s)", pid, child.ID); err != nil {
			return err
		}
		for !jobs.IsTerminal(child.Status) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(moveAllPoll):
			}
			if child, err = jobs.Get(ctx, s.db, child.ID); err != nil {
				return err
			}
		}
		if child.Status != "succeeded" {
			failed++
			msg := ""
			if child.Error != nil {
				msg = *child.Error
			}
			if err := log.Warn(ctx, step, "%s: the move %s: %s", pid, child.Status, msg); err != nil {
				return err
			}
			continue
		}
		if err := log.Info(ctx, step, "%s: moved", pid); err != nil {
			return err
		}
	}
	if failed > 0 {
		return jobs.Permanent(fmt.Errorf("%d of %d projects weren't moved; see the log", failed, len(params.Projects)))
	}
	return nil
}
