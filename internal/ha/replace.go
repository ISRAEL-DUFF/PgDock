package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/faildomain"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

type replaceParams struct {
	Region string    `json:"region"`
	Old    uuid.UUID `json:"old"`
	New    uuid.UUID `json:"new"`
}

// Replace queues replacing the member on oldNode (dead or alive) with one
// on newNode, or on the best other node in the region when newNode is nil
// (V3.1 §3.2). The other members must be healthy: the cluster runs with
// two of three for the few seconds between removing the old member and
// starting the new one.
func (s *Service) Replace(ctx context.Context, oldNode uuid.UUID, newNode *uuid.UUID, by *uuid.UUID) (store.Operation, error) {
	q := store.New(s.db)
	old, err := q.GetEtcdMember(ctx, oldNode)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Operation{}, fmt.Errorf("%w: that node holds no etcd member", provision.ErrInvalid)
	}
	if err != nil {
		return store.Operation{}, err
	}
	if _, err := s.Refresh(ctx); err != nil {
		return store.Operation{}, err
	}
	members, err := q.ListRegionEtcdMembers(ctx, old.Region)
	if err != nil {
		return store.Operation{}, err
	}
	var keep []store.Node
	for _, m := range members {
		if m.NodeID == oldNode {
			continue
		}
		if m.Status != "healthy" {
			return store.Operation{}, fmt.Errorf("%w: member %s on %s is %s; replacing another member now could lose the quorum, so bring it back (or replace it) first",
				provision.ErrConflict, m.Name, m.NodeName, m.Status)
		}
		n, err := q.GetNode(ctx, m.NodeID)
		if err != nil {
			return store.Operation{}, err
		}
		keep = append(keep, n)
	}
	var target store.Node
	if newNode != nil {
		if target, err = q.GetNode(ctx, *newNode); errors.Is(err, pgx.ErrNoRows) {
			return store.Operation{}, fmt.Errorf("%w: node %s not found", provision.ErrInvalid, *newNode)
		} else if err != nil {
			return store.Operation{}, err
		}
		if err := s.eligible(ctx, target, old.Region, oldNode, keep); err != nil {
			return store.Operation{}, err
		}
	} else if target, err = s.Candidate(ctx, old.Region, oldNode, keep); err != nil {
		return store.Operation{}, err
	}
	var op store.Operation
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := s.noEtcdOperation(ctx, tx); err != nil {
			return err
		}
		var err error
		op, err = jobs.Enqueue(ctx, tx, jobs.EnqueueParams{Kind: KindEtcdReplace, CreatedBy: by,
			Params: replaceParams{Region: old.Region, Old: oldNode, New: target.ID}})
		return err
	})
	return op, err
}

// eligible reports whether n can take region's member that was on old:
// in the region, a healthy agent, no member already, not a pooler host,
// in service, and in a failure domain none of keep uses.
func (s *Service) eligible(ctx context.Context, n store.Node, region string, old uuid.UUID, keep []store.Node) error {
	switch {
	case n.ID == old:
		return fmt.Errorf("%w: the new member must be on another node", provision.ErrInvalid)
	case n.Region != region:
		return fmt.Errorf("%w: %s is in %s; %s's members stay in %s", provision.ErrInvalid, n.Name, n.Region, region, region)
	case n.AgentCertFp == nil || n.Status != "healthy":
		return fmt.Errorf("%w: %s has no healthy agent (%s)", provision.ErrConflict, n.Name, n.Status)
	case n.Role == nodes.RolePooler:
		return fmt.Errorf("%w: %s is a pooler host", provision.ErrInvalid, n.Name)
	case n.Lifecycle != "active":
		return fmt.Errorf("%w: %s is %s", provision.ErrConflict, n.Name, n.Lifecycle)
	}
	if _, err := store.New(s.db).GetEtcdMember(ctx, n.ID); err == nil {
		return fmt.Errorf("%w: %s already holds an etcd member", provision.ErrConflict, n.Name)
	}
	for _, k := range keep {
		if !faildomain.Separated(k, n) {
			return fmt.Errorf("%w: %s shares a failure domain with member %s (%s)", provision.ErrConflict, n.Name, k.Name, faildomain.Describe([]store.Node{k, n}))
		}
	}
	return nil
}

