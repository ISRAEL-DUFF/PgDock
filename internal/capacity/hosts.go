package capacity

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/cloud"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// HostFor provides a dedicated host in region for a new instance of need
// when none has room (V4.1 §5.3; dedicated.HostFunc). It opens (or joins)
// the region's dedicated proposal: applied within the budget, it returns
// the node being set up, which the creation waits for; waiting for the
// admin, dedicated.ErrHostPending; with hosts added by hand or dedicated
// automation off, provision.ErrNoCapacity.
func (s *Service) HostFor(ctx context.Context, region string, need dedicated.Size) (uuid.UUID, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if s.provider.Name() == cloud.Manual || !st.Dedicated.Enabled {
		return uuid.Nil, fmt.Errorf("%w: no dedicated node in %s has room, and hosts there are added by the platform admin", provision.ErrNoCapacity, region)
	}
	q := store.New(s.db)
	p, err := q.OpenCapacityProposal(ctx, store.OpenCapacityProposalParams{Region: region, Tier: TierDedicated})
	if errors.Is(err, pgx.ErrNoRows) {
		reason := fmt.Sprintf("A new dedicated instance in %s (%g vCPU, %d MB, %d GB) found no host with room.", region, need.CPUs, need.MemoryMB, need.DiskGB)
		made, err := s.propose(ctx, st, region, TierDedicated, reason)
		if err != nil {
			return uuid.Nil, err
		}
		if made == nil { // another request opened one first
			if p, err = q.OpenCapacityProposal(ctx, store.OpenCapacityProposalParams{Region: region, Tier: TierDedicated}); err != nil {
				return uuid.Nil, err
			}
		} else {
			p = *made
		}
	} else if err != nil {
		return uuid.Nil, err
	}
	if p.Status != ProposalProvisioning {
		return uuid.Nil, fmt.Errorf("%w (%s, %s a month)", dedicated.ErrHostPending, p.ServerType, money(p.MonthlyCostMinor, p.Currency))
	}
	node, _, err := s.proposalNode(ctx, p.ID)
	if err != nil {
		return uuid.Nil, err
	}
	s.log.Info("dedicated host on demand", "region", region, "node", node.Name, "proposal", p.ID)
	return node.ID, nil
}
