package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/regions"
	"github.com/israel-duff/pgdock/internal/store"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestRegionEtcdMoveAll covers V4.1 §8.1's region page: the HA projects
// whose Patroni state is in another region's etcd are listed, and Move
// all moves them one after the other, never two at once. Each project's
// move is replaced by an operation the test finishes, so the sequencing is
// what's tested; TestEtcdPerRegionAndReplace covers a real move.
func TestRegionEtcdMoveAll(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	pa := e.CreateProject("ha-a").Project.Id
	pb := e.CreateProject("ha-b").Project.Id
	p, err := store.New(e.DB).GetProject(ctx, pa)
	if err != nil {
		t.Fatal(err)
	}
	// Both projects' instance under Patroni with its state in another
	// region's cluster; the home region gets a healthy cluster of three.
	if _, err := e.Regions.Save(ctx, regions.Input{ID: "ng-lagos", Name: "Lagos", Country: "NG", Provider: "manual"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Exec(ctx, `UPDATE instances SET patroni = true, etcd_region = 'ng-lagos' WHERE id = $1`, p.InstanceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.DB.Exec(context.Background(), `UPDATE instances SET patroni = false, etcd_region = NULL WHERE id = $1`, p.InstanceID)
		_, _ = e.DB.Exec(context.Background(), `DELETE FROM etcd_members WHERE region = $1`, p.Region)
		_, _ = e.DB.Exec(context.Background(), `DELETE FROM regions WHERE id = 'ng-lagos'`)
	})
	path := "/api/v1/admin/regions/" + p.Region
	var ov gen.RegionOverview
	if code := e.Do("GET", path+"/overview", nil, &ov); code != http.StatusOK || len(ov.HaElsewhere) != 2 || ov.EtcdReady || ov.EtcdProblem == nil {
		t.Fatalf("overview without a cluster: %d %+v", code, ov)
	}
	var apiErr gen.Error
	if code := e.Do("POST", path+"/etcd-move-all", nil, &apiErr); code != http.StatusConflict {
		t.Fatalf("move all without a cluster: %d %+v", code, apiErr)
	}
	for i := range 3 {
		var n gen.NodeCreated
		if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "etcd-" + string(rune('a'+i)), PrivateAddr: "10.9.0." + string(rune('1'+i)),
			Role: gen.CreateNodeRequestRoleDedicated}, &n); code != http.StatusCreated {
			t.Fatalf("node: %d", code)
		}
		if _, err := e.DB.Exec(ctx, `INSERT INTO etcd_members (node_id, name, client_url, peer_url, region, status) VALUES ($1, $2, $3, $3, $4, 'healthy')`,
			n.Node.Id, "etcd-"+n.Node.Id.String()[:8], "http://10.9.0.1:2379", p.Region); err != nil {
			t.Fatal(err)
		}
	}
	if code := e.Do("GET", path+"/overview", nil, &ov); code != http.StatusOK || !ov.EtcdReady || len(ov.EtcdMembers) != 3 {
		t.Fatalf("overview with a cluster: %d %+v", code, ov)
	}

	// Each project's move: an operation no worker runs, finished below.
	children := make(chan uuid.UUID, 4)
	moved := map[uuid.UUID]uuid.UUID{}
	e.Dedicated.SetEtcdMoveOne(func(ctx context.Context, project uuid.UUID, by *uuid.UUID) (store.Operation, error) {
		op, err := jobs.Enqueue(ctx, e.DB, jobs.EnqueueParams{Kind: "test_etcd_move", ProjectID: &project, CreatedBy: by})
		if err == nil {
			children <- op.ID
		}
		return op, err
	})
	t.Cleanup(func() { e.Dedicated.SetEtcdMoveOne(nil) })
	var all gen.Operation
	if code := e.Do("POST", path+"/etcd-move-all", nil, &all); code != http.StatusAccepted {
		t.Fatalf("move all: %d", code)
	}
	if code := e.Do("POST", path+"/etcd-move-all", nil, &apiErr); code != http.StatusConflict {
		t.Fatalf("move all twice: %d %+v", code, apiErr)
	}
	for i := range 2 {
		var child uuid.UUID
		select {
		case child = <-children:
		case <-time.After(30 * time.Second):
			t.Fatalf("move %d never started", i+1)
		}
		// Only this one is under way: the next waits for it.
		time.Sleep(2 * moveAllPollWait)
		if n := len(children); n != 0 {
			t.Fatalf("move %d: %d more started while it ran", i+1, n)
		}
		var pid uuid.UUID
		if err := e.DB.QueryRow(ctx, `UPDATE operations SET status = 'succeeded', finished_at = now() WHERE id = $1 RETURNING project_id`, child).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		moved[pid] = child
	}
	if op := e.WaitOperation(all.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("move all: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	if _, ok := moved[pa]; !ok {
		t.Fatalf("project A wasn't moved: %v", moved)
	}
	if _, ok := moved[pb]; !ok {
		t.Fatalf("project B wasn't moved: %v", moved)
	}
	if code := e.Do("GET", path+"/overview", nil, &ov); code != http.StatusOK || ov.MoveAll == nil || ov.MoveAll.Id != all.Id {
		t.Fatalf("overview after: %d %+v", code, ov)
	}
}

// moveAllPollWait is how often Move all checks on the move it waits for.
const moveAllPollWait = 2 * time.Second