// Candidate is the node a replacement member goes to: an eligible node in
// region, preferring one already running fewest instances.
func (s *Service) Candidate(ctx context.Context, region string, old uuid.UUID, keep []store.Node) (store.Node, error) {
	q := store.New(s.db)
	all, err := q.ListNodes(ctx)
	if err != nil {
		return store.Node{}, err
	}
	var best *store.Node
	bestLoad := 1 << 30
	var why []string
	for i, n := range all {
		if n.Region != region || n.Status == "removed" || n.ID == old || n.Role == nodes.RolePooler {
			continue
		}
		if err := s.eligible(ctx, n, region, old, keep); err != nil {
			why = append(why, err.Error())
			continue
		}
		insts, err := q.ListNodeInstances(ctx, n.ID)
		if err != nil {
			return store.Node{}, err
		}
		if len(insts) < bestLoad {
			best, bestLoad = &all[i], len(insts)
		}
	}
	if best == nil {
		msg := "add a node in another failure domain"
		if len(why) > 0 {
			msg = strings.Join(why, "; ")
		}
		return store.Node{}, fmt.Errorf("%w: no node in %s can take the etcd member: %s", provision.ErrConflict, region, msg)
	}
	return *best, nil
}

// ReplaceOnDrain queues moving node's etcd member, if it holds one, to
// another node (a drained node is leaving). It returns nil when the node
// holds no member.
func (s *Service) ReplaceOnDrain(ctx context.Context, node uuid.UUID) (*store.Operation, error) {
	if _, err := store.New(s.db).GetEtcdMember(ctx, node); errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	op, err := s.Replace(ctx, node, nil, nil)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func (s *Service) runReplace(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	var p replaceParams
	if err := json.Unmarshal(op.Params, &p); err != nil {
		return jobs.Permanent(err)
	}
	q := store.New(s.db)
	ca, err := s.CA(ctx)
	if err != nil {
		return err
	}
	client, err := ca.Client("pgdock-server")
	if err != nil {
		return err
	}
	req := func(action string) agentapi.EtcdMembersRequest {
		return agentapi.EtcdMembersRequest{Action: action, CAPEM: string(ca.CertPEM()), CertPEM: client.CertPEM, KeyPEM: client.KeyPEM}
	}
	newAgent, err := s.nodes.ForNode(ctx, p.New)
	if err != nil {
		return err
	}
	addr, err := newAgent.EtcdAddress(ctx)
	if err != nil {
		return jobs.Permanent(fmt.Errorf("node %s: %w", newAgent.Node.Name, err))
	}
	newName, newPeer := memberName(p.New), url(addr.Host, addr.PeerPort)

	// 1. A remaining member's agent talks to the cluster.
	members, err := q.ListRegionEtcdMembers(ctx, p.Region)
	if err != nil {
		return err
	}
	var via *nodes.Agent
	var list agentapi.EtcdMembers
	for _, m := range members {
		if m.NodeID == p.Old || m.NodeID == p.New {
			continue
		}
		a, err := s.nodes.ForNode(ctx, m.NodeID)
		if err != nil {
			continue
		}
		if list, err = a.EtcdMembers(ctx, req("list")); err == nil {
			via = a
			break
		}
	}
	if via == nil {
		return errors.New("no remaining member of the cluster answers")
	}
	oldName := memberName(p.Old)
	oldAlive := false
	for _, m := range members {
		if m.NodeID == p.Old {
			oldAlive = m.Status == "healthy"
		}
	}

	remove := func() error {
		for _, m := range list.Members {
			if m.Name != oldName {
				continue
			}
			r := req("remove")
			r.MemberID = m.ID
			if list, err = settle(func() (agentapi.EtcdMembers, error) { return via.EtcdMembers(ctx, r) }); err != nil {
				return fmt.Errorf("remove member %s: %w", oldName, err)
			}
			if err := log.Info(ctx, "remove", "removed member %s from %s's cluster", oldName, p.Region); err != nil {
				return err
			}
			break
		}
		return q.DeleteEtcdMember(ctx, p.Old)
	}
	add := func() error {
		if !slices.ContainsFunc(list.Members, func(m agentapi.EtcdClusterMember) bool { return slices.Contains(m.PeerURLs, newPeer) }) {
			r := req("add")
			r.PeerURL = newPeer
			if list, err = settle(func() (agentapi.EtcdMembers, error) { return via.EtcdMembers(ctx, r) }); err != nil {
				return fmt.Errorf("add member on %s: %w", newAgent.Node.Name, err)
			}
		}
		if _, err := q.GetEtcdMember(ctx, p.New); errors.Is(err, pgx.ErrNoRows) {
			if err := q.InsertEtcdMember(ctx, store.InsertEtcdMemberParams{NodeID: p.New, Name: newName, Region: p.Region,
				ClientUrl: url(addr.Host, addr.ClientPort), PeerUrl: newPeer}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		var initial []string
		for _, m := range list.Members {
			name := m.Name
			if slices.Contains(m.PeerURLs, newPeer) {
				name = newName
			}
			if name == "" || len(m.PeerURLs) == 0 {
				continue
			}
			initial = append(initial, name+"="+m.PeerURLs[0])
		}
		pair, err := ca.Member(newName, []string{addr.Host})
		if err != nil {
			return err
		}
		if _, err := newAgent.RunEtcd(ctx, agentapi.EtcdSpec{
			Name: newName, InitialCluster: strings.Join(initial, ","), State: "existing", Token: "pgdock-join",
			CAPEM: string(ca.CertPEM()), CertPEM: pair.CertPEM, KeyPEM: pair.KeyPEM, Wipe: true,
		}); err != nil {
			return fmt.Errorf("start etcd on %s: %w", newAgent.Node.Name, err)
		}
		if err := log.Info(ctx, "add", "member %s started on node %s and joining", newName, newAgent.Node.Name); err != nil {
			return err
		}
		if err := s.waitHealthy(ctx, p.Region, len(list.Members), 3*time.Minute); err != nil {
			return err
		}
		// The member list now names the new member, for a removal after.
		list, err = via.EtcdMembers(ctx, req("list"))
		return err
	}
	if oldAlive {
		// A live member (a drain): add the new one first, so the cluster
		// never has fewer than three members.
		if err := add(); err != nil {
			return err
		}
		if err := remove(); err != nil {
			return err
		}
	} else {
		// A dead member: remove it first, so the cluster's quorum is two
		// of the two that answer rather than three of four.
		if err := remove(); err != nil {
			return err
		}
		if err := add(); err != nil {
			return err
		}
	}
	if err := s.waitHealthy(ctx, p.Region, Members, 3*time.Minute); err != nil {
		return err
	}

	// 2. The old node's container goes, if the node still answers.
	if a, err := s.nodes.ForNode(ctx, p.Old); err == nil {
		if err := a.RemoveEtcd(ctx); err != nil {
			_ = log.Info(ctx, "cleanup", "couldn't remove the old member's container on %s: %v", a.Node.Name, err)
		}
	}
	// Patroni members learn the new member from the cluster; containers
	// created from now on are given the new list.
	return log.Info(ctx, "done", "%s's etcd cluster is back to %d healthy members, with %s on %s", p.Region, Members, newName, newAgent.Node.Name)
}

// settle retries a membership change etcd refuses as an "unhealthy
// cluster": its strict reconfiguration check counts only members the
// leader has heard from steadily for a few seconds, which a member that
// just (re)joined, or a cluster that just elected a leader, isn't yet.
func settle(f func() (agentapi.EtcdMembers, error)) (agentapi.EtcdMembers, error) {
	deadline := time.Now().Add(90 * time.Second)
	for {
		out, err := f()
		if err == nil || time.Now().After(deadline) || !strings.Contains(err.Error(), "unhealthy cluster") {
			return out, err
		}
		time.Sleep(2 * time.Second)
	}
}
