package pooler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/agentapi"
	"github.com/israel-duff/pgdock/internal/floatip"
	"github.com/israel-duff/pgdock/internal/store"
)

// HostView is what the arbiter knows about one pooler host in one check.
type HostView struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	ServerID   string    `json:"server_id"`
	Reachable  bool      `json:"reachable"`
	Ready      bool      `json:"ready"`
	Stale      bool      `json:"stale"`
	VRRP       string    `json:"vrrp_state"`
	Generation int64     `json:"generation"`
	Reason     string    `json:"reason,omitempty"`
}

func (h HostView) healthy() bool { return h.Reachable && h.Ready }

// Decision is the arbiter's verdict on one check.
type Decision struct {
	// AssignTo is the host the floating IP should move to, if it must move.
	AssignTo *HostView
	// Holder is the host the floating IP routes to now (nil if none or an
	// unknown server).
	Holder *HostView
	// SplitBrain: more than one host says keepalived made it MASTER.
	SplitBrain bool
	// NoHealthy: no pooler host can serve.
	NoHealthy bool
}

// Decide is the arbiter's rule (V3 §2.1). The floating IP stays where it
// is while that host is healthy, even if keepalived disagrees: keepalived's
// own claim moves it within seconds, and fighting it would flap. When the
// holder is unhealthy, unknown, or the IP is unassigned, it goes to a
// healthy host, preferring the one keepalived made MASTER.
func Decide(hosts []HostView, holder string, manageIP bool) Decision {
	var d Decision
	masters, healthy := 0, 0
	for i := range hosts {
		h := &hosts[i]
		if h.Reachable && h.VRRP == "MASTER" {
			masters++
		}
		if h.healthy() {
			healthy++
		}
		if holder != "" && h.ServerID == holder {
			d.Holder = h
		}
	}
	d.SplitBrain = masters > 1
	d.NoHealthy = len(hosts) > 0 && healthy == 0
	if !manageIP || (d.Holder != nil && d.Holder.healthy()) {
		return d
	}
	var pick *HostView
	for i := range hosts {
		h := &hosts[i]
		if !h.healthy() || h.ServerID == "" {
			continue
		}
		if pick == nil || (h.VRRP == "MASTER" && pick.VRRP != "MASTER") {
			pick = h
		}
	}
	d.AssignTo = pick
	return d
}

// Arbiter checks the pooler hosts and the floating IP every few seconds,
// keeps stale hosts caught up, and moves the IP when keepalived can't.
type Arbiter struct {
	m        *Manager
	fip      floatip.Provider
	manageIP bool
	log      *slog.Logger
	// Grace is how many consecutive checks the holder must be unhealthy
	// before the arbiter moves the IP itself (default 2), so keepalived's
	// own failover goes first.
	Grace int
	// RepushEvery limits re-pushes to stale hosts (default 15s).
	RepushEvery time.Duration

	mu         sync.Mutex
	snapshot   ArbiterSnapshot
	badChecks  int
	lastHolder string
	split      bool
	wasReady   map[uuid.UUID]bool
	lastRepush time.Time
}

// ArbiterSnapshot is the arbiter's latest view, for the admin console.
type ArbiterSnapshot struct {
	CheckedAt  time.Time  `json:"checked_at"`
	Generation int64      `json:"generation"`
	Hosts      []HostView `json:"hosts"`
	// HolderServerID is the server the floating IP routes to ("" when
	// there is no floating IP or it is unassigned).
	HolderServerID string `json:"holder_server_id"`
	HolderName     string `json:"holder_name,omitempty"`
	HolderError    string `json:"holder_error,omitempty"`
	ManagesIP      bool   `json:"manages_ip"`
	SplitBrain     bool   `json:"split_brain"`
	NoHealthy      bool   `json:"no_healthy"`
}

// NewArbiter returns an arbiter for m's pooler hosts. fip nil means no
// floating IP is managed (keepalived alone moves the address).
func NewArbiter(m *Manager, fip floatip.Provider, log *slog.Logger) *Arbiter {
	_, none := fip.(floatip.None)
	manage := fip != nil && !none
	if fip == nil {
		fip = floatip.None{}
	}
	return &Arbiter{m: m, fip: fip, manageIP: manage, log: log, Grace: 2, RepushEvery: 15 * time.Second, wasReady: map[uuid.UUID]bool{}}
}

// Snapshot returns the latest check.
func (a *Arbiter) Snapshot() ArbiterSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.snapshot
	s.Hosts = append([]HostView(nil), s.Hosts...)
	return s
}

