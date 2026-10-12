package statuspage

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/statusapi"
)

func TestSLAProbes(t *testing.T) {
	svc, _, _, ts := newTestService(t, "")
	svc.Now = time.Now // the client signs with the real clock
	var down atomic.Bool
	svc.Probe = func(_ context.Context, p Probe) error {
		if p.Kind != "postgres" || p.Query != "SELECT 1" {
			t.Errorf("probe %+v", p)
		}
		if strings.Contains(p.DSN, "flaky") && down.Load() {
			return errors.New("connection refused")
		}
		return nil
	}
	ctx := context.Background()
	c := &statusapi.Client{URL: ts.URL, Secret: secret}
	if err := c.PutSLATargets(ctx, statusapi.SLATargets{Targets: []statusapi.SLATarget{
		{ID: "steady", DSN: "postgresql://p_a_sla:pw@db.example.com:6543/p_a"},
		{ID: "flaky", DSN: "postgresql://p_b_sla:pw@db.example.com:6543/flaky"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := (&statusapi.Client{URL: ts.URL, Secret: "wrong"}).PutSLATargets(ctx, statusapi.SLATargets{}); err == nil {
		t.Fatal("unsigned targets accepted")
	}
	if err := svc.SLATick(ctx); err != nil {
		t.Fatal(err)
	}
	down.Store(true)
	if err := svc.SLATick(ctx); err != nil { // same minute: a failure wins
		t.Fatal(err)
	}
	res, err := c.SLAResults(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range res.Results {
		got[r.ID] = r.OK
		if !r.Minute.Equal(r.Minute.Truncate(time.Minute)) {
			t.Errorf("minute %s", r.Minute)
		}
	}
	if len(res.Results) < 2 || !got["steady"] || got["flaky"] {
		t.Fatalf("results: %+v", res.Results)
	}
	// Replacing the targets stops probing the old ones.
	if err := c.PutSLATargets(ctx, statusapi.SLATargets{}); err != nil {
		t.Fatal(err)
	}
	if ts, err := svc.st.slaTargets(ctx); err != nil || len(ts) != 0 {
		t.Fatalf("targets after replacing: %v %v", ts, err)
	}
}
