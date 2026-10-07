package ha

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/faildomain"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Operation kinds: bootstrap a region's cluster on three nodes, and
// replace one member (V3.1 §3.2).
const (
	KindEtcdSetup   = "etcd_setup"
	KindEtcdReplace = "etcd_replace"
)

// Members is the size of the etcd cluster: it keeps a quorum when any one
// node is lost (V3 §2.2).
const Members = 3

// Service manages the etcd cluster.
type Service struct {
	db      *pgxpool.Pool
	keyring *crypto.Keyring
	nodes   *nodes.Service
	log     *slog.Logger

	mu sync.Mutex
	ca *CA
}

// New returns a Service.
func New(db *pgxpool.Pool, keyring *crypto.Keyring, ns *nodes.Service, log *slog.Logger) *Service {
	return &Service{db: db, keyring: keyring, nodes: ns, log: log}
}

// Kinds returns the operation kinds this service runs.
func (s *Service) Kinds() map[string]jobs.Kind {
	return map[string]jobs.Kind{
		KindEtcdSetup:   {Handler: s.runSetup, MaxAttempts: 2, Timeout: 15 * time.Minute},
		KindEtcdReplace: {Handler: s.runReplace, MaxAttempts: 3, Timeout: 15 * time.Minute},
	}
}

// CA returns the etcd certificate authority.
func (s *Service) CA(ctx context.Context) (*CA, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ca != nil {
		return s.ca, nil
	}
	ca, err := LoadCA(ctx, s.db, s.keyring)
	if err != nil {
		return nil, err
	}
	s.ca = ca
	return ca, nil
}

type setupParams struct {
	Region string      `json:"region"`
	Nodes  []uuid.UUID `json:"nodes"`
	Token  string      `json:"token"`
}

// Setup checks nodeIDs (three distinct nodes with reachable agents, in one
// region and three failure domains) and queues the bootstrap of that
// region's cluster (V3.1 §3.1). Each region has its own.
func (s *Service) Setup(ctx context.Context, nodeIDs []uuid.UUID, by *uuid.UUID) (store.Operation, error) {
	if len(nodeIDs) != Members {
		return store.Operation{}, fmt.Errorf("%w: the etcd cluster needs exactly %d nodes, one member on each", provision.ErrInvalid, Members)
	}
	seen := map[uuid.UUID]bool{}
	q := store.New(s.db)
	var picked []store.Node
	region := ""
	for _, id := range nodeIDs {
		if seen[id] {
			return store.Operation{}, fmt.Errorf("%w: the %d nodes must be different", provision.ErrInvalid, Members)
		}
		seen[id] = true
		n, err := q.GetNode(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Operation{}, fmt.Errorf("%w: node %s not found", provision.ErrInvalid, id)
		}
		if err != nil {
			return store.Operation{}, err
		}
		if n.AgentCertFp == nil || n.Status != "healthy" {
			return store.Operation{}, fmt.Errorf("%w: node %s has no healthy agent (%s)", provision.ErrConflict, n.Name, n.Status)
		}
		if region != "" && n.Region != region {
			return store.Operation{}, fmt.Errorf("%w: a cluster's members are in one region: %s is in %s, not %s", provision.ErrInvalid, n.Name, n.Region, region)
		}
		region = n.Region
		if m, err := q.GetEtcdMember(ctx, id); err == nil {
			return store.Operation{}, fmt.Errorf("%w: %s already holds a member of %s's cluster", provision.ErrConflict, n.Name, m.Region)
		}
		picked = append(picked, n)
	}
	// One member per failure domain (V3.1 §2.2): two that fail together
	// would take the quorum with them.
	if ok, pair := faildomain.AllSeparated(picked); !ok {
		return store.Operation{}, fmt.Errorf("%w: %s and %s are in the same failure domain; pick nodes in three different ones (%s)",
			provision.ErrConflict, pair[0].Name, pair[1].Name, faildomain.Describe(picked))
	}
	existing, err := q.ListRegionEtcdMembers(ctx, region)
	if err != nil {
		return store.Operation{}, err
	}
	if len(existing) > 0 {
		return store.Operation{}, fmt.Errorf("%w: %s's etcd cluster is already set up; replace a member instead", provision.ErrConflict, region)
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	var op store.Operation
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// One change to etcd at a time, platform-wide.
		if err := s.noEtcdOperation(ctx, tx); err != nil {
			return err
		}
		var err error
		op, err = jobs.Enqueue(ctx, tx, jobs.EnqueueParams{Kind: KindEtcdSetup, CreatedBy: by,
			Params: setupParams{Region: region, Nodes: nodeIDs, Token: "pgdock-" + hex.EncodeToString(b)}})
		return err
	})
	return op, err
}

