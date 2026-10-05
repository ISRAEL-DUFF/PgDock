package metrics_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/metrics"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

// The Prometheus export is read by the platform admin and by whatever holds
// the metrics token. They may see organisations, sizes and usage (V2 s2.4),
// but not the names a tenant gave its projects and databases.
func TestPrometheusLabelsDoNotExposeTenantNames(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	q := store.New(db)

	const (
		projectName = "Acme Takeover Plan"
		dbName      = "acme_takeover_db"
		orgName     = "Acme Holdings"
	)
	var org uuid.UUID
	if err := db.QueryRow(ctx, `INSERT INTO organizations (name, slug, plan_id)
		SELECT $1::text, 'acme-holdings', id FROM quota_plans WHERE name = 'Personal' RETURNING id`, orgName).Scan(&org); err != nil {
		t.Fatal(err)
	}
	node, err := q.UpsertNode(ctx, store.UpsertNodeParams{Name: "node-a", PrivateAddr: "127.0.0.1", Role: "both"})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := q.InsertInstance(ctx, store.InsertInstanceParams{ID: uuid.New(), NodeID: node.ID, Kind: "shared", PgVersion: 18})
	if err != nil {
		t.Fatal(err)
	}
	p, err := q.InsertProject(ctx, store.InsertProjectParams{
		ID: uuid.New(), OrgID: org, Name: projectName, Slug: "acme-takeover-plan", DbName: dbName, OwnerRole: dbName + "_owner",
		ScramVerifier: "SCRAM-SHA-256$4096:x$y:z", Tier: "shared", InstanceID: inst.ID, Settings: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetProjectStatus(ctx, store.SetProjectStatusParams{ID: p.ID, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for scope, id := range map[string]uuid.UUID{metrics.ScopeProject: p.ID, metrics.ScopeNode: node.ID} {
		metric := "size_bytes"
		if scope == metrics.ScopeNode {
			metric = "load1"
		}
		if err := q.UpsertMetricPoints(ctx, store.UpsertMetricPointsParams{
			Scope: scope, ScopeIds: []uuid.UUID{id}, Metrics: []string{metric}, Ts: now, Vals: []float64{42},
		}); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	if err := metrics.WritePrometheus(ctx, &out, db, time.Minute); err != nil {
		t.Fatal(err)
	}
	text := out.String()

	for _, secret := range []string{projectName, dbName, "takeover", "Takeover"} {
		if strings.Contains(text, secret) {
			t.Errorf("the export contains the tenant-chosen %q:\n%s", secret, text)
		}
	}
	for _, want := range []string{
		`project_id="` + p.ID.String() + `"`,
		`org_id="` + org.String() + `"`,
		`org="` + orgName + `"`,
		`tier="shared"`,
		`node="node-a"`, // nodes belong to the platform, so their names stay
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the export lacks %s:\n%s", want, text)
		}
	}
}
