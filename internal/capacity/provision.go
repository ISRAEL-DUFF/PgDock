package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/cloud"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/store"
)

// KindProvision creates a server for a proposal and waits for it to join:
// the agent registers with the token in its cloud-init, a shared node gets
// its shared cluster, and the node goes into service (V3 §5.1, §5.2).
const KindProvision = "capacity_provision"

type provisionParams struct {
	ProposalID uuid.UUID `json:"proposal_id"`
}

func opProposal(op store.Operation) (uuid.UUID, error) {
	var p provisionParams
	if err := json.Unmarshal(op.Params, &p); err != nil || p.ProposalID == uuid.Nil {
		return uuid.Nil, jobs.Permanent(fmt.Errorf("provision params: %w", err))
	}
	return p.ProposalID, nil
}

// labels mark the servers PGDock created.
func labels(name, region string) map[string]string {
	return map[string]string{"pgdock": "node", "pgdock-node": name, "pgdock-region": region}
}

func (s *Service) runProvision(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	id, err := opProposal(op)
	if err != nil {
		return err
	}
	q := store.New(s.db)
	p, err := q.GetCapacityProposal(ctx, id)
	if err != nil {
		return jobs.Permanent(err)
	}
	if p.Status != ProposalProvisioning {
		return jobs.Permanent(fmt.Errorf("the proposal is %s", p.Status))
	}
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}

	// 1. The node, named for its region.
	var node store.Node
	if p.NodeID != nil {
		if node, err = q.GetNode(ctx, *p.NodeID); err != nil {
			return err
		}
	} else {
		prefix := "pgd-" + p.Region + "-"
		n, err := q.NextNodeNumber(ctx, prefix)
		if err != nil {
			return err
		}
		role := "shared"
		if p.Tier == TierDedicated {
			role = "dedicated"
		}
		cost := p.MonthlyCostMinor
		typ := p.ServerType
		node, err = q.InsertProvisionedNode(ctx, store.InsertProvisionedNodeParams{
			Name: fmt.Sprintf("%s%d", prefix, n), PrivateAddr: "pending", Role: role, Provider: p.Provider, Region: p.Region,
			ServerType: &typ, MonthlyCostMinor: &cost, CostCurrency: p.Currency,
		})
		if err != nil {
			return err
		}
		if err := q.SetCapacityProposalNode(ctx, store.SetCapacityProposalNodeParams{ID: p.ID, NodeID: &node.ID}); err != nil {
			return err
		}
		_ = log.Info(ctx, "node", "node %s (%s, %s)", node.Name, role, p.Region)
	}

	// 2. The server, unless an earlier attempt created it.
	if node.ProviderServerID == nil {
		existing, err := s.provider.ListServers(ctx, cloud.Filter{Labels: map[string]string{"pgdock-node": node.Name}})
		if err != nil {
			return err
		}
		var srv cloud.Server
		if len(existing) > 0 {
			srv = existing[0]
			_ = log.Info(ctx, "server", "found server %s from an earlier attempt", srv.ID)
		} else {
			token, _, err := s.nodes.NewToken(ctx, node.ID)
			if err != nil {
				return err
			}
			b := s.cfg.Bootstrap
			b.NodeName, b.Token = node.Name, token
			userData, err := b.CloudInit()
			if err != nil {
				return jobs.Permanent(err)
			}
			srv, err = s.provider.CreateServer(ctx, cloud.ServerSpec{
				Name: node.Name, Type: p.ServerType, Location: p.Location, Image: s.cfg.Image, UserData: userData,
				PlacementGroup: s.cfg.PlacementGroup, Network: s.cfg.Network, SSHKeys: s.cfg.SSHKeys, Labels: labels(node.Name, p.Region),
			})
			if errors.Is(err, cloud.ErrManual) {
				return jobs.Permanent(err)
			}
			if err != nil {
				return err
			}
			_ = log.Info(ctx, "server", "created %s server %s (%s) in %s", s.provider.Name(), srv.ID, p.ServerType, srv.Location)
		}
		addr := srv.PrivateIP
		if addr == "" {
			addr = "pending"
		}
		if err := q.SetNodeServer(ctx, store.SetNodeServerParams{ID: node.ID, ProviderServerID: &srv.ID, PrivateAddr: addr}); err != nil {
			return err
		}
	}

	// 3. Its agent registers and answers.
	_ = log.Info(ctx, "join", "waiting for the agent on %s to register", node.Name)
	deadline := s.cfg.Now().Add(s.cfg.JoinTimeout)
	for {
		node, err = q.GetNode(ctx, node.ID)
		if err != nil {
			return err
		}
		if node.AgentCertFp != nil {
			if st := s.nodes.Check(ctx, node); st.Reachable {
				break
			}
		}
		if s.cfg.Now().After(deadline) {
			return jobs.Permanent(fmt.Errorf("the agent on %s didn't join within %s; check the server's cloud-init log (/var/log/cloud-init-output.log)", node.Name, s.cfg.JoinTimeout))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.cfg.Poll):
		}
	}
	if node.AgentHost != nil && node.PrivateAddr == "pending" {
		// The provider gave no private address: the agent advertises its
		// own, and instances there are reached at it.
		if err := q.SetNodePrivateAddr(ctx, store.SetNodePrivateAddrParams{ID: node.ID, PrivateAddr: *node.AgentHost}); err != nil {
			return err
		}
	}
	_ = log.Info(ctx, "join", "agent %s registered from %s", deref(node.AgentVersion), deref(node.AgentHost))

	// 4. A shared node's shared cluster.
	if p.Tier == TierShared {
		if err := s.sharedCluster(ctx, node, st.Shared.ClusterMemoryMB, log); err != nil {
			return err
		}
	}

	// 5. Into service: placement picks it up.
	if _, err := q.SetNodeLifecycle(ctx, store.SetNodeLifecycleParams{ID: node.ID, Lifecycle: "active"}); err != nil {
		return err
	}
	if err := q.FinishCapacityProposal(ctx, store.FinishCapacityProposalParams{ID: p.ID, Status: ProposalDone}); err != nil {
		return err
	}
	_ = log.Info(ctx, "done", "%s is in service", node.Name)
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sharedCluster creates the node's shared cluster and waits for it.
func (s *Service) sharedCluster(ctx context.Context, node store.Node, memMB int, log *jobs.StepLogger) error {
	q := store.New(s.db)
	if inst, err := q.SharedInstanceOnNode(ctx, node.ID); err == nil && inst.Status == "running" {
		return nil
	}
	op, err := s.ded.AddSharedCluster(ctx, node.ID, memMB, 0, nil)
	if err != nil {
		// An earlier attempt's cluster: wait for it to run.
		if _, ierr := q.SharedInstanceOnNode(ctx, node.ID); ierr != nil {
			return err
		}
		for {
			inst, err := q.SharedInstanceOnNode(ctx, node.ID)
			if err != nil {
				return err
			}
			switch inst.Status {
			case "running":
				return nil
			case "error":
				return fmt.Errorf("the shared cluster on %s failed", node.Name)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(s.cfg.Poll):
			}
		}
	}
	_ = log.Info(ctx, "shared_cluster", "creating the shared cluster (%d MB), operation %s", memMB, op.ID)
	for {
		o, err := jobs.Get(ctx, s.db, op.ID)
		if err != nil {
			return err
		}
		if jobs.IsTerminal(o.Status) {
			if o.Status != "succeeded" {
				return fmt.Errorf("the shared cluster on %s failed: %s", node.Name, deref(o.Error))
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.cfg.Poll):
		}
	}
}

