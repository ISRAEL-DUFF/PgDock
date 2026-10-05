package pooler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/store"
)

// HostDriver reaches pooler hosts' agents (V3 §2.1). The nodes service
// implements it.
type HostDriver interface {
	// Hosts lists the registered pooler hosts.
	Hosts(ctx context.Context) ([]store.Node, error)
	// Push sends a configuration bundle to one host.
	Push(ctx context.Context, n store.Node, b agentapi.PoolerBundle) (agentapi.PoolerStatus, error)
	// Expect tells one host the newest generation, returning its status.
	Expect(ctx context.Context, n store.Node, e agentapi.PoolerExpected) (agentapi.PoolerStatus, error)
}

// HostAdminConfig is how pgdock-server reaches a pooler host's PgBouncer
// admin consoles: the host's private address and these ports.
type HostAdminConfig struct {
	User, Password, SSLMode string
	SessionPort, PooledPort int
}

// poolerHostEntry is a pooler host as the manager knows it.
type poolerHostEntry struct {
	node      store.Node
	session   *Admin
	pooled    *Admin
	reachable bool
	status    agentapi.PoolerStatus
}

type hostSet struct {
	driver HostDriver
	admin  HostAdminConfig

	mu      sync.Mutex
	entries map[uuid.UUID]*poolerHostEntry
	order   []uuid.UUID
}

// SetHostDriver makes the manager push its configuration to pooler hosts
// and send PgBouncer commands to their admin consoles, as well as to the
// local poolers it was built with (if any).
func (m *Manager) SetHostDriver(d HostDriver, admin HostAdminConfig) {
	if admin.SessionPort == 0 {
		admin.SessionPort = 5432
	}
	if admin.PooledPort == 0 {
		admin.PooledPort = 6543
	}
	m.hostsMu.Lock()
	m.hosts = &hostSet{driver: d, admin: admin, entries: map[uuid.UUID]*poolerHostEntry{}}
	m.hostsMu.Unlock()
}

func (m *Manager) hostSet() *hostSet {
	m.hostsMu.Lock()
	defer m.hostsMu.Unlock()
	return m.hosts
}

// refresh reloads the host list, keeping what is known about each host.
func (h *hostSet) refresh(ctx context.Context) ([]*poolerHostEntry, error) {
	nodes, err := h.driver.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[uuid.UUID]bool{}
	h.order = h.order[:0]
	var out []*poolerHostEntry
	for _, n := range nodes {
		seen[n.ID] = true
		e, ok := h.entries[n.ID]
		if !ok || e.node.PrivateAddr != n.PrivateAddr {
			s, err := NewAdmin("session@"+n.Name, net.JoinHostPort(n.PrivateAddr, strconv.Itoa(h.admin.SessionPort)), h.admin.User, h.admin.Password, h.admin.SSLMode)
			if err != nil {
				return nil, err
			}
			p, err := NewAdmin("transaction@"+n.Name, net.JoinHostPort(n.PrivateAddr, strconv.Itoa(h.admin.PooledPort)), h.admin.User, h.admin.Password, h.admin.SSLMode)
			if err != nil {
				return nil, err
			}
			// A new host is assumed reachable until a call says otherwise.
			reach := true
			if ok {
				reach = e.reachable
			}
			e = &poolerHostEntry{session: s, pooled: p, reachable: reach}
			h.entries[n.ID] = e
		}
		e.node = n
		h.order = append(h.order, n.ID)
		out = append(out, e)
	}
	for id := range h.entries {
		if !seen[id] {
			delete(h.entries, id)
		}
	}
	return out, nil
}

func (h *hostSet) setReachable(id uuid.UUID, ok bool, st agentapi.PoolerStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, found := h.entries[id]; found {
		e.reachable = ok
		if ok {
			e.status = st
		}
	}
}

// reachableAdmins are the admin consoles of hosts last seen reachable.
func (h *hostSet) reachableAdmins() []*Admin {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*Admin
	for _, id := range h.order {
		if e := h.entries[id]; e != nil && e.reachable {
			out = append(out, e.session, e.pooled)
		}
	}
	return out
}

// readBundle collects the pooler files from the config directory: the
// routes and auth file Sync writes, and the TLS pair tlscert writes.
func readBundle(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, name := range agentapi.PoolerFiles {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		files[name] = b
	}
	return files, nil
}

// HostPushError lists the pooler hosts that did not take a configuration.
// Their copy is stale, so they fail keepalived's check and can't take the
// floating IP; the arbiter pushes again until they catch up.
type HostPushError struct {
	Failed map[string]error // by host name
	// All is true when no host took the configuration.
	All bool
}

func (e *HostPushError) Error() string {
	var errs []error
	for name, err := range e.Failed {
		errs = append(errs, fmt.Errorf("pooler host %s: %w", name, err))
	}
	return errors.Join(errs...).Error()
}

// pushHosts sends the current files to every pooler host, then tells all
// of them the generation. It returns nil when there are no hosts or every
// host took it, a *HostPushError otherwise.
func (m *Manager) pushHosts(ctx context.Context) error {
	hs := m.hostSet()
	if hs == nil {
		return nil
	}
	entries, err := hs.refresh(ctx)
	if err != nil {
		return fmt.Errorf("pooler hosts: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	files, err := readBundle(m.dir)
	if err != nil {
		return fmt.Errorf("pooler bundle: %w", err)
	}
	hash := agentapi.PoolerHash(files)
	cfg, err := store.New(m.db).AdvancePoolerConfig(ctx, hash)
	if err != nil {
		return fmt.Errorf("pooler generation: %w", err)
	}
	b := agentapi.PoolerBundle{Generation: cfg.Generation, Hash: hash, Files: files}

	type result struct {
		e   *poolerHostEntry
		st  agentapi.PoolerStatus
		err error
	}
	results := make(chan result, len(entries))
	for _, e := range entries {
		go func() {
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			st, err := hs.driver.Push(pctx, e.node, b)
			results <- result{e, st, err}
		}()
	}
	failed := map[string]error{}
	for range entries {
		r := <-results
		hs.setReachable(r.e.node.ID, r.err == nil, r.st)
		if r.err != nil {
			failed[r.e.node.Name] = r.err
			m.event(ctx, &r.e.node.ID, "push_failed", map[string]any{"generation": b.Generation, "error": r.err.Error()})
		}
	}
	// Every host learns the generation, including any that just failed,
	// so a host that missed this push knows it is stale.
	for _, e := range entries {
		ectx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, err := hs.driver.Expect(ectx, e.node, agentapi.PoolerExpected{Generation: b.Generation, Hash: hash})
		cancel()
		if err == nil {
			hs.setReachable(e.node.ID, true, st)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return &HostPushError{Failed: failed, All: len(failed) == len(entries)}
}

// event records a pooler event; failures to record are only logged.
func (m *Manager) event(ctx context.Context, node *uuid.UUID, kind string, detail map[string]any) {
	d, _ := json.Marshal(detail)
	var id *uuid.UUID
	if node != nil {
		id = node
	}
	if err := store.New(m.db).InsertPoolerEvent(context.WithoutCancel(ctx), store.InsertPoolerEventParams{NodeID: id, Kind: kind, Detail: d}); err != nil {
		m.log.Warn("could not record pooler event", "kind", kind, "err", err)
	}
}
