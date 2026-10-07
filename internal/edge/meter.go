package edge

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/edgeapi"
)

// Bounds on what is held between reports, so a pgdock-server outage can't
// grow the edge's memory without limit.
const (
	maxLogs    = 20000
	maxPending = 30
)

type usageKey struct {
	project uuid.UUID
	hour    time.Time
}

// meter collects usage, request logs and keys in use between reports.
type meter struct {
	mu      sync.Mutex
	usage   map[usageKey]*edgeapi.Usage
	logs    []edgeapi.Log
	dropped int
	keys    map[uuid.UUID]bool
	// pending are reports not yet accepted, retried in order with their
	// batch ids.
	pending []edgeapi.Report
}

func newMeter() *meter {
	return &meter{usage: map[usageKey]*edgeapi.Usage{}, keys: map[uuid.UUID]bool{}}
}

// record counts a request; billed requests passed the key check.
func (m *meter) record(l edgeapi.Log, billed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := usageKey{l.ProjectID, l.At.Truncate(time.Hour)}
	u := m.usage[k]
	if u == nil {
		u = &edgeapi.Usage{ProjectID: l.ProjectID, Hour: k.hour}
		m.usage[k] = u
	}
	if billed {
		u.Requests++
		u.EgressBytes += l.BytesOut
	}
	if len(m.logs) < maxLogs {
		m.logs = append(m.logs, l)
	} else {
		m.dropped++
	}
	if l.KeyID != nil && billed {
		m.keys[*l.KeyID] = true
	}
}

// take moves what was collected into a new pending report.
func (m *meter) take(edge string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.usage) == 0 && len(m.logs) == 0 && len(m.keys) == 0 {
		return
	}
	r := edgeapi.Report{BatchID: uuid.NewString(), Edge: edge, At: time.Now().UTC(), Logs: m.logs}
	for _, u := range m.usage {
		r.Usage = append(r.Usage, *u)
	}
	for k := range m.keys {
		r.KeysUsed = append(r.KeysUsed, k)
	}
	m.usage, m.logs, m.keys = map[usageKey]*edgeapi.Usage{}, nil, map[uuid.UUID]bool{}
	m.pending = append(m.pending, r)
	if len(m.pending) > maxPending {
		// The oldest reports go first: losing them under-bills, which is
		// the safe side.
		m.pending = m.pending[len(m.pending)-maxPending:]
	}
}

func (m *meter) next() (edgeapi.Report, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return edgeapi.Report{}, false
	}
	return m.pending[0], true
}

func (m *meter) done(batch string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) > 0 && m.pending[0].BatchID == batch {
		m.pending = m.pending[1:]
	}
}

func (e *Edge) report(ctx context.Context) {
	t := time.NewTicker(e.cfg.ReportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		e.flush(ctx)
	}
}

// flush sends what was collected (and any earlier reports not accepted).
func (e *Edge) flush(ctx context.Context) {
	e.meter.take(e.cfg.Name)
	for {
		r, ok := e.meter.next()
		if !ok {
			return
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := e.client.Report(rctx, r)
		cancel()
		if err != nil {
			e.cfg.Log.Warn("edge report", "batch", r.BatchID, "err", err)
			return // retried next time with the same batch id
		}
		e.meter.done(r.BatchID)
	}
}

// Flush sends a report now (tests).
func (e *Edge) Flush(ctx context.Context) { e.flush(ctx) }
