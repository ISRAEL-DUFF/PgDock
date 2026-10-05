package statuspage

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/israel-duff/pgdock/internal/statusapi"
)

// The SLA's outside vantage point (V3 §2.7): pgdock-server hands over the
// HA projects' pooler endpoints (each with a login that can only connect
// and run SELECT 1); the status service probes them every interval and
// keeps a result per minute, which pgdock-server fetches. A minute counts
// as available here when every probe in it succeeded.

const slaSchema = `
CREATE TABLE IF NOT EXISTS sla_targets (id TEXT PRIMARY KEY, dsn TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sla_minutes (
  id TEXT NOT NULL, minute INTEGER NOT NULL, ok INTEGER NOT NULL,
  PRIMARY KEY (id, minute)
) WITHOUT ROWID;
`

// slaRetention is how long results are kept for pgdock-server to fetch.
const slaRetention = 7 * 24 * time.Hour

func (st *store) setSLATargets(ctx context.Context, ts []statusapi.SLATarget) error {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sla_targets`); err != nil {
		return err
	}
	for _, t := range ts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sla_targets (id, dsn) VALUES (?, ?)`, t.ID, t.DSN); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (st *store) slaTargets(ctx context.Context) ([]statusapi.SLATarget, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT id, dsn FROM sla_targets ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []statusapi.SLATarget
	for rows.Next() {
		var t statusapi.SLATarget
		if err := rows.Scan(&t.ID, &t.DSN); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (st *store) recordSLA(ctx context.Context, id string, minute time.Time, ok bool) error {
	// A failure anywhere in the minute makes the minute a failure.
	_, err := st.db.ExecContext(ctx, `INSERT INTO sla_minutes (id, minute, ok) VALUES (?, ?, ?)
		ON CONFLICT (id, minute) DO UPDATE SET ok = min(ok, excluded.ok)`, id, minute.Unix(), boolInt(ok))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (st *store) slaResults(ctx context.Context, since time.Time) ([]statusapi.SLAResult, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT id, minute, ok FROM sla_minutes WHERE minute >= ? ORDER BY minute, id LIMIT 200000`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []statusapi.SLAResult{}
	for rows.Next() {
		var r statusapi.SLAResult
		var m int64
		var ok int
		if err := rows.Scan(&r.ID, &m, &ok); err != nil {
			return nil, err
		}
		r.Minute, r.OK = time.Unix(m, 0).UTC(), ok == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (st *store) pruneSLA(ctx context.Context, before time.Time) error {
	_, err := st.db.ExecContext(ctx, `DELETE FROM sla_minutes WHERE minute < ?`, before.Unix())
	return err
}

// SLATick probes every SLA target once and records the result in the
// current minute.
func (s *Service) SLATick(ctx context.Context) error {
	ts, err := s.st.slaTargets(ctx)
	if err != nil {
		return err
	}
	now := s.Now().UTC()
	minute := now.Truncate(time.Minute)
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go func(t statusapi.SLATarget) {
			defer wg.Done()
			err := s.Probe(ctx, Probe{Kind: "postgres", DSN: t.DSN, Query: "SELECT 1", Timeout: duration{5 * time.Second}})
			if rerr := s.st.recordSLA(context.WithoutCancel(ctx), t.ID, minute, err == nil); rerr != nil {
				s.log.Warn("record an SLA probe", "target", t.ID, "err", rerr)
			}
		}(t)
	}
	wg.Wait()
	return s.st.pruneSLA(ctx, now.Add(-slaRetention))
}

func (s *Service) pushSLATargets(w http.ResponseWriter, r *http.Request, body []byte) {
	var t statusapi.SLATargets
	if err := json.Unmarshal(body, &t); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := t.Validate(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err := s.st.setSLATargets(r.Context(), t.Targets); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) slaResultsHandler(w http.ResponseWriter, r *http.Request, body []byte) {
	var req statusapi.SLAResultsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	res, err := s.st.slaResults(r.Context(), req.Since)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, statusapi.SLAResults{Results: res})
}
