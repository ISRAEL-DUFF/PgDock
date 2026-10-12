package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/regions"
	"github.com/israel-duff/pgdock/test/testenv"
)

// TestRegionReadiness: a hidden region opens only when its blocking launch
// checks pass (V4.1 §13): it needs a healthy node, and an etcd cluster, if
// it has one, of three members in three failure domains.
func TestRegionReadiness(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	ctx := context.Background()
	if _, err := e.Regions.Save(ctx, regions.Input{ID: "ng-ready", Name: "Ready test", Country: "NG", Hidden: true}); err != nil {
		t.Fatal(err)
	}
	open := func() (int, gen.Error) {
		var er gen.Error
		code := e.Do("PUT", "/api/v1/admin/regions/ng-ready", gen.AdminRegionRequest{Name: "Ready test", Country: ptr("NG"), Hidden: ptr(false)}, &er)
		return code, er
	}
	readiness := func() gen.RegionReadiness {
		t.Helper()
		var rd gen.RegionReadiness
		if code := e.Do("GET", "/api/v1/admin/regions/ng-ready/readiness", nil, &rd); code != http.StatusOK {
			t.Fatalf("readiness: %d", code)
		}
		return rd
	}
	failing := func(rd gen.RegionReadiness) []string {
		var out []string
		for _, c := range rd.Checks {
			if c.Blocking {
				out = append(out, c.Name)
			}
		}
		return out
	}
	status := func() string {
		var s string
		if err := e.DB.QueryRow(ctx, `SELECT status FROM regions WHERE id = 'ng-ready'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// No nodes: refused, and the region stays hidden.
	if rd := readiness(); rd.Ready || strings.Join(failing(rd), ",") != "nodes" {
		t.Fatalf("an empty region: %+v", rd)
	}
	if code, er := open(); code != http.StatusConflict || er.Code != "region_not_ready" || !strings.Contains(er.Message, "nodes") {
		t.Fatalf("opening an empty region: %d %+v", code, er)
	}
	if s := status(); s != "hidden" {
		t.Fatalf("status after a refused opening: %s", s)
	}

	// Three healthy dedicated nodes, two of them in one rack.
	var nodes []uuid.UUID
	for i, rack := range []string{"rack-a", "rack-a", "rack-b"} {
		var id uuid.UUID
		if err := e.DB.QueryRow(ctx, `INSERT INTO nodes (name, private_addr, role, agent_cert_fp, capacity, status, region, failure_domain)
			VALUES ($1, $2::inet, 'dedicated', 'fp', '{}', 'healthy', 'ng-ready', $3) RETURNING id`,
			fmt.Sprintf("ready-%d", i), fmt.Sprintf("10.99.0.%d", i+1), rack).Scan(&id); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, id)
	}
	// An etcd cluster of two is refused; of three in two racks, too.
	addMember := func(i int) {
		t.Helper()
		if _, err := e.DB.Exec(ctx, `INSERT INTO etcd_members (node_id, name, client_url, peer_url, region) VALUES ($1, $2, 'https://x:2379', 'https://x:2380', 'ng-ready')`,
			nodes[i], fmt.Sprintf("etcd-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	addMember(0)
	addMember(2)
	if rd := readiness(); strings.Join(failing(rd), ",") != "etcd" {
		t.Fatalf("two etcd members: %+v", rd)
	}
	addMember(1)
	if f := failing(readiness()); strings.Join(f, ",") != "etcd,failure domains" {
		t.Fatalf("three etcd members in two racks: %v", f)
	}
	if code, er := open(); code != http.StatusConflict || !strings.Contains(er.Message, "share a failure domain") {
		t.Fatalf("opening with etcd in two racks: %d %+v", code, er)
	}

	// The third rack: it opens. Warnings (pooler pair, copy target) don't block.
	if _, err := e.DB.Exec(ctx, `UPDATE nodes SET failure_domain = 'rack-c' WHERE id = $1`, nodes[1]); err != nil {
		t.Fatal(err)
	}
	rd := readiness()
	if !rd.Ready {
		t.Fatalf("three racks: %+v", rd)
	}
	warned := false
	for _, c := range rd.Checks {
		if c.Name == "pooler pair" && !c.Ok && !c.Blocking {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("no pooler-pair warning for a region without pooler hosts: %+v", rd.Checks)
	}
	if code, er := open(); code != http.StatusOK {
		t.Fatalf("opening a ready region: %d %+v", code, er)
	}
	if s := status(); s != "active" {
		t.Fatalf("status after opening: %s", s)
	}
	// An open region is saved without the checks (they gate opening only).
	if _, err := e.DB.Exec(ctx, `DELETE FROM etcd_members WHERE node_id = $1`, nodes[1]); err != nil {
		t.Fatal(err)
	}
	if code, er := open(); code != http.StatusOK {
		t.Fatalf("saving an open region: %d %+v", code, er)
	}
}
