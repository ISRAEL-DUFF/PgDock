package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/israel-duff/pgdock/internal/store"
)

// Usage metrics (V2 §10.9). GB is 10^9 bytes.
const (
	MetricSharedStorage = "shared_storage_gb_hours"
	MetricDedicatedCPU  = "dedicated_vcpu_hours"
	MetricDedicatedRAM  = "dedicated_ram_gb_hours"
	MetricDedicatedDisk = "dedicated_disk_gb_hours"
	MetricBackupStorage = "backup_storage_gb_hours" // recorded daily
	MetricPoolerTraffic = "pooler_transfer_gb"
	MetricBranchHours   = "branch_hours"
	MetricBranchStorage = "branch_gb_hours"
	MetricWebhookTries  = "webhook_delivery_attempts"
	MetricWebhookSent   = "webhook_deliveries"
	MetricJobRunsSQL    = "job_runs_sql"
	MetricJobRunsHTTP   = "job_runs_http"
	// HA standbys' resources and synchronous replication (V3 §2.2, §3.1).
	MetricHACPU           = "ha_vcpu_hours"
	MetricHARAM           = "ha_ram_gb_hours"
	MetricHADisk          = "ha_disk_gb_hours"
	MetricSyncReplication = "sync_replication_hours"
	// Backend services (V4 §11.1), reported by pgdock-edge.
	MetricAPIRequests = "api_requests"
	MetricAPIEgress   = "api_egress_gb"
	// MetricAuthMAU counts each user once a month, when first active.
	MetricAuthMAU = "auth_mau"
	// Auth codes sent over the platform's SMS and WhatsApp (V4 §6.2).
	MetricMessagesSMS      = "messages_sms"
	MetricMessagesWhatsApp = "messages_whatsapp"

	// Backend services' storage (V4 §5, §11.1).
	MetricStorageGBHours  = "storage_gb_hours"
	MetricStorageEgress   = "storage_egress_gb"
	MetricImageTransforms = "image_transforms"
	// Backend services' realtime (V4 §6, §11.1).
	MetricRealtimeConnMinutes = "realtime_connection_minutes"
	MetricRealtimeMessages    = "realtime_messages"
	// Read replicas (V4 §7, §11.1): each bills like a dedicated instance
	// of its size.
	MetricReplicaHours = "replica_hours"
	MetricReplicaCPU   = "replica_vcpu_hours"
	MetricReplicaRAM   = "replica_ram_gb_hours"
	MetricReplicaDisk  = "replica_disk_gb_hours"
)

// UsageMetrics lists them with their units, for the API and UI.
var UsageMetrics = []struct{ Name, Unit, Granularity string }{
	{MetricSharedStorage, "GB-hours", "hour"},
	{MetricDedicatedCPU, "vCPU-hours", "hour"},
	{MetricDedicatedRAM, "GB-RAM-hours", "hour"},
	{MetricDedicatedDisk, "GB-disk-hours", "hour"},
	{MetricBackupStorage, "GB-hours", "day"},
	{MetricPoolerTraffic, "GB", "hour"},
	{MetricBranchHours, "branch-hours", "hour"},
	{MetricBranchStorage, "GB-hours", "hour"},
	{MetricWebhookTries, "attempts", "hour"},
	{MetricWebhookSent, "deliveries", "hour"},
	{MetricJobRunsSQL, "runs", "hour"},
	{MetricJobRunsHTTP, "runs", "hour"},
	{MetricHACPU, "vCPU-hours", "hour"},
	{MetricHARAM, "GB-RAM-hours", "hour"},
	{MetricHADisk, "GB-disk-hours", "hour"},
	{MetricSyncReplication, "hours", "hour"},
	{MetricAPIRequests, "requests", "hour"},
	{MetricAPIEgress, "GB", "hour"},
	{MetricAuthMAU, "users", "hour"},
	{MetricMessagesSMS, "messages", "hour"},
	{MetricMessagesWhatsApp, "messages", "hour"},
	{MetricStorageGBHours, "GB-hours", "hour"},
	{MetricStorageEgress, "GB", "hour"},
	{MetricImageTransforms, "transforms", "hour"},
	{MetricRealtimeConnMinutes, "connection-minutes", "hour"},
	{MetricRealtimeMessages, "messages", "hour"},
	{MetricReplicaHours, "replica-hours", "hour"},
	{MetricReplicaCPU, "vCPU-hours", "hour"},
	{MetricReplicaRAM, "GB-RAM-hours", "hour"},
	{MetricReplicaDisk, "GB-disk-hours", "hour"},
}

const (
	gb = 1e9
	// usageLockKey keeps usage recording to one server at a time.
	usageLockKey int64 = 0x7067646f636b09
	// usageBackfill is how far back a first run records (a week, plus a
	// day of slack).
	usageBackfill = 8 * 24 * time.Hour
	// rollupAfter turns hourly rows into daily ones (V2 §10.9).
	rollupAfter = 90 * 24 * time.Hour

	usageWatermarkKey = "usage.watermark"
)