// noEtcdOperation refuses while an etcd setup or replacement runs.
func (s *Service) noEtcdOperation(ctx context.Context, tx pgx.Tx) error {
	var running int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM operations WHERE kind = ANY($1) AND status IN ('queued', 'running')`,
		[]string{KindEtcdSetup, KindEtcdReplace}).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return fmt.Errorf("%w: an etcd cluster is being set up or repaired; try again when it finishes", provision.ErrConflict)
	}
	return nil
}

// memberName is a node's member name in the cluster.
func memberName(node uuid.UUID) string {
	return "etcd-" + strings.ReplaceAll(node.String(), "-", "")[:12]
}

func url(host string, port int) string {
	return "https://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func (s *Service) runSetup(ctx context.Context, op store.Operation, log *jobs.StepLogger) error {
	var p setupParams
	if err := json.Unmarshal(op.Params, &p); err != nil {
		return jobs.Permanent(err)
	}
	ca, err := s.CA(ctx)
	if err != nil {
		return err
	}
	type plan struct {
		node  uuid.UUID
		agent *nodes.Agent
		addr  agentapi.EtcdAddress
		name  string
	}
	var plans []plan
	var initial []string
	for _, id := range p.Nodes {
		agent, err := s.nodes.ForNode(ctx, id)
		if err != nil {
			return err
		}
		addr, err := agent.EtcdAddress(ctx)
		if err != nil {
			return jobs.Permanent(fmt.Errorf("node %s: %w", agent.Node.Name, err))
		}
		pl := plan{node: id, agent: agent, addr: addr, name: memberName(id)}
		plans = append(plans, pl)
		initial = append(initial, pl.name+"="+url(addr.Host, addr.PeerPort))
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.DeleteRegionEtcdMembers(ctx, p.Region); err != nil {
			return err
		}
		for _, pl := range plans {
			if err := q.InsertEtcdMember(ctx, store.InsertEtcdMemberParams{NodeID: pl.node, Name: pl.name, Region: p.Region,
				ClientUrl: url(pl.addr.Host, pl.addr.ClientPort), PeerUrl: url(pl.addr.Host, pl.addr.PeerPort)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, pl := range plans {
		pair, err := ca.Member(pl.name, []string{pl.addr.Host})
		if err != nil {
			return err
		}
		// A new cluster: any data from an earlier, failed attempt goes.
		if _, err := pl.agent.RunEtcd(ctx, agentapi.EtcdSpec{
			Name: pl.name, InitialCluster: strings.Join(initial, ","), State: "new", Token: p.Token,
			CAPEM: string(ca.CertPEM()), CertPEM: pair.CertPEM, KeyPEM: pair.KeyPEM, Wipe: true,
		}); err != nil {
			return fmt.Errorf("start etcd on %s: %w", pl.agent.Node.Name, err)
		}
		if err := log.Info(ctx, "etcd", "member %s started on node %s (%s)", pl.name, pl.agent.Node.Name, pl.addr.Host); err != nil {
			return err
		}
	}
	if err := s.waitHealthy(ctx, p.Region, len(plans), 3*time.Minute); err != nil {
		return err
	}
	return log.Info(ctx, "done", "%s's etcd cluster of %d members is healthy; HA can now be enabled on its dedicated projects", p.Region, len(plans))
}

// Member is an etcd member and its last known health.
type Member = store.ListEtcdMembersRow

// Refresh asks each member's agent for its health and records it.
func (s *Service) Refresh(ctx context.Context) ([]Member, error) {
	q := store.New(s.db)
	ms, err := q.ListEtcdMembers(ctx)
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	for i := range ms {
		wg.Add(1)
		go func(m *Member) {
			defer wg.Done()
			status, msg := "unhealthy", ""
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			agent, err := s.nodes.ForNode(cctx, m.NodeID)
			if err == nil {
				var e agentapi.Etcd
				if e, err = agent.Etcd(cctx); err == nil {
					if e.Healthy {
						status = "healthy"
					} else {
						msg = e.Error
						if msg == "" {
							msg = "container " + e.State
						}
					}
				}
			}
			if err != nil {
				msg = err.Error()
			}
			m.Status, m.Error = status, nil
			if msg != "" {
				m.Error = &msg
			}
			now := time.Now()
			m.CheckedAt = &now
			if err := q.SetEtcdMemberStatus(context.WithoutCancel(ctx), store.SetEtcdMemberStatusParams{NodeID: m.NodeID, Status: status, Error: m.Error}); err != nil {
				s.log.Warn("record etcd member health", "member", m.Name, "err", err)
			}
		}(&ms[i])
	}
	wg.Wait()
	return ms, nil
}

// waitHealthy waits until region's cluster has want healthy members.
func (s *Service) waitHealthy(ctx context.Context, region string, want int, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		if _, err := s.Refresh(ctx); err != nil {
			return err
		}
		ms, err := store.New(s.db).ListRegionEtcdMembers(ctx, region)
		if err != nil {
			return err
		}
		healthy := 0
		for _, m := range ms {
			if m.Status == "healthy" {
				healthy++
			}
		}
		if healthy >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s's etcd cluster didn't become healthy: %d of %d members healthy", region, healthy, want)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Members are region's cluster's members, with their last known health.
func (s *Service) Members(ctx context.Context, region string) ([]store.ListRegionEtcdMembersRow, error) {
	return store.New(s.db).ListRegionEtcdMembers(ctx, region)
}

// Endpoints are region's members' client URLs, for Patroni.
func (s *Service) Endpoints(ctx context.Context, region string) ([]string, error) {
	ms, err := store.New(s.db).ListRegionEtcdMembers(ctx, region)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range ms {
		out = append(out, m.ClientUrl)
	}
	slices.Sort(out)
	return out, nil
}

// Ready reports whether HA instances in region can use its cluster: all
// members set up and a quorum of them healthy at the last check.
func (s *Service) Ready(ctx context.Context, region string) error {
	ms, err := store.New(s.db).ListRegionEtcdMembers(ctx, region)
	if err != nil {
		return err
	}
	if len(ms) < Members {
		return fmt.Errorf("%w: %s has no etcd cluster yet (Platform → Nodes → etcd cluster for HA, region %s)", provision.ErrConflict, region, region)
	}
	healthy := 0
	for _, m := range ms {
		if m.Status == "healthy" {
			healthy++
		}
	}
	if healthy <= len(ms)/2 {
		return fmt.Errorf("%w: only %d of %d etcd members are healthy", provision.ErrConflict, healthy, len(ms))
	}
	return nil
}

// Run refreshes the members' health every interval until ctx ends.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("etcd health", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
