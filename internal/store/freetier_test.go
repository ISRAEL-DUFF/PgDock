package store_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/xenibase/internal/store"
	"github.com/israel-duff/xenibase/internal/store/storetest"
)

// The Free tier pauses idle projects of organisations on the Free billing
// plan. The platform's own organisation (the Unlimited quota plan) is
// given a Free billing account by the billing migration, and must still
// never be paused: found when rehearsing the V2 to V4 upgrade, where the
// admin's own projects were next in line.
func TestFreeTierNeverSweepsTheUnlimitedPlan(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewEmpty(t)
	if err := store.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	node, inst := uuid.New(), uuid.New()
	for _, stmt := range []string{
		`INSERT INTO nodes (id, name, private_addr, role, agent_cert_fp, capacity) VALUES ('` + node.String() + `', 'n1', '10.0.0.1', 'both', 'fp', '{}')`,
		`INSERT INTO instances (id, node_id, kind, pg_version, port, status) VALUES ('` + inst.String() + `', '` + node.String() + `', 'shared', 18, 5432, 'running')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO price_books (version, effective_at, prices) VALUES (1, now(), '{}')`); err != nil {
		t.Fatal(err)
	}
	// One organisation per quota plan, each on the Free billing plan with a
	// shared project idle for a month.
	type org struct{ id, project uuid.UUID }
	mk := func(plan, slug string) org {
		o := org{uuid.New(), uuid.New()}
		if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug, plan_id) VALUES ($1, $2::text, $2::text, (SELECT id FROM quota_plans WHERE name = $3))`, o.id, slug, plan); err != nil {
			t.Fatalf("organization %s: %v", slug, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO billing_accounts (org_id, plan, price_book_version) VALUES ($1, 'free', 1) ON CONFLICT (org_id) DO UPDATE SET plan = 'free'`, o.id); err != nil {
			t.Fatalf("billing account %s: %v", slug, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO projects (id, name, slug, db_name, owner_role, scram_verifier, tier, instance_id, status, org_id, region, last_active_at)
			VALUES ($1, $2::text, $2::text, $2::text, $2::text || '_owner', 'SCRAM-SHA-256$4096:c2FsdA==$a2V5:a2V5', 'shared', $3, 'active', $4, 'eu-central', now() - interval '30 days')`,
			o.project, slug, inst, o.id); err != nil {
			t.Fatalf("project %s: %v", slug, err)
		}
		return o
	}
	platform, personal := mk("Unlimited", "platform"), mk("Personal", "personal")
	q := store.New(pool)
	idle := func() map[uuid.UUID]bool {
		rows, err := q.FreeProjectsIdleSince(ctx, ptrTime(7))
		if err != nil {
			t.Fatal(err)
		}
		m := map[uuid.UUID]bool{}
		for _, p := range rows {
			m[p.ID] = true
		}
		return m
	}
	if got := idle(); got[platform.project] || !got[personal.project] {
		t.Fatalf("idle Free projects: platform=%v personal=%v, want only the personal one", got[platform.project], got[personal.project])
	}

	// Asleep already (an upgrade that paused it, or an archive): the sweep
	// wakes the platform's, and never archives it.
	for _, o := range []org{platform, personal} {
		if _, err := pool.Exec(ctx, `UPDATE projects SET lifecycle = 'paused', paused_at = now() - interval '100 days' WHERE id = $1`, o.project); err != nil {
			t.Fatal(err)
		}
	}
	paused, err := q.FreeProjectsPausedSince(ctx, ptrTime(90))
	if err != nil {
		t.Fatal(err)
	}
	if len(paused) != 1 || paused[0].ID != personal.project {
		t.Fatalf("paused Free projects to archive: %d, want just the personal one", len(paused))
	}
	wake, err := q.SleepingPaidProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(wake) != 1 || wake[0].ID != platform.project {
		t.Fatalf("projects to wake: %d, want just the platform's", len(wake))
	}
}

// ptrTime is the moment that many days ago.
func ptrTime(days int) *time.Time {
	t := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	return &t
}