type usageWatermark struct {
	Hour time.Time `json:"hour"` // next hour to record
	Day  time.Time `json:"day"`  // next day to record
}

// RecordUsage records every completed hour (and day) since the last run.
// Records are upserted, so a repeated run changes nothing.
func (s *Service) RecordUsage(ctx context.Context) error {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", usageLockKey).Scan(&got); err != nil || !got {
		return err // another server is recording
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", usageLockKey) }()

	now := s.Now().UTC()
	q := store.New(s.db)
	wm := usageWatermark{Hour: now.Add(-usageBackfill).Truncate(time.Hour)}
	wm.Day = time.Date(wm.Hour.Year(), wm.Hour.Month(), wm.Hour.Day(), 0, 0, 0, 0, time.UTC)
	if raw, err := q.GetSetting(ctx, usageWatermarkKey); err == nil {
		_ = json.Unmarshal(raw, &wm)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	var errs []error
	end := now.Truncate(time.Hour) // the current hour is not complete
	// Redo the last recorded hour too: points can land a little late.
	from := wm.Hour.Add(-time.Hour)
	if from.Before(end) {
		if err := s.recordHours(ctx, from, end); err != nil {
			errs = append(errs, err)
		} else {
			wm.Hour = end
		}
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for d := wm.Day; d.Before(today); d = d.AddDate(0, 0, 1) {
		if err := s.recordBackupDay(ctx, d); err != nil {
			errs = append(errs, err)
			break
		}
		wm.Day = d.AddDate(0, 0, 1)
	}
	if err := s.recordTraffic(ctx, now); err != nil {
		errs = append(errs, err)
	}
	if _, err := q.RollupUsage(ctx, now.Add(-rollupAfter)); err != nil {
		errs = append(errs, err)
	}
	raw, _ := json.Marshal(wm)
	if err := q.PutSetting(ctx, store.PutSettingParams{Key: usageWatermarkKey, Value: raw}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Service) upsertUsage(ctx context.Context, q *store.Queries, org, project, plan uuid.UUID, metric, gran string, at time.Time, qty float64) error {
	return q.UpsertUsage(ctx, store.UpsertUsageParams{
		OrgID: org, ProjectID: project, Metric: metric, Granularity: gran, PeriodStart: at, Quantity: numeric(qty), PlanID: plan,
	})
}

// recordHours records shared storage and dedicated instance hours for each
// hour in [from, to).
func (s *Service) recordHours(ctx context.Context, from, to time.Time) error {
	q := store.New(s.db)
	storage, err := q.HourlySharedStorage(ctx, store.HourlySharedStorageParams{FromTs: from, ToTs: to})
	if err != nil {
		return fmt.Errorf("shared storage: %w", err)
	}
	for _, r := range storage {
		if err := s.upsertUsage(ctx, q, r.OrgID, r.ProjectID, r.PlanID, MetricSharedStorage, "hour", r.PeriodStart, r.AvgBytes/gb); err != nil {
			return err
		}
		if r.IsBranch {
			if err := s.upsertUsage(ctx, q, r.OrgID, r.ProjectID, r.PlanID, MetricBranchStorage, "hour", r.PeriodStart, r.AvgBytes/gb); err != nil {
				return err
			}
		}
	}
	branches, err := q.HourlyBranches(ctx, store.HourlyBranchesParams{FromTs: from, LastHour: to.Add(-time.Hour)})
	if err != nil {
		return fmt.Errorf("branches: %w", err)
	}
	for _, r := range branches {
		if err := s.upsertUsage(ctx, q, r.OrgID, r.ProjectID, r.PlanID, MetricBranchHours, "hour", r.PeriodStart, max(0, min(1, r.Fraction))); err != nil {
			return err
		}
	}
	hooks, err := q.HourlyWebhookDeliveries(ctx, store.HourlyWebhookDeliveriesParams{FromTs: from, ToTs: to})
	if err != nil {
		return fmt.Errorf("webhook deliveries: %w", err)
	}
	for _, r := range hooks {
		if err := s.upsertUsage(ctx, q, r.OrgID, r.ProjectID, r.PlanID, MetricWebhookTries, "hour", r.PeriodStart, float64(r.Attempts)); err != nil {
			return err
		}
		if err := s.upsertUsage(ctx, q, r.OrgID, r.ProjectID, r.PlanID, MetricWebhookSent, "hour", r.PeriodStart, float64(r.Successes)); err != nil {
			return err
		}
	}
	runs, err := q.HourlyJobRuns(ctx, store.HourlyJobRunsParams{FromTs: from, ToTs: to})
	if err != nil {
		return fmt.Errorf("job runs: %w", err)
	}
	for _, r := range runs {
		metric := MetricJobRunsSQL
		if r.Kind == "http" {
			metric = MetricJobRunsHTTP
		}
		if err := s.upsertUsage(ctx, q, r.OrgID, r.ProjectID, r.PlanID, metric, "hour", r.PeriodStart, float64(r.Runs)); err != nil {
			return err
		}
	}
	ded, err := q.HourlyDedicated(ctx, store.HourlyDedicatedParams{FromTs: from, LastHour: to.Add(-time.Hour)})
	if err != nil {
		return fmt.Errorf("dedicated: %w", err)
	}
	for _, r := range ded {
		f := max(0, min(1, r.Fraction))
		vals := map[string]float64{
			MetricDedicatedCPU:  r.Cpus * f,
			MetricDedicatedRAM:  float64(r.MemMb) / 1000 * f,
			MetricDedicatedDisk: float64(r.DiskGb) * f,
		}
		if n := float64(r.Standbys); n > 0 {
			vals[MetricHACPU] = r.Cpus * f * n
			vals[MetricHARAM] = float64(r.MemMb) / 1000 * f * n
			vals[MetricHADisk] = float64(r.DiskGb) * f * n
		}
		if r.SyncReplication {
			vals[MetricSyncReplication] = f
		}
		for metric, v := range vals {
			if err := s.upsertUsage(ctx, q, r.OrgID, r.ProjectID, r.PlanID, metric, "hour", r.PeriodStart, v); err != nil {
				return err
			}
		}
	}
	// Read replicas, summed per project and hour.
	reps, err := q.HourlyReplicas(ctx, store.HourlyReplicasParams{FromTs: from, LastHour: to.Add(-time.Hour)})
	if err != nil {
		return fmt.Errorf("read replicas: %w", err)
	}
	type hourKey struct {
		project uuid.UUID
		at      time.Time
	}
	sums := map[hourKey]map[string]float64{}
	who := map[uuid.UUID]store.HourlyReplicasRow{}
	var order []hourKey
	for _, r := range reps {
		f := max(0, min(1, r.Fraction))
		k := hourKey{r.ProjectID, r.PeriodStart}
		if sums[k] == nil {
			sums[k] = map[string]float64{}
			order = append(order, k)
		}
		who[r.ProjectID] = r
		sums[k][MetricReplicaHours] += f
		sums[k][MetricReplicaCPU] += r.Cpus * f
		sums[k][MetricReplicaRAM] += float64(r.MemMb) / 1000 * f
		sums[k][MetricReplicaDisk] += float64(r.DiskGb) * f
	}
	for _, k := range order {
		r := who[k.project]
		for metric, v := range sums[k] {
			if err := s.upsertUsage(ctx, q, r.OrgID, k.project, r.PlanID, metric, "hour", k.at, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// recordBackupDay records the backup storage each project held on day.
func (s *Service) recordBackupDay(ctx context.Context, day time.Time) error {
	q := store.New(s.db)
	rows, err := q.DailyBackupBytes(ctx, store.DailyBackupBytesParams{DayStart: day, DayEnd: day.AddDate(0, 0, 1)})
	if err != nil {
		return fmt.Errorf("backup storage: %w", err)
	}
	for _, r := range rows {
		if err := s.upsertUsage(ctx, q, r.OrgID, r.ProjectID, r.PlanID, MetricBackupStorage, "day", day, r.Bytes/gb*24); err != nil {
			return err
		}
	}
	return nil
}

// recordTraffic adds the bytes each project moved through the poolers since
// the last sample to the current hour. The first sample after a start (or
// a pooler restart, which resets its counters) only sets the baseline.
func (s *Service) recordTraffic(ctx context.Context, now time.Time) error {
	pm := s.projects.Pooler()
	if pm == nil {
		return nil
	}
	q := store.New(s.db)
	names, err := q.ProjectPoolerNames(ctx)
	if err != nil {
		return err
	}
	type owner struct{ project, org, plan uuid.UUID }
	byName := map[string]owner{}
	for _, n := range names {
		o := owner{n.ProjectID, n.OrgID, n.PlanID}
		byName[n.DbName] = o
		if n.AliasDbName != nil {
			byName[*n.AliasDbName] = o
		}
	}
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	if s.traffic == nil {
		s.traffic = map[string]int64{}
	}
	hour := now.Truncate(time.Hour)
	var errs []error
	for _, a := range pm.Admins() {
		stats, err := a.TransferBytes(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for db, total := range stats {
			o, ok := byName[db]
			if !ok {
				continue
			}
			key := a.Name + "/" + db
			prev, seen := s.traffic[key]
			s.traffic[key] = total
			if !seen || total <= prev {
				continue
			}
			if err := q.AddUsage(ctx, store.AddUsageParams{
				OrgID: o.org, ProjectID: o.project, Metric: MetricPoolerTraffic, PeriodStart: hour,
				Quantity: numeric(float64(total-prev) / gb), PlanID: o.plan,
			}); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func numeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%.9f", f))
	return n
}
