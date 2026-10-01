package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestMultiNodeSharedPlacement adds a second node through the API, runs a
// shared cluster on it through its agent, and checks new shared projects
// spread across both clusters and work through the poolers.
func TestMultiNodeSharedPlacement(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	ctx := context.Background()

	// Make the harness's node shared-only: then no node takes dedicated
	// instances.
	var nodes gen.NodeList
	e.Do("GET", "/api/v1/nodes", nil, &nodes)
	if len(nodes.Items) != 1 || nodes.Items[0].Role != "both" {
		t.Fatalf("nodes: %+v", nodes.Items)
	}
	var updated gen.Node
	if code := e.Do("PATCH", "/api/v1/nodes/"+nodes.Items[0].Id.String(), gen.UpdateNodeRequest{Role: gen.UpdateNodeRequestRoleShared}, &updated); code != http.StatusOK || updated.Role != "shared" {
		t.Fatalf("set role: %d %+v", code, updated)
	}
	tier := gen.ProjectTierDedicated
	var apiErr gen.Error
	e.ConfigureBackups()
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Too early", Tier: &tier}, &apiErr); code != http.StatusServiceUnavailable || apiErr.Code != "no_capacity" {
		t.Fatalf("dedicated without a dedicated node: %d %+v", code, apiErr)
	}

	var created gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-b", PrivateAddr: "agent-test-2", Role: gen.CreateNodeRequestRoleShared}, &created); code != http.StatusCreated || created.Token == "" {
		t.Fatalf("create node: %d %+v", code, created)
	}
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "node-b", PrivateAddr: "x", Role: gen.CreateNodeRequestRoleShared}, nil); code != http.StatusBadRequest {
		t.Fatalf("duplicate node name: %d", code)
	}
	e.StartSecondAgent("node-b", created.Token)

	var detail gen.NodeDetail
	if code := e.Do("GET", "/api/v1/nodes/"+created.Node.Id.String(), nil, &detail); code != http.StatusOK || !detail.Node.Agent.Registered {
		t.Fatalf("node detail: %d %+v", code, detail.Node.Agent)
	}

	var op gen.Operation
	if code := e.Do("POST", "/api/v1/nodes/"+created.Node.Id.String()+"/shared-cluster", gen.SharedClusterRequest{MemoryMb: 512}, &op); code != http.StatusAccepted {
		t.Fatalf("shared cluster: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("shared cluster: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	if code := e.Do("POST", "/api/v1/nodes/"+created.Node.Id.String()+"/shared-cluster", gen.SharedClusterRequest{MemoryMb: 512}, nil); code != http.StatusConflict {
		t.Fatalf("second shared cluster on one node: %d", code)
	}

	// New shared projects go to the least loaded cluster.
	perNode := map[string]int{}
	for _, name := range []string{"Spread A", "Spread B", "Spread C", "Spread D"} {
		c := e.CreateProject(name)
		var p gen.Project
		e.Do("GET", "/api/v1/projects/"+c.Project.Id.String(), nil, &p)
		if p.Instance == nil || p.Instance.Kind != "shared" {
			t.Fatalf("%s: instance %+v", name, p.Instance)
		}
		perNode[p.Instance.NodeName]++
		conn := e.MustConnect(c.Connection.PooledUrl)
		var db string
		if err := conn.QueryRow(ctx, `SELECT current_database()`).Scan(&db); err != nil || db != c.Project.DbName {
			t.Fatalf("%s through the pooler: %q %v", name, db, err)
		}
	}
	if perNode["test"] != 2 || perNode["node-b"] != 2 {
		t.Fatalf("placement: %v", perNode)
	}

	e.Do("GET", "/api/v1/nodes/"+created.Node.Id.String(), nil, &detail)
	if len(detail.Instances) != 1 || detail.Instances[0].Kind != "shared" || detail.Instances[0].Projects != 2 {
		t.Fatalf("node-b instances: %+v", detail.Instances)
	}

	// A node with instances cannot be removed.
	e.Reauth()
	if code := e.Do("DELETE", "/api/v1/nodes/"+created.Node.Id.String(), nil, &apiErr); code != http.StatusConflict {
		t.Fatalf("remove busy node: %d %+v", code, apiErr)
	}
	// An empty one can.
	var spare gen.NodeCreated
	e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "spare", PrivateAddr: "10.0.0.9", Role: gen.CreateNodeRequestRoleDedicated}, &spare)
	if code := e.Do("DELETE", "/api/v1/nodes/"+spare.Node.Id.String(), nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove empty node: %d", code)
	}
}
