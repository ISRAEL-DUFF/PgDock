package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/israel-duff/pgdock/internal/api/gen"
	"github.com/israel-duff/pgdock/internal/jobs"
	"github.com/israel-duff/pgdock/internal/store/storetest"
)

func newDBServer(t *testing.T, dev bool) *httptest.Server {
	t.Helper()
	pool := storetest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	n := jobs.NewNotifier(pool, log)
	r := jobs.NewRunner(pool, n, log, jobs.RunnerConfig{PollInterval: 50 * time.Millisecond}, map[string]jobs.Kind{jobs.KindNoop: jobs.Noop()})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); n.Run(ctx) }()
	go func() { defer wg.Done(); r.Run(ctx) }()

	ts := httptest.NewServer(NewHandler(Options{
		Logger: log, DB: pool, Notifier: n, DevEndpoints: dev, StreamCtx: ctx,
		UI: fstest.MapFS{"index.html": {Data: []byte("app")}}, UIIndex: "index.html",
	}))
	t.Cleanup(func() { ts.Close(); cancel(); wg.Wait() })
	return ts
}

func post(t *testing.T, ts *httptest.Server, path, body string) (response, string) {
	t.Helper()
	res, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return response{StatusCode: res.StatusCode, Header: res.Header}, string(b)
}

func TestDevEndpointDisabledByDefault(t *testing.T) {
	ts := newDBServer(t, false)
	if res, _ := post(t, ts, "/api/v1/dev/operations", `{}`); res.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d", res.StatusCode)
	}
}

func TestOperationLifecycleOverAPI(t *testing.T) {
	ts := newDBServer(t, true)

	if res, body := get(t, ts, "/readyz"); res.StatusCode != http.StatusOK {
		t.Fatalf("readyz: %d %s", res.StatusCode, body)
	}
	if res, _ := post(t, ts, "/api/v1/dev/operations", `{"steps": 0}`); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid params: got %d", res.StatusCode)
	}

	res, body := post(t, ts, "/api/v1/dev/operations", `{"steps": 2, "delay_ms": 20}`)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	var op gen.Operation
	if err := json.Unmarshal([]byte(body), &op); err != nil {
		t.Fatal(err)
	}
	if op.Kind != jobs.KindNoop || op.Status != gen.Queued || res.Header.Get("Location") != "/api/v1/operations/"+op.Id.String() {
		t.Fatalf("unexpected operation: %+v", op)
	}

	// Stream to completion.
	sres, err := http.Get(ts.URL + "/api/v1/operations/" + op.Id.String() + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	var sawDone bool
	sc := bufio.NewScanner(sres.Body)
	for sc.Scan() {
		if sc.Text() == "event: done" {
			sawDone = true
		}
	}
	_ = sres.Body.Close()
	if !sawDone {
		t.Fatal("stream ended without a done event")
	}

	res, body = get(t, ts, "/api/v1/operations/"+op.Id.String())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get: %d %s", res.StatusCode, body)
	}
	if err := json.Unmarshal([]byte(body), &op); err != nil {
		t.Fatal(err)
	}
	if op.Status != gen.Succeeded || len(op.Log) != 3 || op.Params["steps"] != float64(2) {
		t.Fatalf("unexpected final operation: %+v", op)
	}

	res, body = get(t, ts, "/api/v1/operations?status=succeeded&kind=noop")
	var list gen.OperationList
	if err := json.Unmarshal([]byte(body), &list); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", res.StatusCode, body)
	}
	if len(list.Items) != 1 || list.Items[0].Id != op.Id {
		t.Fatalf("unexpected list: %+v", list)
	}

	for _, p := range []string{"/api/v1/operations?status=bogus", "/api/v1/operations?limit=0", "/api/v1/operations/not-a-uuid"} {
		if res, _ := get(t, ts, p); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, res.StatusCode)
		}
	}
	if res, _ := get(t, ts, "/api/v1/operations/00000000-0000-0000-0000-000000000000"); res.StatusCode != http.StatusNotFound {
		t.Errorf("missing operation: got %d", res.StatusCode)
	}
}
