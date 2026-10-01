// Package nodes registers nodes' agents and talks to them (spec §3.3, M3
// "Agent v0").
package nodes

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/agentca"
	"github.com/israel-duff/pgdock/internal/store"
)

// Errors for the API layer.
var (
	ErrBadToken = errors.New("the registration token is invalid or has expired")
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("node not found")
)

// Service manages nodes and their agents.
type Service struct {
	db         *pgxpool.Pool
	ca         *agentca.CA
	clientCert tls.Certificate
	bootstrap  string // install-time token, see Register
	log        *slog.Logger

	mu     sync.Mutex
	health map[uuid.UUID]Status
}

// Status is the last known state of a node's agent.
type Status struct {
	Reachable bool
	Health    agentapi.Health
	Metrics   agentapi.HostMetrics
	Err       string
	CheckedAt time.Time
}

// NewService returns a Service. bootstrapToken (PGDOCK_AGENT_BOOTSTRAP_TOKEN)
// lets the install bundle's own agent register the local node without an
// operator; empty disables it.
func NewService(db *pgxpool.Pool, ca *agentca.CA, bootstrapToken string, log *slog.Logger) (*Service, error) {
	cert, err := ca.ServerClientCert()
	if err != nil {
		return nil, err
	}
	return &Service{db: db, ca: ca, clientCert: cert, bootstrap: bootstrapToken, log: log, health: map[uuid.UUID]Status{}}, nil
}

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// NewToken creates a one-time registration token for a node, valid for a
// day, replacing any earlier one.
func (s *Service) NewToken(ctx context.Context, nodeID uuid.UUID) (string, time.Time, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	tok := "pgdreg_" + base64.RawURLEncoding.EncodeToString(b)
	exp := time.Now().Add(24 * time.Hour)
	h := hashToken(tok)
	if err := store.New(s.db).SetRegistrationToken(ctx, store.SetRegistrationTokenParams{ID: nodeID, RegistrationToken: &h, RegistrationExpiresAt: &exp}); err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// Register signs an agent's CSR and pins its certificate on the node. The
// token is either a node's one-time token or, with req.Node naming a node
// that has no agent yet, the bootstrap token.
func (s *Service) Register(ctx context.Context, req agentapi.RegisterRequest) (agentapi.RegisterResponse, error) {
	if req.AdvertiseHost == "" || req.AdvertisePort < 1 || req.AdvertisePort > 65535 || strings.ContainsAny(req.AdvertiseHost, " /") {
		return agentapi.RegisterResponse{}, fmt.Errorf("%w: advertise host and port are required", ErrInvalid)
	}
	var out agentapi.RegisterResponse
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		var node store.Node
		var err error
		if s.bootstrap != "" && req.Node != "" && subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.bootstrap)) == 1 {
			node, err = q.GetNodeByName(ctx, req.Node)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: no node named %q", ErrBadToken, req.Node)
			}
			if err == nil && node.AgentCertFp != nil {
				// The bootstrap token only claims nodes without an agent;
				// re-registering needs a token from the UI.
				return fmt.Errorf("%w: node %q already has an agent; issue a new token from the UI", ErrBadToken, req.Node)
			}
		} else {
			h := hashToken(req.Token)
			node, err = q.GetNodeByRegistrationToken(ctx, &h)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrBadToken
			}
		}
		if err != nil {
			return err
		}
		certPEM, fp, err := s.ca.SignAgent([]byte(req.CSR), node.ID.String(), []string{req.AdvertiseHost})
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		v := req.Version
		if _, err := q.CompleteNodeRegistration(ctx, store.CompleteNodeRegistrationParams{
			ID: node.ID, AgentCertFp: &fp, AgentHost: &req.AdvertiseHost, AgentPort: int32(req.AdvertisePort), AgentVersion: &v,
		}); err != nil {
			return err
		}
		out = agentapi.RegisterResponse{NodeID: node.ID.String(), CertPEM: string(certPEM), CAPEM: string(s.ca.CertPEM())}
		s.log.Info("agent registered", "node", node.Name, "advertise", fmt.Sprintf("%s:%d", req.AdvertiseHost, req.AdvertisePort), "fingerprint", fp)
		return nil
	})
	return out, err
}

// ForNode returns the agent of a node.
func (s *Service) ForNode(ctx context.Context, nodeID uuid.UUID) (*Agent, error) {
	n, err := store.New(s.db).GetNode(ctx, nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.agentFor(n)
}

// ForInstance returns the agent of the node hosting an instance.
func (s *Service) ForInstance(ctx context.Context, instanceID uuid.UUID) (*Agent, error) {
	n, err := store.New(s.db).NodeForInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("node for instance %s: %w", instanceID, err)
	}
	return s.agentFor(n)
}

// Any returns the first node with a registered agent (for work that is not
// tied to a project, like metadata self-backups).
func (s *Service) Any(ctx context.Context) (*Agent, error) {
	n, err := store.New(s.db).FirstAgentNode(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoAgent
	}
	if err != nil {
		return nil, err
	}
	return s.agentFor(n)
}

// Status returns the last health check of a node.
func (s *Service) Status(id uuid.UUID) (Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.health[id]
	return st, ok
}

// Check polls one node's agent now.
func (s *Service) Check(ctx context.Context, n store.Node) Status {
	st := Status{CheckedAt: time.Now()}
	a, err := s.agentFor(n)
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st.Health, err = a.Health(cctx)
		if err == nil {
			st.Metrics, err = a.Metrics(cctx)
		}
		cancel()
	}
	if err != nil {
		st.Err = err.Error()
	} else {
		st.Reachable = true
	}
	s.mu.Lock()
	s.health[n.ID] = st
	s.mu.Unlock()

	if n.AgentCertFp != nil {
		status := "unreachable"
		capacity := json.RawMessage(`{}`)
		var ver *string
		if st.Reachable {
			status = "healthy"
			capacity, _ = json.Marshal(st.Metrics)
			ver = &st.Health.Version
		}
		_ = store.New(s.db).RecordNodeHeartbeat(ctx, store.RecordNodeHeartbeatParams{ID: n.ID, Status: status, Capacity: capacity, AgentVersion: ver})
	}
	return st
}

// Run checks every agent every interval until ctx ends (spec §8.7: node
// metrics every 30s).
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		ns, err := store.New(s.db).ListNodes(ctx)
		if err == nil {
			for _, n := range ns {
				if n.AgentCertFp != nil {
					s.Check(ctx, n)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// List returns all nodes.
func (s *Service) List(ctx context.Context) ([]store.Node, error) {
	return store.New(s.db).ListNodes(ctx)
}
