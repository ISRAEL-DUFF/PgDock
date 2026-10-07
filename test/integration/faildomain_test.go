package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestFailureDomains is V3.1-M1's done-when: with the nodes in racks, the
// etcd cluster needs three racks, enabling HA puts the standby in another
// rack from the primary and is refused when only same-rack nodes are
// free, a second pooler host in the first's rack is warned about, and the
// failure-domain check and its alert follow the racks as they change.
func TestFailureDomains(t *testing.T) {
	needDedicated(t)
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	e.StartAgent()
	e.ConfigureBackups()
	e.SetNodeRole("test", "dedicated")
	ns := threeNodes(t, e, gen.CreateNodeRequestRoleDedicated)
	rack := func(n gen.Node, d string) {
		t.Helper()
		var got gen.Node
		if code := e.Do("PATCH", "/api/v1/nodes/"+n.Id.String(), gen.UpdateNodeRequest{FailureDomain: &d}, &got); code != http.StatusOK ||
			got.FailureDomain == nil || *got.FailureDomain != d || got.FailureDomainLabel == nil || *got.FailureDomainLabel != d {
			t.Fatalf("set %s's domain to %s: %d %+v", n.Name, d, code, got)
		}
	}
	bad := "not a rack!"
	if code := e.Do("PATCH", "/api/v1/nodes/"+ns[0].Id.String(), gen.UpdateNodeRequest{FailureDomain: &bad}, nil); code != http.StatusBadRequest {
		t.Fatalf("a bad domain: %d", code)
	}
	problems := func() []gen.FailureDomainProblem {
		t.Helper()
		var out gen.FailureDomainProblems
		if code := e.Do("GET", "/api/v1/admin/failure-domains", nil, &out); code != http.StatusOK {
			t.Fatalf("failure domains: %d", code)
		}
		return out.Items
	}
	alert := func() map[string]string {
		t.Helper()
		if err := e.Alerts.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		rows, err := e.DB.Query(ctx, `SELECT target_id, status FROM alerts WHERE kind = 'failure_domain'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var id, st string
			_ = rows.Scan(&id, &st)
			out[id] = st
		}
		return out
	}

	// 1. etcd: three members in one rack are refused.
	for _, n := range ns {
		rack(n, "rack-a")
	}
	req := gen.EtcdSetupRequest{NodeIds: []openapi_types.UUID{ns[0].Id, ns[1].Id, ns[2].Id}}
	var e1 gen.Error
	if code := e.Do("POST", "/api/v1/admin/etcd", req, &e1); code != http.StatusConflict || !strings.Contains(e1.Message, "failure domain") {
		t.Fatalf("etcd in one rack: %d %s", code, e1.Message)
	}
	rack(ns[1], "rack-b")
	rack(ns[2], "rack-c")
	setupEtcd(t, e, ns)
	if p := problems(); len(p) != 0 {
		t.Fatalf("problems with three racks: %+v", p)
	}

	// 2. A dedicated project on test (rack-a); the other two move into
	// rack-a, so HA has nowhere to go.
	tier, profile, vol := gen.ProjectTierDedicated, "small", 5
	var c gen.ProjectCredentials
	if code := e.Do("POST", "/api/v1/projects", gen.CreateProjectRequest{Name: "Racked", Tier: &tier, Profile: &profile, VolumeGb: &vol, NodeId: &ns[0].Id}, &c); code != http.StatusAccepted {
		t.Fatalf("create: %d", code)
	}
	if op := e.WaitOperation(c.Operation.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("create: %s\n%s", op.Status, testenv.FormatLog(op))
	}
	rack(ns[1], "rack-a")
	rack(ns[2], "rack-a")
	// etcd now shares a rack: the check and the alert say so.
	if p := problems(); len(p) != 1 || p[0].Group != gen.Etcd || len(p[0].Nodes) != 2 {
		t.Fatalf("problems with etcd in one rack: %+v", p)
	}
	if a := alert(); a["etcd"] != "firing" {
		t.Fatalf("alerts: %v", a)
	}
	haURL := "/api/v1/projects/" + c.Project.Id.String() + "/ha"
	var e2 gen.Error
	if code := e.Do("POST", haURL, gen.HAEnableRequest{}, &e2); code != http.StatusConflict || !strings.Contains(e2.Message, "failure domain") {
		t.Fatalf("HA with only same-rack nodes: %d %s", code, e2.Message)
	}
	if code := e.Do("POST", haURL, gen.HAEnableRequest{NodeId: &ns[1].Id}, &e2); code != http.StatusConflict || !strings.Contains(e2.Message, "failure domain") {
		t.Fatalf("HA on a named same-rack node: %d %s", code, e2.Message)
	}

	// 3. node-c moves to rack-b: the standby goes there, not to node-b.
	rack(ns[2], "rack-b")
	var op gen.Operation
	if code := e.Do("POST", haURL, gen.HAEnableRequest{}, &op); code != http.StatusAccepted {
		t.Fatalf("enable HA: %d", code)
	}
	if op = e.WaitOperation(op.Id); op.Status != gen.OperationStatusSucceeded {
		t.Fatalf("enable HA: %s %s\n%s", op.Status, deref(op.Error), testenv.FormatLog(op))
	}
	st := haStatus(t, e, c.Project.Id)
	for _, m := range st.Members {
		if m.NodeId == ns[1].Id {
			t.Fatalf("a member on node-b, in the primary's rack: %+v", st.Members)
		}
	}
	if len(st.Members) != 2 {
		t.Fatalf("members: %+v", st.Members)
	}

	// 4. The pair is fine; etcd still shares rack-a until node-b moves.
	rack(ns[1], "rack-c")
	if p := problems(); len(p) != 0 {
		t.Fatalf("problems after fixing the racks: %+v", p)
	}
	if a := alert(); a["etcd"] != "resolved" {
		t.Fatalf("alerts after fixing: %v", a)
	}
	// Moving the standby's node into the primary's rack is reported, for
	// the pair and for etcd (node-c holds a member too).
	rack(ns[2], "rack-a")
	groups := map[gen.FailureDomainProblemGroup]gen.FailureDomainProblem{}
	for _, p := range problems() {
		groups[p.Group] = p
	}
	if p, ok := groups[gen.HaPair]; len(groups) != 2 || !ok || p.ProjectId == nil || *p.ProjectId != c.Project.Id {
		t.Fatalf("problems with the pair in one rack: %+v", groups)
	}
	if a := alert(); a["ha:"+strings.TrimPrefix(groups[gen.HaPair].Key, "ha:")] != "firing" {
		t.Fatalf("alerts with the pair in one rack: %v", a)
	}
	rack(ns[2], "rack-b")

	// 5. Pooler hosts: the second in the first's rack is warned about.
	pooler := gen.CreateNodeRequestRolePooler
	pr := "rack-p"
	var first, second gen.NodeCreated
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "edge-one", PrivateAddr: "10.9.0.1", Role: pooler, FailureDomain: &pr}, &first); code != http.StatusCreated || first.Warnings != nil {
		t.Fatalf("first pooler host: %d %+v", code, first.Warnings)
	}
	if code := e.Do("POST", "/api/v1/nodes", gen.CreateNodeRequest{Name: "edge-two", PrivateAddr: "10.9.0.2", Role: pooler, FailureDomain: &pr}, &second); code != http.StatusCreated ||
		second.Warnings == nil || len(*second.Warnings) != 1 || !strings.Contains((*second.Warnings)[0], "edge-one") {
		t.Fatalf("second pooler host: %d %+v", code, second.Warnings)
	}
	for _, n := range []gen.NodeCreated{first, second} {
		e.Do("DELETE", "/api/v1/nodes/"+n.Node.Id.String(), nil, nil)
	}
}
