package dedicated

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Dedicated hosts on demand (V4.1 §5.3): when no node in the region has
// room for a new instance, the capacity service is asked for one. Within
// the infrastructure budget its server is created at once and the
// project's creation waits for it to join; over the budget the creation is
// refused while the proposal waits for the platform admin.

// ErrHostPending refuses a creation whose host waits for the admin's
// approval.
var ErrHostPending = errors.New("a dedicated host has been requested and waits for the platform admin's approval")

// HostWait is how long a creation waits for a host being provisioned.
const HostWait = 25 * time.Minute

// HostFunc provides a node in region for an instance of size: a node being
// provisioned (its row exists, lifecycle provisioning), ErrHostPending, or
// provision.ErrNoCapacity when hosts can't be added automatically there.
type HostFunc func(ctx context.Context, region string, need Size) (uuid.UUID, error)

// placeDedicated picks the node in region with room for need and the fewest
// instances, or asks for a host.
func (s *Service) placeDedicated(ctx context.Context, q *store.Queries, region string, need Size) (uuid.UUID, error) {
	nodes, err := q.CapacityNodes(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	var candidates []store.CapacityNodesRow
	full := ""
	for _, n := range nodes {
		if n.Region != region || (n.Role != "dedicated" && n.Role != "both") || n.Status != "healthy" ||
			n.Lifecycle != "active" || n.AgentCertFp == nil {
			continue
		}
		if ok, why := fits(n, need.CPUs, int64(need.MemoryMB), int64(need.DiskGB)); !ok {
			full = why
			continue
		}
		candidates = append(candidates, n)
	}
	if len(candidates) > 0 {
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Instances < candidates[j].Instances })
		return candidates[0].ID, nil
	}
	if s.Hosts != nil {
		id, err := s.Hosts(ctx, region, need)
		if err == nil || !errors.Is(err, provision.ErrNoCapacity) {
			return id, err
		}
	}
	if full != "" {
		return uuid.Nil, fmt.Errorf("%w: no dedicated node in %s has room for %g vCPU, %d MB and %d GB (%s), and hosts there are added by hand",
			provision.ErrNoCapacity, region, need.CPUs, need.MemoryMB, need.DiskGB, full)
	}
	return uuid.Nil, fmt.Errorf("%w: no healthy node with an agent in %s accepts dedicated instances", provision.ErrNoCapacity, region)
}

// waitForHost waits for a node being provisioned for this instance to join
// and come into service.
func (s *Service) waitForHost(ctx context.Context, nodeID uuid.UUID, log *jobs.StepLogger) error {
	q := store.New(s.db)
	n, err := q.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if n.Lifecycle != "provisioning" {
		return nil
	}
	if err := log.Info(ctx, "host", "waiting for a host: %s is being set up (usually 5–10 minutes)", n.Name); err != nil {
		return err
	}
	deadline := time.Now().Add(HostWait)
	for {
		n, err = q.GetNode(ctx, nodeID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && n.Status == "removed") {
			return jobs.Permanent(errors.New("the host being set up for this project was removed"))
		}
		if err != nil {
			return err
		}
		if n.Lifecycle == "active" && n.AgentCertFp != nil {
			return log.Info(ctx, "host", "%s is in service", n.Name)
		}
		var status string
		if err := s.db.QueryRow(ctx, `SELECT status FROM capacity_proposals WHERE node_id = $1 ORDER BY created_at DESC LIMIT 1`, nodeID).Scan(&status); err == nil && status == "failed" {
			return jobs.Permanent(fmt.Errorf("setting up %s failed; the platform admin has the provisioning log", n.Name))
		}
		if time.Now().After(deadline) {
			return jobs.Permanent(fmt.Errorf("%s wasn't in service within %s", n.Name, HostWait))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.hostPoll()):
		}
	}
}

func (s *Service) hostPoll() time.Duration {
	if s.cfg.HostPoll > 0 {
		return s.cfg.HostPoll
	}
	return 5 * time.Second
}
