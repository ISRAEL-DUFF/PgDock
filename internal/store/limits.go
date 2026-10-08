package store

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Quota limit keys (V2 §10.3). A limit that is absent is unlimited.
const (
	LimitProjects           = "projects"
	LimitBranches           = "branches"
	LimitSharedStorageMB    = "shared_storage_mb"
	LimitProjectStorageMB   = "project_storage_mb"
	LimitProjectConnections = "project_connections"
	LimitBackupStorageMB    = "backup_storage_mb"
	LimitWebhookPerMin      = "webhook_deliveries_per_min"
	LimitScheduledJobs      = "scheduled_jobs"
	LimitJobMinIntervalS    = "job_min_interval_s"
	LimitHTTPJobRunsPerHour = "http_job_runs_per_hour"
	LimitConsoleQueries     = "console_queries"
	LimitOperationsInFlight = "operations_in_flight"
	// Backend services' storage (V4 §10.1).
	LimitFileStorageMB     = "file_storage_mb"
	LimitStorageEgressMBMo = "storage_egress_mb_per_month"
	LimitImageTransformsMo = "image_transforms_per_month"
	LimitUploadMaxMB       = "upload_max_mb"
	// Realtime (V4 §10.1): concurrent connections, and the free plan's messages.
	LimitRealtimeConnections = "realtime_connections"
	LimitRealtimeMessagesMo  = "realtime_messages_per_month"
)

// LimitKeys are the known limits, in display order.
var LimitKeys = []string{
	LimitProjects, LimitBranches, LimitSharedStorageMB, LimitProjectStorageMB, LimitProjectConnections,
	LimitBackupStorageMB, LimitWebhookPerMin, LimitScheduledJobs, LimitJobMinIntervalS,
	LimitHTTPJobRunsPerHour, LimitConsoleQueries, LimitOperationsInFlight,
	LimitFileStorageMB, LimitStorageEgressMBMo, LimitImageTransformsMo, LimitUploadMaxMB,
	LimitRealtimeConnections, LimitRealtimeMessagesMo,
}

// Limits are an organisation's effective quotas: its plan's limits with its
// overrides applied. A key that is absent is unlimited.
type Limits map[string]int64

// Get returns a limit and whether there is one.
func (l Limits) Get(key string) (int64, bool) {
	v, ok := l[key]
	return v, ok
}

// DecodePlanLimits parses a plan's limits column.
func DecodePlanLimits(raw json.RawMessage) (Limits, error) {
	m := map[string]*int64{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("plan limits: %w", err)
		}
	}
	out := Limits{}
	for k, v := range m {
		if v != nil {
			out[k] = *v
		}
	}
	return out, nil
}

// EffectiveLimits applies an organisation's overrides to its plan's limits:
// an override with a number replaces the plan's, one with null removes it
// (unlimited).
func EffectiveLimits(plan, overrides json.RawMessage) (Limits, error) {
	l, err := DecodePlanLimits(plan)
	if err != nil {
		return nil, err
	}
	o := map[string]*int64{}
	if len(overrides) > 0 {
		if err := json.Unmarshal(overrides, &o); err != nil {
			return nil, fmt.Errorf("limit overrides: %w", err)
		}
	}
	for k, v := range o {
		if v == nil {
			delete(l, k)
		} else {
			l[k] = *v
		}
	}
	return l, nil
}

// ValidateLimits checks limits (or overrides, where null is allowed) use
// known keys and non-negative values.
func ValidateLimits(raw json.RawMessage, allowNull bool) error {
	m := map[string]*int64{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("limits must be an object of whole numbers: %w", err)
	}
	known := map[string]bool{}
	for _, k := range LimitKeys {
		known[k] = true
	}
	var bad []string
	for k, v := range m {
		switch {
		case !known[k]:
			bad = append(bad, "unknown limit "+k)
		case v == nil && !allowNull:
			bad = append(bad, k+" must be a number (leave it out for unlimited)")
		case v != nil && *v < 0:
			bad = append(bad, k+" must not be negative")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("%v", bad)
	}
	return nil
}

// DedicatedAllowance is how many dedicated instances an organisation may
// run, and their total size (V2 §10.6). Zero means none.
type DedicatedAllowance struct {
	Instances int     `json:"instances"`
	CPUs      float64 `json:"cpus"`
	MemoryMB  int     `json:"memory_mb"`
	DiskGB    int     `json:"disk_gb"`
}

// DecodeDedicatedAllowance parses organizations.dedicated_allowance.
func DecodeDedicatedAllowance(raw json.RawMessage) (DedicatedAllowance, error) {
	var a DedicatedAllowance
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return a, fmt.Errorf("dedicated allowance: %w", err)
		}
	}
	return a, nil
}
