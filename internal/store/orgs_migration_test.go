package store_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

// TestV1DataMovesIntoThePersonalOrg loads a V1 installation (migration 9:
// one owner, projects, operations, audit rows), upgrades it, and checks
// that the owner became the platform admin with a personal organisation
// holding every project, unchanged (M8 done-when).
func TestV1DataMovesIntoThePersonalOrg(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewEmpty(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := store.MigrateTo(ctx, pool, 9, log); err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	node, inst, target := uuid.New(), uuid.New(), uuid.New()
	projects := []uuid.UUID{uuid.New(), uuid.New()}
	for _, stmt := range []string{
		`INSERT INTO operators (id, email, password_hash, role) VALUES ('` + owner.String() + `', 'Owner@Example.com', 'x', 'owner')`,
		`INSERT INTO nodes (id, name, private_addr, role, agent_cert_fp, capacity) VALUES ('` + node.String() + `', 'n1', '10.0.0.1', 'both', 'fp', '{}')`,
		`INSERT INTO instances (id, node_id, kind, pg_version, port, status) VALUES ('` + inst.String() + `', '` + node.String() + `', 'shared', 18, 5432, 'running')`,
		`INSERT INTO storage_targets (id, name, endpoint, bucket, credentials, is_default) VALUES ('` + target.String() + `', 't', 'https://s3', 'b', '\x00', true)`,
		`INSERT INTO sessions (id, operator_id) VALUES ('s1', '` + owner.String() + `')`,
		`INSERT INTO audit_log (operator_id, action, outcome) VALUES ('` + owner.String() + `', 'project.create', 'success')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for i, id := range projects {
		db := []string{"blog_k2f9", "shop_x7q1"}[i]
		if _, err := pool.Exec(ctx, `INSERT INTO projects (id, name, slug, db_name, owner_role, scram_verifier, tier, instance_id, status, storage_target_id, created_by)
			VALUES ($1, $2, $2, $3, $3 || '_owner', 'SCRAM-SHA-256$4096:c2FsdA==$a2V5:a2V5', 'shared', $4, 'active', $5, $6)`,
			id, db, db, inst, target, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO operations (kind, project_id, status, created_by) VALUES ('create', $1, 'succeeded', $2)`, id, owner); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Migrate(ctx, pool, log); err != nil {
		t.Fatal(err)
	}
	q := store.New(pool)
	u, err := q.GetUser(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if u.PlatformRole != "platform_admin" || u.EmailVerifiedAt == nil || u.ApprovedAt == nil {
		t.Fatalf("owner after the upgrade: %+v", u)
	}
	org, err := q.GetPersonalOrg(ctx, &owner)
	if err != nil {
		t.Fatal(err)
	}
	if org.Name != "Owner's projects" {
		t.Errorf("personal org name %q", org.Name)
	}
	if m, err := q.GetOrgMember(ctx, store.GetOrgMemberParams{OrgID: org.ID, UserID: owner}); err != nil || m.Role != "owner" {
		t.Fatalf("membership: %+v %v", m, err)
	}
	var plan string
	if err := pool.QueryRow(ctx, `SELECT p.name FROM quota_plans p WHERE p.id = $1`, org.PlanID).Scan(&plan); err != nil || plan != "Unlimited" {
		t.Fatalf("plan %q %v", plan, err)
	}
	list, err := q.ListOrgProjects(ctx, store.ListOrgProjectsParams{OrgID: org.ID, SeeAll: true, UserID: owner, MaxRows: 10})
	if err != nil || len(list) != 2 {
		t.Fatalf("projects in the personal org: %d %v", len(list), err)
	}
	for _, p := range list {
		// Names, roles, and verifiers (so connection strings) are untouched.
		if p.DbName != p.Slug || p.OwnerRole != p.DbName+"_owner" || p.Status != "active" {
			t.Errorf("project changed: %+v", p)
		}
	}
	if n, err := q.CountOrgOwners(ctx, org.ID); err != nil || n != 1 {
		t.Fatalf("owners: %d %v", n, err)
	}
	var sessUser uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT user_id FROM sessions WHERE id = 's1'`).Scan(&sessUser); err != nil || sessUser != owner {
		t.Fatalf("session: %v %v", sessUser, err)
	}
	var auditUser uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT user_id FROM audit_log WHERE action = 'project.create'`).Scan(&auditUser); err != nil || auditUser != owner {
		t.Fatalf("audit: %v %v", auditUser, err)
	}

	// And back down, for a rollback to V1.
	if err := store.MigrateTo(ctx, pool, 9, log); err != nil {
		t.Fatalf("down: %v", err)
	}
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM operators WHERE id = $1`, owner).Scan(&role); err != nil || role != "owner" {
		t.Fatalf("after down: %q %v", role, err)
	}
}
