package store_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tenantTables hold (or point at) organisations' data.
var tenantTables = []string{
	"projects", "project_members", "project_db_users", "org_members", "invitations", "organizations",
	"backups", "operations", "audit_log", "retired_databases", "alerts", "metric_points",
	"usage_records", "reaped_sessions", "break_glass_sessions", "dedicated_requests",
	"api_tokens", "device_auth_requests", "editor_preferences", "storage_targets", "backup_keys",
}

// TestTenantQueriesAreScoped is the V2 §2.6 lint: every query on a tenant
// table filters by org_id, or says why it may not with a
// "-- tenant: system - <reason>" line (background workers, the platform
// admin's views, lookups of a resource the request already authorized).
func TestTenantQueriesAreScoped(t *testing.T) {
	files, err := filepath.Glob("queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no queries: %v", err)
	}
	touch := regexp.MustCompile(`\b(from|join|update|into)\s+(` + strings.Join(tenantTables, "|") + `)\b`)
	comment := regexp.MustCompile(`--[^\n]*`)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range regexp.MustCompile(`(?m)^-- name: `).Split(string(raw), -1)[1:] {
			name := strings.Fields(q)[0]
			sql := strings.ToLower(comment.ReplaceAllString(q, ""))
			if !touch.MatchString(sql) {
				continue
			}
			if strings.Contains(sql, "org_id") {
				continue
			}
			if m := regexp.MustCompile(`(?m)^-- tenant: system - (.+)$`).FindStringSubmatch(q); m != nil && len(strings.TrimSpace(m[1])) > 10 {
				continue
			}
			t.Errorf("%s: %s reads or writes a tenant table without an org_id predicate; scope it, or explain with \"-- tenant: system - <reason>\"",
				filepath.Base(f), name)
		}
	}
}
