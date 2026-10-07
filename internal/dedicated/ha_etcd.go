package dedicated

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// KindHAEtcdMove moves an HA project's Patroni state onto its region's
// etcd cluster (V3.1 §3.3).
const KindHAEtcdMove = "ha_etcd_move"

type etcdMoveParams struct {
	Instance    uuid.UUID `json:"instance"`
	Region      string    `json:"region"`
	HadHA       bool      `json:"had_ha"`
	Standby     uuid.UUID `json:"standby"`
	StandbyNode uuid.UUID `json:"standby_node"`
}

// EtcdRegionOf is the region whose etcd cluster holds the instance's
// state, and the region the project should use.
func (s *Service) EtcdRegionOf(ctx context.Context, p store.Project) (current, want string, err error) {
	inst, err := store.New(s.db).GetInstance(ctx, p.InstanceID)
	if err != nil {
		return "", "", err
	}
	if !inst.Patroni {
		return "", p.Region, nil
	}
	current, err = s.etcdRegion(ctx, inst)
	return current, p.Region, err
}

// MoveToRegionEtcd queues moving the project's Patroni state to its
// region's own etcd cluster: the standby is removed, the primary restarts
// on the new cluster with the poolers holding clients, and a new standby
// is built (V3.1 §3.3). Writes pause once, for the restart.
func (s *Service) MoveToRegionEtcd(ctx context.Context, projectID uuid.UUID, by *uuid.UUID) (store.Operation, error) {
	if s.Etcd == nil {
		return store.Operation{}, fmt.Errorf("%w: HA is not available on this server", provision.ErrNoDedicated)
	}
	return s.projects.EnqueueExclusiveTx(ctx, projectID, []string{provision.StatusActive}, "", KindHAEtcdMove, by,
		func(tx pgx.Tx, pr store.Project) (any, error) {
			inst, err := store.New(tx).GetInstance(ctx, pr.InstanceID)
			if err != nil {
				return nil, err
			}
			if !inst.Patroni {
				return nil, fmt.Errorf("%w: the project isn't under Patroni; enabling HA uses its region's cluster", provision.ErrConflict)
			}
			current, err := s.etcdRegion(ctx, inst)
			if err != nil {
				return nil, err
			}
			if current == pr.Region {
				return nil, fmt.Errorf("%w: the project already uses %s's etcd cluster", provision.ErrConflict, pr.Region)
			}
			if err := s.Etcd.Ready(ctx, pr.Region); err != nil {
				return nil, err
			}
			out := etcdMoveParams{Instance: inst.ID, Region: pr.Region, HadHA: inst.HaEnabled}
			if inst.HaEnabled {
				n, err := s.standbyNode(ctx, inst.NodeID, nil)
				if err != nil {
					return nil, err
				}
				out.Standby, out.StandbyNode = uuid.New(), n.ID
			}
			return out, nil
		})
}

func (s *Service) runHAEtcdMove(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	var params etcdMoveParams
	if err := decodeJSON(op.Params, &params); err != nil {
		return err
	}
	q := store.New(s.db)
	inst, err := q.GetInstance(ctx, params.Instance)
	if err != nil {
		return jobs.Permanent(err)
	}
	p, err := q.GetProject(ctx, *op.ProjectID)
	if err != nil {
		return jobs.Permanent(err)
	}
	from, err := s.etcdRegion(ctx, inst)
	if err != nil {
		return err
	}

	// 1. The standby goes: one member can change clusters with one restart.
	members, err := q.ListInstanceMembers(ctx, inst.ID)
	if err != nil {
		return err
	}
	leader := leaderKey(inst)
	for _, m := range members {
		if m.ID == leader {
			continue
		}
		if err := s.removeMember(ctx, m.ID, m.NodeID); err != nil {
			return err
		}
		if err := log.Info(ctx, "standby", "removed the standby on %s", m.NodeName); err != nil {
			return err
		}
	}

	// 2. The primary restarts on the region's cluster, the poolers holding
	// clients. Patroni finds its data and initialises the new cluster
	// from it, as when HA was first turned on.
	if from != params.Region {
		if err := q.SetInstanceEtcdRegion(ctx, store.SetInstanceEtcdRegionParams{ID: inst.ID, EtcdRegion: &params.Region}); err != nil {
			return err
		}
		inst.EtcdRegion = &params.Region
		if err := s.restartLeader(ctx, p, inst, log); err != nil {
			return err
		}
	}

	// 3. A new standby, as HA was before.
	if params.HadHA {
		now, err := q.ListInstanceMembers(ctx, inst.ID)
		if err != nil {
			return err
		}
		if len(now) < 2 {
			agent, m, err := s.addStandby(ctx, inst, params.Standby, params.StandbyNode, log)
			if err != nil {
				return err
			}
			if err := log.Info(ctx, "standby", "standby on %s is %s (%s behind)", agent.Node.Name, m.State, lagText(m.LagBytes())); err != nil {
				return err
			}
		}
	}
	return log.Info(ctx, "done", "the project's Patroni state is in %s's etcd cluster (was %s)", params.Region, from)
}

// restartLeader re-creates the leader's container from its current spec
// (its etcd cluster included), with the poolers holding clients.
func (s *Service) restartLeader(ctx context.Context, p store.Project, inst store.Instance, log *jobs.StepLogger) error {
	member := leaderKey(inst)
	spec, err := s.memberSpec(ctx, inst, member)
	if err != nil {
		return err
	}
	spec.Recreate = true
	agent, err := s.nodes.ForNode(ctx, inst.NodeID)
	if err != nil {
		return err
	}
	dbs := store.PoolerNames(p)
	start := time.Now()
	if _, err := s.projects.Pooler().Freeze(ctx, freezeWait, dbs...); err != nil {
		_ = s.projects.Pooler().Resume(context.WithoutCancel(ctx), dbs...)
		return fmt.Errorf("pause the poolers: %w", err)
	}
	resume := func() {
		if err := s.projects.Pooler().Resume(context.WithoutCancel(ctx), dbs...); err != nil && !isNotPaused(err) {
			s.log.Warn("pooler RESUME after the restart", "project", p.ID, "err", err)
		}
	}
	res, err := agent.CreateInstance(ctx, spec)
	if err != nil {
		resume()
		return fmt.Errorf("restart on the new etcd cluster: %w", err)
	}
	if err := s.recordRunning(ctx, inst, agent, res); err != nil {
		resume()
		return err
	}
	if err := s.recordMember(ctx, member, agent, res); err != nil {
		resume()
		return err
	}
	_, werr := s.waitMember(ctx, inst.ID, member, true, 2*time.Minute, log)
	if werr == nil && (inst.Host == nil || *inst.Host != res.Host || int(inst.Port) != res.Port) {
		werr = s.projects.SyncPooler(ctx, log, "pooler", "instance address changed")
	}
	resume()
	if werr != nil {
		return werr
	}
	return log.Info(ctx, "restart", "the primary runs on the new etcd cluster; clients were held for %s", time.Since(start).Round(time.Millisecond))
}
