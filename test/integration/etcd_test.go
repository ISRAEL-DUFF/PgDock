package integration

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// threeNodes registers node-b and node-c beside the harness's "test" node
// and starts their agents; it returns the three nodes' ids.
func threeNodes(t *testing.T, e *testenv.Env, role gen.CreateNodeRequestRole) []gen.Node {
	t.Helper()
	var list gen.NodeList
	e.Do("GET", "/api/v1/nodes", nil, &list)
	var out []gen.Node
	for _, n := range list.Items {
		if n.Name == "test" {
			out = append(out, n)
		}
	}
	for i, name := range []string{"node-b", "node-c"} {
		var created gen.NodeCreated
		addr := []string{"agent-test-2", "agent-test-3"}[i]
		if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: name, PrivateAddr: addr, Role: role}, &created); code != http.StatusCreated {
			t.Fatalf("create %s: %d", name, code)
		}
		if i == 0 {
			e.StartSecondAgent(name, created.Token)
		} else {
			e.StartThirdAgent(name, created.Token)
		}
		out = append(out, created.Node)
	}
	// Wait until each new agent has been seen healthy.
	deadline := time.Now().Add(30 * time.Second)
	for {
		e.Do("GET", "/api/v1/nodes", nil, &list)
		healthy := 0
		for _, n := range list.Items {
			if n.Status == "healthy" {
				healthy++
			}
		}
		if healthy >= 3 {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes not healthy: %+v", list.Items)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// setupEtcd bootstraps the etcd cluster on the three nodes.
func setupEtcd(t *testing.T, e *testenv.Env, ns []gen.Node) gen.EtcdCluster {
	t.Helper()
	ids := []gen.Node{ns[0], ns[1], ns[2]}
	req := gen.EtcdSetupRequest{}
	for _, n := range ids {
		req.NodeIds = append(req.NodeIds, n.Id)
	}
	var op gen.Operation
	if code := e.Do("POST", "/api/v1/admin/etcd", req, &op); code != http.StatusAccepted {
		t.Fatalf("etcd setup: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("etcd setup: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	var c gen.EtcdCluster
	var region string
	if err := e.DB.QueryRow(context.Background(), `SELECT region FROM nodes WHERE id = $1`, ns[0].Id).Scan(&region); err != nil {
		t.Fatal(err)
	}
	if code := e.Do("GET", "/api/v1/admin/etcd?region="+region, nil, &c); code != http.StatusOK || !c.Ready || len(c.Members) != 3 {
		t.Fatalf("etcd cluster: %d %+v", code, c)
	}
	return c
}

func TestEtcdCluster(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	e.StartAgent()
	ns := threeNodes(t, e, gen.CreateNodeRequestRoleDedicated)

	// Two nodes or the same node twice are refused.
	if code := e.Do("POST", "/api/v1/admin/etcd", gen.EtcdSetupRequest{NodeIds: []openapi_types.UUID{ns[0].Id, ns[0].Id, ns[1].Id}}, nil); code != http.StatusBadRequest {
		t.Fatalf("a node twice: %d", code)
	}
	c := setupEtcd(t, e, ns)
	for _, m := range c.Members {
		if m.Status != gen.EtcdMemberStatusHealthy || !strings.HasPrefix(m.ClientUrl, "https://") {
			t.Fatalf("member: %+v", m)
		}
	}
	if code := e.Do("POST", "/api/v1/admin/etcd", gen.EtcdSetupRequest{NodeIds: []openapi_types.UUID{ns[0].Id, ns[1].Id, ns[2].Id}}, nil); code != http.StatusConflict {
		t.Fatalf("a second setup: %d", code)
	}

	// Clients need a certificate from the etcd CA.
	member := "pgdock-etcd-" + ns[1].Id.String()
	endpoint := "--endpoints=" + c.Members[0].ClientUrl
	ctl := func(args ...string) (string, error) {
		out, err := exec.Command("docker", append([]string{"exec", member, "/usr/local/bin/etcdctl", endpoint, "--cacert=/etc/pgdock-etcd/ca.pem"}, args...)...).CombinedOutput()
		return string(out), err
	}
	if out, err := ctl("--cert=/etc/pgdock-etcd/member.pem", "--key=/etc/pgdock-etcd/member-key.pem", "endpoint", "health"); err != nil || !strings.Contains(out, "is healthy") {
		t.Fatalf("with a certificate: %v %s", err, out)
	}
	if out, err := ctl("--command-timeout=3s", "endpoint", "health"); err == nil {
		t.Fatalf("without a client certificate the member answered: %s", out)
	}

	// One member down: the cluster keeps its quorum.
	if out, err := exec.Command("docker", "kill", "pgdock-etcd-"+ns[2].Id.String()).CombinedOutput(); err != nil {
		t.Fatalf("kill member: %v %s", err, out)
	}
	e.Do("GET", "/api/v1/admin/etcd", nil, &c)
	down := 0
	for _, m := range c.Members {
		if m.Status != gen.EtcdMemberStatusHealthy {
			down++
		}
	}
	if !c.Ready || down != 1 {
		t.Fatalf("with one member down: ready %v, %d unhealthy: %+v", c.Ready, down, c.Members)
	}
}