// Run checks every interval until ctx ends.
func (a *Arbiter) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := a.Tick(ctx); err != nil && ctx.Err() == nil {
			a.log.Warn("pooler arbiter check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one check.
func (a *Arbiter) Tick(ctx context.Context) error {
	hs := a.m.hostSet()
	if hs == nil {
		return nil
	}
	entries, err := hs.refresh(ctx)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		a.mu.Lock()
		a.snapshot = ArbiterSnapshot{CheckedAt: time.Now(), ManagesIP: a.manageIP}
		a.mu.Unlock()
		return nil
	}
	q := store.New(a.m.db)
	cfg, err := q.GetPoolerConfig(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	views := make([]HostView, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			st, err := hs.driver.Expect(cctx, e.node, agentapi.PoolerExpected{Generation: cfg.Generation, Hash: cfg.Hash})
			cancel()
			v := HostView{ID: e.node.ID, Name: e.node.Name}
			if e.node.ProviderServerID != nil {
				v.ServerID = *e.node.ProviderServerID
			}
			if err != nil {
				v.Reason = err.Error()
			} else {
				v.Reachable, v.Ready, v.Stale, v.VRRP, v.Generation, v.Reason = true, st.Ready, st.Stale, st.VRRPState, st.Generation, st.Reason
				if st.ServerID != "" && st.ServerID != v.ServerID {
					v.ServerID = st.ServerID
					sid := st.ServerID
					if err := q.SetProviderServerID(ctx, store.SetProviderServerIDParams{ID: e.node.ID, ProviderServerID: &sid}); err != nil {
						a.log.Warn("could not record pooler host server ID", "host", e.node.Name, "err", err)
					}
				}
			}
			hs.setReachable(e.node.ID, err == nil, st)
			var vrrp *string
			if v.VRRP != "" {
				vrrp = &v.VRRP
			}
			gen, hash := v.Generation, st.Hash
			if err := q.RecordPoolerHostState(ctx, store.RecordPoolerHostStateParams{
				ID: e.node.ID, PoolerGeneration: &gen, PoolerHash: &hash, PoolerVrrpState: vrrp, PoolerReady: &v.Ready,
			}); err != nil {
				a.log.Warn("could not record pooler host state", "host", e.node.Name, "err", err)
			}
			views[i] = v
		}()
	}
	wg.Wait()

	// A reachable host serving an old generation missed a push: push
	// again (at most every RepushEvery).
	for _, v := range views {
		behind := v.Stale || v.Generation == 0 || v.Generation < cfg.Generation
		if v.Reachable && behind && time.Since(a.lastRepush) >= a.RepushEvery {
			a.lastRepush = time.Now()
			a.log.Info("pooler host is stale; pushing the configuration again", "host", v.Name, "serving", v.Generation, "expected", cfg.Generation)
			if err := a.m.Reload(ctx); err != nil {
				a.log.Warn("re-push to stale pooler host", "err", err)
			}
			break
		}
	}

	holder, herr := "", error(nil)
	if a.manageIP {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		holder, herr = a.fip.Holder(hctx)
		cancel()
	}
	d := Decide(views, holder, a.manageIP && herr == nil)
	a.observe(ctx, views, d, holder)

	if d.AssignTo != nil {
		a.mu.Lock()
		a.badChecks++
		bad := a.badChecks
		a.mu.Unlock()
		if bad >= a.Grace {
			to := *d.AssignTo
			from := "nobody"
			if d.Holder != nil {
				from = d.Holder.Name
			}
			actx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := a.fip.Assign(actx, to.ServerID)
			cancel()
			if err != nil {
				a.log.Error("could not move the floating IP", "to", to.Name, "err", err)
			} else {
				a.log.Warn("moved the floating IP: keepalived did not", "from", from, "to", to.Name)
				a.m.event(ctx, &to.ID, "reassigned", map[string]any{"from": from, "to": to.Name, "by": "arbiter"})
				holder = to.ServerID
				a.mu.Lock()
				a.badChecks = 0
				a.lastHolder = holder
				a.mu.Unlock()
			}
		}
	} else {
		a.mu.Lock()
		a.badChecks = 0
		a.mu.Unlock()
	}

	snap := ArbiterSnapshot{
		CheckedAt: time.Now(), Generation: cfg.Generation, Hosts: views, HolderServerID: holder,
		ManagesIP: a.manageIP, SplitBrain: d.SplitBrain, NoHealthy: d.NoHealthy,
	}
	if herr != nil {
		snap.HolderError = herr.Error()
	}
	for _, v := range views {
		if holder != "" && v.ServerID == holder {
			snap.HolderName = v.Name
		}
	}
	a.mu.Lock()
	a.snapshot = snap
	a.mu.Unlock()
	return nil
}

// observe records transitions as pooler events: the IP changing hands,
// split brain starting, hosts going stale or recovering.
func (a *Arbiter) observe(ctx context.Context, views []HostView, d Decision, holder string) {
	a.mu.Lock()
	prevHolder, prevSplit := a.lastHolder, a.split
	a.lastHolder, a.split = holder, d.SplitBrain
	type change struct {
		v    HostView
		kind string
	}
	var changes []change
	for _, v := range views {
		was, seen := a.wasReady[v.ID]
		a.wasReady[v.ID] = v.healthy()
		switch {
		case seen && was && !v.healthy() && v.Stale:
			changes = append(changes, change{v, "stale"})
		case seen && !was && v.healthy():
			changes = append(changes, change{v, "recovered"})
		}
	}
	a.mu.Unlock()

	if holder != "" && holder != prevHolder && d.Holder != nil {
		a.m.event(ctx, &d.Holder.ID, "took_ip", map[string]any{"server_id": holder, "vrrp_state": d.Holder.VRRP})
		a.log.Info("floating IP now routes to pooler host", "host", d.Holder.Name)
	}
	if d.SplitBrain && !prevSplit {
		a.m.event(ctx, nil, "split_brain", map[string]any{"holder": holder})
		a.log.Error("more than one pooler host is keepalived MASTER")
	}
	for _, c := range changes {
		a.m.event(ctx, &c.v.ID, c.kind, map[string]any{"reason": c.v.Reason, "generation": c.v.Generation})
	}
}
