package integration

import (
	"bytes"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/edge"
)

// TestRenderWorkerCrash is V4.1-M11's render isolation check (V4.1 §12.4):
// an image that crashes the decoder's worker answers 500 transform_failed,
// while the edge, and other renders on it, keep working.
func TestRenderWorkerCrash(t *testing.T) {
	sp := newStorageProject(t, "gallery", false)
	e := sp.e
	// A second edge rendering in workers (pgdock-edge render-worker), with the
	// test's crash marker: a worker that sees it panics, as a broken decoder would.
	const marker = "PGDOCK-CRASH-THE-DECODER"
	ed := e.StartEdge(func(c *edge.Config) {
		c.Name = "edge-render"
		c.RenderWorker = []string{e.EdgeBinary(), "render-worker"}
		c.RenderWorkerEnv = []string{"GOMEMLIMIT=256MiB", edge.RenderWorkerASEnv + "=4294967296", edge.RenderWorkerCrashEnv + "=" + marker}
	})
	admin := files{t: t, ed: ed, ref: sp.ref, key: sp.admin.key}
	waitFor(t, 30*time.Second, "the render edge", func() bool {
		return admin.do("GET", "/data/v1/health", nil, "").Code == http.StatusOK
	})
	if r := admin.json("POST", "/storage/v1/bucket", `{"id":"pics","public":true}`, ""); r.Code != 200 && r.Code != 201 {
		t.Fatalf("bucket: %s", r)
	}
	upload := func(name string, body []byte) {
		t.Helper()
		if r := admin.do("POST", "/storage/v1/object/pics/"+name, bytes.NewReader(body), "", "Content-Type", "image/png"); r.Code != 200 {
			t.Fatalf("upload %s: %s", name, r)
		}
	}
	good := pngOf(t, 400, 300)
	upload("good.png", good)
	upload("bad.png", append(pngOf(t, 40, 40), []byte(marker)...))

	render := func(name, q string) fileResult {
		return admin.do("GET", "/storage/v1/render/pics/"+name+"?"+q, nil, "")
	}
	if r := render("good.png", "width=100&format=webp"); r.Code != 200 || r.Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("a render in a worker: %s", r)
	}
	started := ed.RenderWorkersStarted()
	if started == 0 {
		t.Fatal("the render ran in the edge, not a worker")
	}

	// The crash, with other renders and requests going on beside it.
	var wg sync.WaitGroup
	others := make([]fileResult, 6)
	for i := range others {
		wg.Add(1)
		go func() {
			defer wg.Done()
			others[i] = render("good.png", "width="+strconv.Itoa(60+i)+"&format=png")
		}()
	}
	if r := render("bad.png", "width=20&format=png"); r.Code != http.StatusInternalServerError || r.Error.Code != "transform_failed" {
		t.Fatalf("the crashing image: %s", r)
	}
	wg.Wait()
	for i, r := range others {
		if r.Code != 200 {
			t.Fatalf("render %d beside the crash: %s", i, r)
		}
	}
	if r := admin.do("GET", "/data/v1/health", nil, ""); r.Code != 200 {
		t.Fatalf("the edge after the crash: %s", r)
	}
	// It is tried again (a crash isn't cached) and fails the same way.
	if r := render("bad.png", "width=20&format=png"); r.Code != http.StatusInternalServerError {
		t.Fatalf("the crashing image again: %s", r)
	}
	if r := render("good.png", "width=33&format=jpeg"); r.Code != 200 || ed.RenderWorkersStarted() <= started {
		t.Fatalf("after the crashes: %s, workers started %d (was %d)", r, ed.RenderWorkersStarted(), started)
	}
}
