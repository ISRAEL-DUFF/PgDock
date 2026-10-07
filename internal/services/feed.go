package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/tenancy"
)

// feedPage bounds a page of the configuration feed.
const feedPage = 500

// Config is the configuration feed for pgdock-edge (V4 §2.1): projects
// changed after since, waiting up to wait for one. With region set, a
// project in another region comes as disabled, so an edge that served it
// before a move drops it.
func (s *Service) Config(ctx context.Context, region string, since int64, wait time.Duration) (edgeapi.Config, error) {
	q := store.New(s.db)
	deadline := time.Now().Add(wait)
	for {
		rows, err := q.EdgeConfigChanges(ctx, store.EdgeConfigChangesParams{Since: since, Lim: feedPage})
		if err != nil {
			return edgeapi.Config{}, err
		}
		if len(rows) > 0 || !time.Now().Before(deadline) {
			return s.page(ctx, region, since, rows)
		}
		select {
		case <-ctx.Done():
			return edgeapi.Config{Next: since}, nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Service) page(ctx context.Context, region string, since int64, rows []store.EdgeConfigChangesRow) (edgeapi.Config, error) {
	out := edgeapi.Config{Projects: []edgeapi.Project{}, Next: since, More: len(rows) == feedPage}
	var live []uuid.UUID
	for _, r := range rows {
		if state(r) == edgeapi.StateActive || state(r) == edgeapi.StatePaused || state(r) == edgeapi.StateSuspended {
			live = append(live, r.ProjectID)
		}
	}
	q := store.New(s.db)
	keys := map[uuid.UUID][]edgeapi.Key{}
	jwks := map[uuid.UUID][]json.RawMessage{}
	if len(live) > 0 {
		ks, err := q.EdgeAPIKeys(ctx, live)
		if err != nil {
			return out, err
		}
		for _, k := range ks {
			keys[k.ProjectID] = append(keys[k.ProjectID], edgeapi.Key{ID: k.ID, Kind: k.Kind, Hash: k.KeyHash})
		}
		js, err := q.EdgeJWTKeys(ctx, live)
		if err != nil {
			return out, err
		}
		for _, j := range js {
			jwks[j.ProjectID] = append(jwks[j.ProjectID], j.PublicJwk)
		}
	}
	for _, r := range rows {
		out.Next = r.ChangedSeq
		p := edgeapi.Project{Ref: r.Ref, ProjectID: r.ProjectID, OrgID: r.OrgID, Region: r.Region, State: state(r),
			Version: r.ConfigVersion, Seq: r.ChangedSeq}
		if region != "" && r.Region != region {
			p.State = edgeapi.StateDisabled
		}
		if p.State == edgeapi.StateDisabled {
			out.Projects = append(out.Projects, p)
			continue
		}
		st, err := DecodeSettings(r.Settings)
		if err != nil {
			return out, fmt.Errorf("%s settings: %w", r.Ref, err)
		}
		host, port := "", 0
		if addr := s.projects.PooledAddr(r.Region); addr != "" {
			h, ps, err := net.SplitHostPort(addr)
			if err == nil {
				host = h
				port, _ = strconv.Atoi(ps)
			}
		}
		edge := store.EdgeRole(r.DbName)
		p.Database, p.EdgeUser, p.Password = r.DbName, edge, s.edgePassword(edge)
		p.PoolerHost, p.PoolerPort = host, port
		p.AnonRole, p.UserRole, p.ServiceRole = store.AnonRole(r.DbName), store.UserRole(r.DbName), store.ServiceRole(r.DbName)
		p.Keys, p.JWKs, p.CORSOrigins = keys[r.ProjectID], jwks[r.ProjectID], r.CorsOrigins
		p.Settings = edgeapi.Settings{StatementTimeoutMs: or(st.StatementTimeoutMs, DefaultStatementTimeoutMs),
			RatePerIP: or(st.RatePerIP, DefaultRatePerIP), RatePerKey: or(st.RatePerKey, DefaultRatePerKey),
			AllowSecretInBrowser: st.AllowSecretInBrowser}
		out.Projects = append(out.Projects, p)
	}
	return out, nil
}

func or(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

// state is how the edge treats a project.
func state(r store.EdgeConfigChangesRow) string {
	switch {
	case !r.Enabled || !r.EdgeReady || r.DeletedAt != nil:
		return edgeapi.StateDisabled
	case r.Status == "deleting" || r.Status == "deleted" || r.Status == "failed" || r.Status == "provisioning":
		return edgeapi.StateDisabled
	case r.OrgStatus != "active":
		return edgeapi.StateSuspended
	case r.Lifecycle != "active":
		return edgeapi.StatePaused
	}
	return edgeapi.StateActive
}

// Report records an edge's usage, request logs and keys in use, once per
// batch.
func (s *Service) Report(ctx context.Context, r edgeapi.Report) error {
	if r.BatchID == "" || len(r.BatchID) > 100 {
		return fmt.Errorf("%w: a report needs a batch id", ErrInvalid)
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.InsertEdgeReport(ctx, store.InsertEdgeReportParams{BatchID: r.BatchID, Edge: r.Edge})
		if err != nil || n == 0 {
			return err // n == 0: already recorded
		}
		ids := map[uuid.UUID]bool{}
		for _, u := range r.Usage {
			ids[u.ProjectID] = true
		}
		owners := map[uuid.UUID]store.ProjectUsageOwnerRow{}
		if len(ids) > 0 {
			list := make([]uuid.UUID, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			rows, err := q.ProjectUsageOwner(ctx, list)
			if err != nil {
				return err
			}
			for _, o := range rows {
				owners[o.ID] = o
			}
		}
		for _, u := range r.Usage {
			o, ok := owners[u.ProjectID]
			if !ok {
				continue // deleted since
			}
			hour := u.Hour.UTC().Truncate(time.Hour)
			if u.Requests > 0 {
				if err := q.AddUsage(ctx, store.AddUsageParams{OrgID: o.OrgID, ProjectID: o.ID, Metric: tenancy.MetricAPIRequests,
					PeriodStart: hour, Quantity: intNumeric(u.Requests), PlanID: o.PlanID}); err != nil {
					return err
				}
			}
			if u.EgressBytes > 0 {
				if err := q.AddUsage(ctx, store.AddUsageParams{OrgID: o.OrgID, ProjectID: o.ID, Metric: tenancy.MetricAPIEgress,
					PeriodStart: hour, Quantity: gbNumeric(u.EgressBytes), PlanID: o.PlanID}); err != nil {
					return err
				}
			}
		}
		if len(r.Logs) > 0 {
			logs := make([]store.InsertRequestLogsParams, 0, len(r.Logs))
			for _, l := range r.Logs {
				if !ids[l.ProjectID] {
					// Logs come with usage for the same project; anything else
					// is not ours to record.
					continue
				}
				logs = append(logs, store.InsertRequestLogsParams{ProjectID: l.ProjectID, At: l.At, RequestID: trunc(l.RequestID, 64),
					Method: trunc(l.Method, 10), Path: trunc(l.Path, 500), Status: int32(l.Status), LatencyMs: int32(l.LatencyMs),
					Role: strPtr(l.Role), UserID: l.UserID, KeyID: l.KeyID, Ip: strPtr(trunc(l.IP, 64)), BytesOut: l.BytesOut})
			}
			if _, err := q.InsertRequestLogs(ctx, logs); err != nil {
				return err
			}
		}
		if len(r.KeysUsed) > 0 {
			at := r.At
			if at.IsZero() || at.After(time.Now()) {
				at = time.Now()
			}
			if err := q.TouchAPIKeys(ctx, store.TouchAPIKeysParams{At: at, Ids: r.KeysUsed}); err != nil {
				return err
			}
		}
		return nil
	})
}

// VerifyEdge checks a request's edge signature.
func (s *Service) VerifyEdge(r *http.Request, body []byte) error {
	return edgeapi.Verify(s.cfg.EdgeSecret, r, body, time.Now())
}

// Wake resumes the paused project ref names.
func (s *Service) Wake(ctx context.Context, ref string) error {
	svc, err := store.New(s.db).ProjectServicesByRef(ctx, ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if s.Waker == nil {
		return nil
	}
	return s.Waker(ctx, svc.ProjectID)
}

func intNumeric(n int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(n), Valid: true}
}

// gbNumeric is bytes as GB (10^9) with nine decimal places.
func gbNumeric(bytes int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(bytes), Exp: -9, Valid: true}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
