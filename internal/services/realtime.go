package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// Realtime (V4 §6): pgdock-edge serves the WebSockets and reads each
// project's outbox; pgdock-server keeps the limits it applies and clears
// what it leaves behind.

const (
	// DefaultRealtimeChangesPerSecond is the database changes a project's
	// subscribers get each second before they are told to resync (§6.3).
	DefaultRealtimeChangesPerSecond = 200
	// DefaultRealtimeGroups is the distinct claims groups checked per
	// change; subscribers past it are told to resync.
	DefaultRealtimeGroups = 100

	// outboxKeep is how long captured changes stay: the edge reads them
	// within moments, and a client that missed them refetches (§6.4).
	outboxKeep = 5 * time.Minute
	// relayKeep is how long a large message between edges stays.
	relayKeep = 2 * time.Minute
	// historyKeep is how long persisted broadcasts stay (§6.4).
	historyKeep = 7 * 24 * time.Hour
)

// RealtimeSweep clears captured changes, relayed messages and old history
// from each project, and works out the limits the edge applies. It runs
// with the storage sweep.
func (s *Service) RealtimeSweep(ctx context.Context) error {
	q := store.New(s.db)
	ps, err := q.RealtimeProjects(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range ps {
		if err := s.sweepRealtime(ctx, r.Project); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Ref, err))
		}
	}
	if err := s.realtimeLimits(ctx, ps); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Service) sweepRealtime(ctx context.Context, p store.Project) error {
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	for _, st := range []struct {
		sql  string
		keep time.Duration
	}{
		{`DELETE FROM pgd_realtime.outbox WHERE at < now() - $1::interval`, outboxKeep},
		{`DELETE FROM pgd_realtime.relay WHERE at < now() - $1::interval`, relayKeep},
		{`DELETE FROM pgd_realtime.broadcast_history WHERE at < now() - $1::interval`, historyKeep},
	} {
		if _, err := conn.Exec(ctx, st.sql, st.keep.String()); err != nil {
			return err
		}
	}
	return nil
}

