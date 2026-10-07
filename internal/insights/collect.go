package insights

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// params matches a bind parameter ($1).
var params = regexp.MustCompile(`\$[0-9]+`)

// bucketSize is a query_stats bucket.
const bucketSize = 5 * time.Minute

// maxQueryText bounds the query texts kept (pg_stat_statements keeps more).
const maxQueryText = 10_000

// snapKey identifies a pg_stat_statements entry.
type snapKey struct {
	dbid, userid, queryid int64
	toplevel              bool
}

// The baseline row: an instance with one has been snapshotted before, so
// entries new since then are new work, not history from before PGDock
// looked.
var baselineKey = snapKey{dbid: 0, userid: 0, queryid: 0, toplevel: true}

type statRow struct {
	key            snapKey
	calls, rows    int64
	totalMS, maxMS float64
	hits, reads    int64
	query          string
}

// Collect snapshots pg_stat_statements on every instance with an eligible
// project and records each project's deltas since the last snapshot.
func (s *Service) Collect(ctx context.Context) error {
	rows, err := store.New(s.db).InsightsProjects(ctx)
	if err != nil {
		return err
	}
	byInstance := map[uuid.UUID][]store.InsightsProjectsRow{}
	var order []uuid.UUID
	for _, r := range rows {
		if !s.eligible(r.Tier, r.Plan) {
			continue
		}
		if byInstance[r.InstanceID] == nil {
			order = append(order, r.InstanceID)
		}
		byInstance[r.InstanceID] = append(byInstance[r.InstanceID], r)
	}
	var errs []error
	for _, inst := range order {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.collectInstance(ctx, inst, byInstance[inst]); err != nil {
			errs = append(errs, fmt.Errorf("instance %s: %w", inst, err))
		}
	}
	return errors.Join(errs...)
}

// CollectProject collects p's instance now (tests, and the UI's refresh).
func (s *Service) CollectProject(ctx context.Context, p store.Project) error {
	rows, err := store.New(s.db).InsightsProjects(ctx)
	if err != nil {
		return err
	}
	var group []store.InsightsProjectsRow
	for _, r := range rows {
		if r.InstanceID == p.InstanceID && s.eligible(r.Tier, r.Plan) {
			group = append(group, r)
		}
	}
	if len(group) == 0 {
		return ErrNotAvailable
	}
	return s.collectInstance(ctx, p.InstanceID, group)
}

