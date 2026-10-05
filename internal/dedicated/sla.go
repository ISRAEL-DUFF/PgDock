package dedicated

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/statusapi"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// SLA measurement (V3 §2.7): every interval, each HA project's pooler
// endpoint is probed from pgdock-server (one vantage point) and, when a
// status service is configured, from it (the other). availability_minutes
// keeps a row per minute; a minute is unavailable when every vantage point
// that probed in it failed.

// ensureProbe creates (or resets) the project's SLA probe login: it may
// connect to the database and run SELECT 1, nothing else. The poolers
// accept it like the project's other logins.
func (s *Service) ensureProbe(ctx context.Context, inst store.Instance, p store.Project, log *jobs.StepLogger) error {
	sec, err := s.openHASecret(inst)
	if err != nil {
		return err
	}
	if sec.ProbePassword == "" {
		sec.ProbePassword = randomPassword()
		sealed, err := s.sealHASecret(inst.ID, sec)
		if err != nil {
			return err
		}
		if err := store.New(s.db).SetInstancePatroni(ctx, store.SetInstancePatroniParams{ID: inst.ID, Patroni: inst.Patroni,
			HaEnabled: inst.HaEnabled, SyncReplication: inst.SyncReplication, LeaderMember: inst.LeaderMember, PatroniSecret: sealed}); err != nil {
			return err
		}
	}
	verifier, err := crypto.SCRAMVerifier(sec.ProbePassword)
	if err != nil {
		return err
	}
	admin, err := s.projects.AdminConn(ctx, inst.ID, "postgres")
	if err != nil {
		return err
	}
	defer admin.Close(context.Background())
	role := store.ProbeRole(p.DbName)
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return err
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	for _, stmt := range []string{
		verb + " ROLE " + provision.Ident(role) + " LOGIN NOINHERIT CONNECTION LIMIT 5 PASSWORD " + quoteLiteral(verifier),
		"GRANT CONNECT ON DATABASE " + provision.Ident(p.DbName) + " TO " + provision.Ident(role),
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("SLA probe login: %w", err)
		}
	}
	if err := store.New(s.db).SetProbeVerifier(ctx, store.SetProbeVerifierParams{ID: p.ID, ProbeVerifier: &verifier}); err != nil {
		return err
	}
	if err := s.projects.SyncPooler(ctx, log, "pooler", "SLA probe login added"); err != nil {
		return err
	}
	return log.Info(ctx, "sla", "availability is measured from now: a probe connects through the pooler and runs SELECT 1 every minute")
}

// slaState is what the prober keeps between ticks.
type slaState struct {
	mu         sync.Mutex
	pushedHash string
	since      time.Time
}

// SLATick probes every HA project once from here and, with status set,
// exchanges targets and results with the status service.
func (s *Service) SLATick(ctx context.Context, status *statusapi.Client) error {
	s.sla.mu.Lock()
	defer s.sla.mu.Unlock()
	q := store.New(s.db)
	ts, err := q.SLAProbeTargets(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	minute := now.Truncate(time.Minute)
	var targets []statusapi.SLATarget
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, t := range ts {
		url, err := s.probeURL(t)
		if err != nil {
			s.log.Warn("SLA probe", "project", t.ID, "err", err)
			continue
		}
		targets = append(targets, statusapi.SLATarget{ID: t.ID.String(), DSN: url})
		excluded := t.Status != provision.StatusActive || t.OrgStatus != tenancy.OrgActive
		wg.Add(1)
		go func(id uuid.UUID, url string) {
			defer wg.Done()
			ok := probe(ctx, url) == nil
			mu.Lock()
			defer mu.Unlock()
			if err := q.RecordAvailability(context.WithoutCancel(ctx), store.RecordAvailabilityParams{
				ProjectID: id, Minute: minute, InternalOk: &ok, Excluded: excluded}); err != nil {
				s.log.Warn("record availability", "project", id, "err", err)
			}
		}(t.ID, url)
	}
	wg.Wait()
	if status == nil {
		return nil
	}
	return s.exchangeSLA(ctx, status, targets, now)
}

// exchangeSLA pushes the targets when they change and records the status
// service's results since the last fetch.
func (s *Service) exchangeSLA(ctx context.Context, status *statusapi.Client, targets []statusapi.SLATarget, now time.Time) error {
	h := sha256.New()
	for _, t := range targets {
		fmt.Fprintf(h, "%s\x00%s\x00", t.ID, t.DSN)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if sum != s.sla.pushedHash {
		if err := status.PutSLATargets(ctx, statusapi.SLATargets{Targets: targets}); err != nil {
			return fmt.Errorf("push SLA targets: %w", err)
		}
		s.sla.pushedHash = sum
	}
	since := s.sla.since
	if since.IsZero() {
		since = now.Add(-15 * time.Minute)
	}
	res, err := status.SLAResults(ctx, since)
	if err != nil {
		return fmt.Errorf("fetch SLA results: %w", err)
	}
	q := store.New(s.db)
	known := map[string]bool{}
	for _, t := range targets {
		known[t.ID] = true
	}
	latest := since
	for _, r := range res.Results {
		id, err := uuid.Parse(r.ID)
		if err != nil || !known[r.ID] {
			continue
		}
		ok := r.OK
		if err := q.RecordAvailability(ctx, store.RecordAvailabilityParams{ProjectID: id, Minute: r.Minute.UTC(), ExternalOk: &ok}); err != nil {
			return err
		}
		if r.Minute.After(latest) {
			latest = r.Minute
		}
	}
	// The latest minute may still change: fetch it again next time.
	s.sla.since = latest
	return nil
}

func (s *Service) probeURL(t store.SLAProbeTargetsRow) (string, error) {
	inst := store.Instance{ID: t.InstanceID, PatroniSecret: t.PatroniSecret}
	sec, err := s.openHASecret(inst)
	if err != nil {
		return "", err
	}
	if sec.ProbePassword == "" {
		return "", fmt.Errorf("no probe login yet")
	}
	c := s.projects.ConnectionFor(projectOf(t))
	c.User, c.Database = store.ProbeRole(t.DbName), t.DbName
	return c.PooledURL(sec.ProbePassword), nil
}

func projectOf(t store.SLAProbeTargetsRow) store.Project {
	return store.Project{ID: t.ID, DbName: t.DbName, AliasDbName: t.AliasDbName, OwnerRole: t.OwnerRole, LegacyUntil: t.LegacyUntil}
}

// probe connects and runs SELECT 1 within five seconds.
func probe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	var one int
	return conn.QueryRow(ctx, "SELECT 1").Scan(&one)
}

// RunSLA probes every interval until ctx ends.
func (s *Service) RunSLA(ctx context.Context, interval time.Duration, status *statusapi.Client) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.SLATick(ctx, status); err != nil && ctx.Err() == nil {
			s.log.Warn("SLA probes", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
