package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/agentapi"
)

// Patroni talks to a member's REST API. Reading the cluster needs no
// credentials; changing it (switchover, configuration) takes the REST
// password.
type Patroni struct {
	HTTP *http.Client
}

// DefaultPatroni has short timeouts: a member that doesn't answer in two
// seconds counts as down.
var DefaultPatroni = Patroni{HTTP: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}}

// ClusterMember is one member as Patroni's /cluster lists it.
type ClusterMember struct {
	Name     string `json:"name"`
	Role     string `json:"role"`  // leader, replica, sync_standby, standby_leader
	State    string `json:"state"` // running, streaming, starting, stopped, …
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Timeline int    `json:"timeline"`
	// Lag is bytes behind the leader; Patroni reports "unknown" when it
	// can't tell.
	Lag json.RawMessage `json:"lag,omitempty"`
}

// LagBytes is the member's lag, or -1 when unknown.
func (m ClusterMember) LagBytes() int64 {
	var n int64
	if err := json.Unmarshal(m.Lag, &n); err != nil {
		return -1
	}
	return n
}

// Cluster is Patroni's /cluster.
type Cluster struct {
	Members []ClusterMember `json:"members"`
	Scope   string          `json:"scope"`
}

// Leader is the member holding the leader lock, if any.
func (c Cluster) Leader() *ClusterMember {
	for i, m := range c.Members {
		if m.Role == "leader" || m.Role == "standby_leader" {
			return &c.Members[i]
		}
	}
	return nil
}

// Member finds a member by name.
func (c Cluster) Member(name string) *ClusterMember {
	for i, m := range c.Members {
		if m.Name == name {
			return &c.Members[i]
		}
	}
	return nil
}

func base(host string, port int) string { return fmt.Sprintf("http://%s:%d", host, port) }

// Cluster reads /cluster from the member at host:port.
func (p Patroni) Cluster(ctx context.Context, host string, port int) (Cluster, error) {
	var c Cluster
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base(host, port)+"/cluster", nil)
	if err != nil {
		return c, err
	}
	res, err := p.HTTP.Do(req)
	if err != nil {
		return c, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return c, fmt.Errorf("patroni %s:%d /cluster: HTTP %d", host, port, res.StatusCode)
	}
	return c, json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&c)
}

func (p Patroni) send(ctx context.Context, method, host string, port int, path, password string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, base(host, port)+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(agentapi.PatroniREST, password)
	// A switchover waits for the new leader; give it longer than reads.
	client := *p.HTTP
	client.Timeout = 60 * time.Second
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("patroni %s: HTTP %d: %s", path, res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Switchover hands the leader role from leader to candidate (a planned
// switchover: the old leader stops cleanly first, so nothing is lost).
func (p Patroni) Switchover(ctx context.Context, host string, port int, password, leader, candidate string) error {
	return p.send(ctx, http.MethodPost, host, port, "/switchover", password, map[string]string{"leader": leader, "candidate": candidate})
}

// PatchConfig changes the cluster's dynamic configuration in etcd (e.g.
// synchronous_mode).
func (p Patroni) PatchConfig(ctx context.Context, host string, port int, password string, patch map[string]any) error {
	return p.send(ctx, http.MethodPatch, host, port, "/config", password, patch)
}