func (s *Service) collectInstance(ctx context.Context, inst uuid.UUID, group []store.InsightsProjectsRow) error {
	conn, err := s.projects.AdminConn(ctx, inst, "postgres")
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	now := s.Now()

	// The view is read from the postgres database, where the control plane
	// installs the extension; it shows every database's statements.
	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"); err != nil {
		return fmt.Errorf("pg_stat_statements: %w", err)
	}
	var schema string
	if err := conn.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_stat_statements'`).Scan(&schema); err != nil {
		return err
	}
	names := make([]string, len(group))
	byName := map[string]store.InsightsProjectsRow{}
	for i, g := range group {
		names[i] = g.DbName
		byName[g.DbName] = g
	}
	dbRows, err := conn.Query(ctx, `SELECT oid::bigint, datname FROM pg_database WHERE datname = ANY($1)`, names)
	if err != nil {
		return err
	}
	byDB := map[int64]store.InsightsProjectsRow{}
	var dbids []int64
	if err := forEach(dbRows, func(r pgx.Rows) error {
		var oid int64
		var name string
		if err := r.Scan(&oid, &name); err != nil {
			return err
		}
		byDB[oid] = byName[name]
		dbids = append(dbids, oid)
		return nil
	}); err != nil {
		return err
	}

	// The control plane's own statements (the admin, the console login
	// tracks nothing) are left out.
	stmtRows, err := conn.Query(ctx, `SELECT s.dbid::bigint, s.userid::bigint, s.queryid, s.toplevel, s.calls, s.total_exec_time, s.rows,
		  s.shared_blks_hit, s.shared_blks_read, s.max_exec_time, left(s.query, $2)
		FROM `+provision.Ident(schema)+`.pg_stat_statements s
		WHERE s.dbid::bigint = ANY($1) AND s.queryid IS NOT NULL
		  AND s.userid <> (SELECT oid FROM pg_roles WHERE rolname = current_user)`, dbids, maxQueryText)
	if err != nil {
		return err
	}
	var cur []statRow
	if err := forEach(stmtRows, func(r pgx.Rows) error {
		var x statRow
		if err := r.Scan(&x.key.dbid, &x.key.userid, &x.key.queryid, &x.key.toplevel, &x.calls, &x.totalMS, &x.rows,
			&x.hits, &x.reads, &x.maxMS, &x.query); err != nil {
			return err
		}
		cur = append(cur, x)
		return nil
	}); err != nil {
		return err
	}

	q := store.New(s.db)
	prevRows, err := q.ListQuerySnapshots(ctx, inst)
	if err != nil {
		return err
	}
	prev := map[snapKey]store.QuerySnapshot{}
	for _, p := range prevRows {
		prev[snapKey{p.Dbid, p.Userid, p.Queryid, p.Toplevel}] = p
	}
	_, haveBaseline := prev[baselineKey]

	type pq struct {
		project uuid.UUID
		queryid int64
	}
	deltas := map[pq]*store.AddQueryStatsParams{}
	texts := map[pq]string{}
	slow := map[pq]float64{}
	bucket := now.Truncate(bucketSize)
	for _, c := range cur {
		proj, ok := byDB[c.key.dbid]
		if !ok || !c.key.toplevel {
			continue
		}
		p, seen := prev[c.key]
		var d statRow
		switch {
		case seen && c.calls >= p.Calls && c.totalMS >= p.TotalMs:
			d = statRow{calls: c.calls - p.Calls, totalMS: c.totalMS - p.TotalMs, rows: c.rows - p.Rows,
				hits: c.hits - p.SharedBlksHit, reads: c.reads - p.SharedBlksRead}
		case seen || haveBaseline:
			// Reset since, or new since the last snapshot: all of it is new.
			d = statRow{calls: c.calls, totalMS: c.totalMS, rows: c.rows, hits: c.hits, reads: c.reads}
		default:
			continue // the first look at this instance: history, not this interval's work
		}
		if d.calls <= 0 {
			continue
		}
		k := pq{proj.ID, c.key.queryid}
		a := deltas[k]
		if a == nil {
			a = &store.AddQueryStatsParams{ProjectID: proj.ID, Queryid: c.key.queryid, Bucket: bucket}
			deltas[k] = a
		}
		a.Calls += d.calls
		a.TotalMs += d.totalMS
		a.Rows += d.rows
		a.SharedBlksHit += d.hits
		a.SharedBlksRead += d.reads
		// The interval's slowest call: a new maximum, or else at least the mean.
		mx := d.totalMS / float64(d.calls)
		if !seen || c.maxMS > p.MaxMs {
			mx = max(mx, c.maxMS)
		}
		a.MaxMs = max(a.MaxMs, mx)
		if utf8.ValidString(c.query) {
			texts[k] = c.query
		}
		if mx >= float64(s.cfg.SlowQuery.Milliseconds()) {
			slow[k] = max(slow[k], mx)
		}
	}

	// Examples with their literals, for EXPLAIN, and statements running
	// past the threshold right now.
	actRows, err := conn.Query(ctx, `SELECT datname, usename, query_id, query, state,
		  EXTRACT(EPOCH FROM (now() - query_start)) * 1000, octet_length(query) >= pg_size_bytes(current_setting('track_activity_query_size')) - 1
		FROM pg_stat_activity
		WHERE datname = ANY($1) AND backend_type = 'client backend' AND usename <> current_user
		  AND pid <> pg_backend_pid() AND query_id IS NOT NULL AND query <> ''`, names)
	if err != nil {
		return err
	}
	type activity struct {
		db, role, query, state string
		queryid                int64
		runningMS              float64
		truncated              bool
	}
	var acts []activity
	if err := forEach(actRows, func(r pgx.Rows) error {
		var a activity
		var ms *float64
		if err := r.Scan(&a.db, &a.role, &a.queryid, &a.query, &a.state, &ms, &a.truncated); err != nil {
			return err
		}
		if ms != nil {
			a.runningMS = *ms
		}
		acts = append(acts, a)
		return nil
	}); err != nil {
		return err
	}

	// Write: texts first (the stats name them), then the deltas, then the
	// new snapshot in one transaction.
	var tp []store.UpsertQueryTextsParams
	for k, t := range texts {
		tp = append(tp, store.UpsertQueryTextsParams{ProjectID: k.project, Queryid: k.queryid, Query: t})
	}
	if err := batchErr(q.UpsertQueryTexts(ctx, tp)); err != nil {
		return fmt.Errorf("query texts: %w", err)
	}
	var sp []store.AddQueryStatsParams
	for _, d := range deltas {
		sp = append(sp, *d)
	}
	if err := batchErrStats(q.AddQueryStats(ctx, sp)); err != nil {
		return fmt.Errorf("query stats: %w", err)
	}
	for k, ms := range slow {
		qid := k.queryid
		if err := q.InsertSlowQuery(ctx, store.InsertSlowQueryParams{ProjectID: k.project, Queryid: &qid, Query: texts[k], DurationMs: ms, Source: "snapshot"}); err != nil {
			return err
		}
	}
	for _, a := range acts {
		proj := byName[a.db]
		// An extended-protocol statement shows $1… instead of its literals:
		// no better than the normalised text.
		if !a.truncated && utf8.ValidString(a.query) && !params.MatchString(a.query) {
			if err := q.SetQueryExample(ctx, store.SetQueryExampleParams{ProjectID: proj.ID, Queryid: a.queryid, Example: &a.query}); err != nil {
				return err
			}
		}
		if a.state == "active" && a.runningMS >= float64(s.cfg.SlowQuery.Milliseconds()) && utf8.ValidString(a.query) {
			dup, err := q.RecentSlowQuery(ctx, store.RecentSlowQueryParams{ProjectID: proj.ID, Query: a.query})
			if err != nil {
				return err
			}
			if !dup {
				qid := a.queryid
				if err := q.InsertSlowQuery(ctx, store.InsertSlowQueryParams{ProjectID: proj.ID, Queryid: &qid, Query: a.query,
					DurationMs: a.runningMS, Source: "running", RoleName: a.role}); err != nil {
					return err
				}
			}
		}
	}

	snap := make([]store.InsertQuerySnapshotsParams, 0, len(cur)+1)
	snap = append(snap, store.InsertQuerySnapshotsParams{InstanceID: inst, Toplevel: true, TakenAt: now})
	for _, c := range cur {
		snap = append(snap, store.InsertQuerySnapshotsParams{InstanceID: inst, Dbid: c.key.dbid, Userid: c.key.userid, Queryid: c.key.queryid,
			Toplevel: c.key.toplevel, Calls: c.calls, TotalMs: c.totalMS, Rows: c.rows, SharedBlksHit: c.hits, SharedBlksRead: c.reads,
			MaxMs: c.maxMS, TakenAt: now})
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tq := store.New(tx)
		if err := tq.DeleteQuerySnapshots(ctx, inst); err != nil {
			return err
		}
		_, err := tq.InsertQuerySnapshots(ctx, snap)
		return err
	})
}

func forEach(rows pgx.Rows, f func(pgx.Rows) error) error {
	defer rows.Close()
	for rows.Next() {
		if err := f(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func batchErr(b *store.UpsertQueryTextsBatchResults) error {
	var first error
	b.Exec(func(_ int, err error) {
		if err != nil && first == nil {
			first = err
		}
	})
	return first
}

func batchErrStats(b *store.AddQueryStatsBatchResults) error {
	var first error
	b.Exec(func(_ int, err error) {
		if err != nil && first == nil {
			first = err
		}
	})
	return first
}
