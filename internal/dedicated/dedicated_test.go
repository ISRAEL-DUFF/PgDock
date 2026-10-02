package dedicated_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/agentca"
	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/dedicated"
	"github.com/israel-duff/pgdock/internal/nodes"
	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/storage"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// secrets stands in for the backup service: storage and a key exist.
type secrets struct{}

func (secrets) StorageTarget(context.Context) (uuid.UUID, storage.Target, error) {
	return uuid.New(), storage.Target{}, nil
}
func (secrets) BackupKey(context.Context) ([]byte, error) { return make([]byte, 32), nil }

type env struct {
	db  *pgxpool.Pool
	svc *dedicated.Service
}

func setup(t *testing.T) *env {
	t.Helper()
	db := storetest.New(t)
	k, _ := crypto.GenerateKey()
	kr, _ := crypto.NewKeyring(k)
	ca, err := agentca.LoadOrCreate(context.Background(), db, kr)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := nodes.NewService(db, ca, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	return &env{db: db, svc: dedicated.New(db, kr, ns, nil, secrets{}, dedicated.Config{}, quiet)}
}

// node adds a dedicated-capable node with a registered agent in the given
// status.
func (e *env) node(t *testing.T, name, status string) store.Node {
	t.Helper()
	ctx := context.Background()
	n, err := store.New(e.db).UpsertNode(ctx, store.UpsertNodeParams{Name: name, PrivateAddr: "127.0.0.1", Role: "both"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `UPDATE nodes SET agent_cert_fp = 'fp', status = $2 WHERE id = $1`, n.ID, status); err != nil {
		t.Fatal(err)
	}
	return n
}

// instance adds an instance of kind on node, created age ago.
func (e *env) instance(t *testing.T, node store.Node, kind, age string) store.Instance {
	t.Helper()
	ctx := context.Background()
	inst, err := store.New(e.db).InsertInstance(ctx, store.InsertInstanceParams{ID: uuid.New(), NodeID: node.ID, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, `UPDATE instances SET created_at = now() - $2::interval WHERE id = $1`, inst.ID, age); err != nil {
		t.Fatal(err)
	}
	return inst
}

// project adds a project on inst; deleted soft-deletes it, as a rolled-back
// create does.
func (e *env) project(t *testing.T, inst store.Instance, name string, deleted bool) {
	t.Helper()
	ctx := context.Background()
	var org uuid.UUID
	if err := e.db.QueryRow(ctx, `INSERT INTO organizations (name, slug, plan_id)
		SELECT $1::text, $1::text, id FROM quota_plans WHERE name = 'Personal' RETURNING id`, name).Scan(&org); err != nil {
		t.Fatal(err)
	}
	p, err := store.New(e.db).InsertProject(ctx, store.InsertProjectParams{
		ID: uuid.New(), OrgID: org, Name: name, Slug: name, DbName: name, OwnerRole: name + "_owner",
		ScramVerifier: "SCRAM-SHA-256$4096:x$y:z", Tier: "dedicated", InstanceID: inst.ID, Settings: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		if err := store.New(e.db).SoftDeleteProject(ctx, store.SoftDeleteProjectParams{ID: p.ID, Status: provision.StatusError}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidateRefusesAnUnreachableNode(t *testing.T) {
	e := setup(t)
	n := e.node(t, "node2", "unreachable")

	params := provision.CreateParams{NodeID: &n.ID}
	_, err := e.svc.Validate(context.Background(), &params)
	if !errors.Is(err, provision.ErrConflict) || !strings.Contains(err.Error(), "node2 is unreachable") {
		t.Fatalf("pinning an unreachable node: got %v, want a conflict naming the node and its state", err)
	}

	if _, err := e.db.Exec(context.Background(), `UPDATE nodes SET status = 'healthy' WHERE id = $1`, n.ID); err != nil {
		t.Fatal(err)
	}
	params = provision.CreateParams{NodeID: &n.ID}
	if _, err := e.svc.Validate(context.Background(), &params); err != nil {
		t.Fatalf("pinning a healthy node: %v", err)
	}
	if params.NodeID == nil || *params.NodeID != n.ID {
		t.Fatal("the pinned node was changed")
	}
}

func TestListOrphanedInstances(t *testing.T) {
	e := setup(t)
	n := e.node(t, "node2", "healthy")

	orphan := e.instance(t, n, "dedicated", "10 minutes") // no project at all
	failed := e.instance(t, n, "dedicated", "10 minutes") // its only project was rolled back
	e.project(t, failed, "failed", true)                  //
	inUse := e.instance(t, n, "dedicated", "10 minutes")  // a live project
	e.project(t, inUse, "live", false)                    //
	e.instance(t, n, "dedicated", "1 minute")             // too new: a create may still be setting it up
	e.instance(t, n, "shared", "10 minutes")              // shared clusters are not per-project
	// One shared cluster per node, so the stuck one lives on another node.
	stuck := e.instance(t, e.node(t, "node3", "healthy"), "shared", "10 minutes") // its failed creation could not be rolled back
	if err := store.New(e.db).SetInstanceStatus(context.Background(), store.SetInstanceStatusParams{ID: stuck.ID, Status: "error"}); err != nil {
		t.Fatal(err)
	}
	gone := e.instance(t, n, "dedicated", "10 minutes") // already removed
	if err := store.New(e.db).MarkInstanceDeleted(context.Background(), gone.ID); err != nil {
		t.Fatal(err)
	}

	got, err := store.New(e.db).ListOrphanedInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[uuid.UUID]bool{}
	for _, i := range got {
		ids[i.ID] = true
	}
	if len(got) != 3 || !ids[orphan.ID] || !ids[failed.ID] || !ids[stuck.ID] {
		t.Fatalf("orphans = %d instances, want exactly the unreferenced dedicated one, the rolled-back one, and the stuck shared cluster", len(got))
	}
}

func TestReapOrphansLeavesAnUnreachableNodeAlone(t *testing.T) {
	e := setup(t)
	n := e.node(t, "node2", "unreachable")
	orphan := e.instance(t, n, "dedicated", "10 minutes")

	removed, err := e.svc.ReapOrphans(context.Background())
	if err != nil || removed != 0 {
		t.Fatalf("ReapOrphans = %d, %v; want 0, nil while the node is down", removed, err)
	}
	inst, err := store.New(e.db).GetInstance(context.Background(), orphan.ID)
	if err != nil || (inst.DeletedAt != nil) {
		t.Fatalf("the instance must stay until its node can be reached (err %v, deleted %v)", err, (inst.DeletedAt != nil))
	}
}

func TestAddSharedClusterRefusesAnUnreachableNode(t *testing.T) {
	e := setup(t)
	n := e.node(t, "node2", "unreachable")

	_, err := e.svc.AddSharedCluster(context.Background(), n.ID, 2048, nil)
	if !errors.Is(err, provision.ErrConflict) || !strings.Contains(err.Error(), "node2 is unreachable") {
		t.Fatalf("a shared cluster on an unreachable node: got %v, want a conflict naming the node and its state", err)
	}
	var count int
	if err := e.db.QueryRow(context.Background(), `SELECT count(*) FROM instances WHERE node_id = $1`, n.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("a refused request must leave no instance behind (count %d, err %v)", count, err)
	}

	if _, err := e.db.Exec(context.Background(), `UPDATE nodes SET status = 'healthy' WHERE id = $1`, n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddSharedCluster(context.Background(), n.ID, 2048, nil); err != nil {
		t.Fatalf("a shared cluster on a healthy node: %v", err)
	}
}

// When the node cannot be reached, a rolled-back shared cluster must stay on
// record: the node may have created its container, and deleting the record
// would leave that running with nothing pointing at it.
func TestRollbackSharedClusterKeepsTheRecordWhenTheNodeIsUnreachable(t *testing.T) {
	e := setup(t)
	n := e.node(t, "node2", "unreachable")
	inst := e.instance(t, n, "shared", "1 minute")

	if err := dedicated.RollbackSharedCluster(e.svc, context.Background(), inst); err == nil {
		t.Fatal("rollback reported success though the node could not be asked to destroy the instance")
	}
	got, err := store.New(e.db).GetInstance(context.Background(), inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt != nil || got.Status != "error" || got.Error == nil || *got.Error == "" {
		t.Fatalf("instance after a failed rollback = status %q, deleted %v, error %v; want it kept as \"error\" with the reason",
			got.Status, got.DeletedAt != nil, got.Error)
	}
}
