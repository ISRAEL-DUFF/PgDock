package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

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