// failProvision deletes the server a failed proposal created (nothing runs
// on it) and records the failure.
func (s *Service) failProvision(ctx context.Context, op store.Operation, log *jobs.StepLogger, cause error) error {
	id, err := opProposal(op)
	if err != nil {
		return err
	}
	q := store.New(s.db)
	p, err := q.GetCapacityProposal(ctx, id)
	if err != nil {
		return err
	}
	var errs []error
	if p.NodeID != nil {
		node, err := q.GetNode(ctx, *p.NodeID)
		if err == nil {
			if n, _ := q.NodeLiveInstances(ctx, node.ID); n == 0 {
				if node.ProviderServerID != nil {
					if err := s.provider.DeleteServer(ctx, *node.ProviderServerID); err != nil {
						errs = append(errs, err)
						_ = log.Warn(ctx, "rollback", "could not delete server %s: %v; delete it at the provider", *node.ProviderServerID, err)
					} else {
						_ = log.Info(ctx, "rollback", "deleted server %s", *node.ProviderServerID)
					}
				}
				if len(errs) == 0 {
					errs = append(errs, s.nodes.RemoveNode(ctx, node.ID))
				}
			}
		}
	}
	msg := cause.Error()
	errs = append(errs, q.FinishCapacityProposal(ctx, store.FinishCapacityProposalParams{ID: p.ID, Status: ProposalFailed, Error: &msg}))
	return errors.Join(errs...)
}
