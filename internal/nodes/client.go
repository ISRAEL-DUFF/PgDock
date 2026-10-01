package nodes

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/agentca"
	"github.com/israel-duff/pgdock/internal/store"
)

// Agent is a connection to one node's agent, verified against the CA and
// the node's pinned certificate fingerprint.
type Agent struct {
	Node store.Node
	base string
	http *http.Client
}

// ErrNoAgent means the node has no registered agent.
var ErrNoAgent = errors.New("node has no registered agent")

func (s *Service) agentFor(n store.Node) (*Agent, error) {
	if n.AgentCertFp == nil || *n.AgentCertFp == "" {
		return nil, fmt.Errorf("%w (node %s)", ErrNoAgent, n.Name)
	}
	host := n.PrivateAddr
	if n.AgentHost != nil && *n.AgentHost != "" {
		host = *n.AgentHost
	}
	pin := *n.AgentCertFp
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{s.clientCert},
		RootCAs:      s.ca.Pool(),
		ServerName:   host,
		MinVersion:   tls.VersionTLS13,
		VerifyPeerCertificate: func(raw [][]byte, chains [][]*x509.Certificate) error {
			if len(raw) == 0 || agentca.Fingerprint(raw[0]) != pin {
				return errors.New("agent certificate does not match the pinned fingerprint")
			}
			if id, ok := agentca.NodeIDFromCert(chains[0][0]); !ok || id != n.ID.String() {
				return errors.New("agent certificate is for another node")
			}
			return nil
		},
	}
	return &Agent{
		Node: n,
		base: "https://" + net.JoinHostPort(host, strconv.Itoa(int(n.AgentPort))),
		http: &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg, ResponseHeaderTimeout: 0, IdleConnTimeout: time.Minute}},
	}, nil
}

func (a *Agent) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("agent %s: %w", a.Node.Name, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		var e agentapi.Error
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("agent %s: %s", a.Node.Name, e.Error)
		}
		return fmt.Errorf("agent %s: %s", a.Node.Name, res.Status)
	}
	return json.Unmarshal(raw, out)
}

// Health calls GET /v1/health.
func (a *Agent) Health(ctx context.Context) (agentapi.Health, error) {
	var h agentapi.Health
	return h, a.do(ctx, http.MethodGet, agentapi.PathHealth, nil, &h)
}

// Metrics calls GET /v1/host/metrics.
func (a *Agent) Metrics(ctx context.Context) (agentapi.HostMetrics, error) {
	var m agentapi.HostMetrics
	return m, a.do(ctx, http.MethodGet, agentapi.PathMetrics, nil, &m)
}

// Dump calls POST /v1/dump.
func (a *Agent) Dump(ctx context.Context, req agentapi.DumpRequest) (agentapi.DumpResult, error) {
	var r agentapi.DumpResult
	return r, a.do(ctx, http.MethodPost, agentapi.PathDump, req, &r)
}

// Restore calls POST /v1/restore.
func (a *Agent) Restore(ctx context.Context, req agentapi.RestoreRequest) (agentapi.RestoreResult, error) {
	var r agentapi.RestoreResult
	return r, a.do(ctx, http.MethodPost, agentapi.PathRestore, req, &r)
}

// Copy calls POST /v1/copy.
func (a *Agent) Copy(ctx context.Context, req agentapi.CopyRequest) (agentapi.RestoreResult, error) {
	var r agentapi.RestoreResult
	return r, a.do(ctx, http.MethodPost, agentapi.PathCopy, req, &r)
}