// realtimeLimits sets each project's connection limit and whether its
// organisation's monthly messages are used up.
func (s *Service) realtimeLimits(ctx context.Context, ps []store.RealtimeProjectsRow) error {
	q := store.New(s.db)
	byOrg := map[uuid.UUID][]store.RealtimeProjectsRow{}
	for _, r := range ps {
		byOrg[r.Project.OrgID] = append(byOrg[r.Project.OrgID], r)
	}
	month := time.Now().UTC()
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	var errs []error
	for org, rows := range byOrg {
		o, err := q.OrgWithPlan(ctx, org)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		l, err := store.EffectiveLimits(o.PlanLimits, o.LimitOverrides)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		blocked := false
		if capN, ok := l.Get(store.LimitRealtimeMessagesMo); ok {
			used, err := q.OrgUsageSince(ctx, store.OrgUsageSinceParams{OrgID: org, Metric: tenancy.MetricRealtimeMessages, Since: month})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			blocked = numericFloat(used) >= float64(capN)
		}
		var maxConns *int32
		if n, ok := l.Get(store.LimitRealtimeConnections); ok {
			v := int32(min(n, 1<<30))
			maxConns = &v
		}
		for _, r := range rows {
			if _, err := q.SetRealtimeLimits(ctx, store.SetRealtimeLimitsParams{ProjectID: r.Project.ID, MaxConnections: maxConns,
				MessagesBlocked: blocked}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// RealtimeTable is a table the dashboard can turn realtime on for.
type RealtimeTable struct {
	Schema, Table string
	Enabled       bool
	RLS           bool
	HasPK         bool
}

// RealtimeOverview is a project's realtime page.
type RealtimeOverview struct {
	Tables           []RealtimeTable
	PersistedTopics  []string
	MaxConnections   *int32
	MessagesBlocked  bool
	MessagesMonth    float64
	ConnMinutesMonth float64
	ChangesPerSecond int
	GroupsPerChange  int
}

// realtimeConn opens the project's database as the platform for realtime,
// once its schema is at version 5.
func (s *Service) realtimeConn(ctx context.Context, projectID uuid.UUID) (store.Project, store.ProjectService, *pgx.Conn, error) {
	q := store.New(s.db)
	p, err := q.GetProject(ctx, projectID)
	if err != nil {
		return p, store.ProjectService{}, nil, err
	}
	svc, err := q.GetProjectServices(ctx, projectID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !svc.Enabled) {
		return p, svc, nil, fmt.Errorf("%w: backend services aren't enabled for this project", ErrConflict)
	}
	if err != nil {
		return p, svc, nil, err
	}
	if svc.SchemaVersion < 5 {
		return p, svc, nil, fmt.Errorf("%w: the project's realtime schema isn't ready yet; try again in a minute", ErrConflict)
	}
	conn, err := s.projects.AdminConn(ctx, p.InstanceID, p.DbName)
	return p, svc, conn, err
}

// Realtime is a project's realtime page: the tables of its exposed schemas
// and which have realtime on, the topics kept as history, limits and use.
func (s *Service) Realtime(ctx context.Context, projectID uuid.UUID) (RealtimeOverview, error) {
	_, svc, conn, err := s.realtimeConn(ctx, projectID)
	if err != nil {
		return RealtimeOverview{}, err
	}
	defer conn.Close(context.Background())
	exposed := svc.ExposedSchemas
	if len(exposed) == 0 {
		exposed = []string{"public"}
	}
	out := RealtimeOverview{MaxConnections: svc.RealtimeMaxConnections, MessagesBlocked: svc.RealtimeMessagesBlocked,
		ChangesPerSecond: DefaultRealtimeChangesPerSecond, GroupsPerChange: DefaultRealtimeGroups, Tables: []RealtimeTable{}, PersistedTopics: []string{}}
	rows, err := conn.Query(ctx, `SELECT n.nspname, c.relname, t.table_name IS NOT NULL, c.relrowsecurity,
		  EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pgd_realtime.tables t ON t.schema_name = n.nspname AND t.table_name = c.relname
		WHERE n.nspname = ANY($1) AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		ORDER BY n.nspname, c.relname`, exposed)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var t RealtimeTable
		if err := rows.Scan(&t.Schema, &t.Table, &t.Enabled, &t.RLS, &t.HasPK); err != nil {
			rows.Close()
			return out, err
		}
		out.Tables = append(out.Tables, t)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	topics, err := conn.Query(ctx, `SELECT topic FROM pgd_realtime.persisted_topics ORDER BY topic`)
	if err != nil {
		return out, err
	}
	for topics.Next() {
		var t string
		if err := topics.Scan(&t); err != nil {
			topics.Close()
			return out, err
		}
		out.PersistedTopics = append(out.PersistedTopics, t)
	}
	if err := topics.Err(); err != nil {
		return out, err
	}
	month := time.Now().UTC()
	month = time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	q := store.New(s.db)
	for _, m := range []struct {
		metric string
		to     *float64
	}{{tenancy.MetricRealtimeMessages, &out.MessagesMonth}, {tenancy.MetricRealtimeConnMinutes, &out.ConnMinutesMonth}} {
		v, err := q.ProjectUsageSince(ctx, store.ProjectUsageSinceParams{ProjectID: projectID, Metric: m.metric, Since: month})
		if err != nil {
			return out, err
		}
		*m.to = numericFloat(v)
	}
	return out, nil
}

// SetRealtimeTable turns realtime on or off for a table, as the platform.
func (s *Service) SetRealtimeTable(ctx context.Context, projectID uuid.UUID, schema, table string, enabled bool) error {
	_, _, conn, err := s.realtimeConn(ctx, projectID)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	fn := "pgd_realtime.disable"
	if enabled {
		fn = "pgd_realtime.enable"
	}
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, pgx.Identifier{schema, table}.Sanitize()).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: no table %s.%s", ErrNotFound, schema, table)
	}
	if _, err := conn.Exec(ctx, `SELECT `+fn+`($1::regclass)`, pgx.Identifier{schema, table}.Sanitize()); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "42P10" || pe.Code == "42501" || pe.Code == "42809") {
			return fmt.Errorf("%w: %s", ErrInvalid, pe.Message)
		}
		return err
	}
	return nil
}

// SetPersistedTopics replaces the topics whose broadcasts are kept 7 days.
func (s *Service) SetPersistedTopics(ctx context.Context, projectID uuid.UUID, topics []string) error {
	if len(topics) > 100 {
		return fmt.Errorf("%w: at most 100 topics", ErrInvalid)
	}
	for _, t := range topics {
		if t == "" || len(t) > 255 {
			return fmt.Errorf("%w: a topic is 1 to 255 characters", ErrInvalid)
		}
	}
	_, _, conn, err := s.realtimeConn(ctx, projectID)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM pgd_realtime.persisted_topics WHERE topic <> ALL($1)`, topics); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO pgd_realtime.persisted_topics (topic) SELECT unnest($1::text[]) ON CONFLICT DO NOTHING`, topics)
		return err
	})
}
