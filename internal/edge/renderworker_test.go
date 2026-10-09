package edge

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/files"
)

// TestMain lets the test binary be a render worker: the pool tests start
// it again with renderWorkerTestEnv set.
const renderWorkerTestEnv = "PGDOCK_TEST_RENDER_WORKER"

func TestMain(m *testing.M) {
	if os.Getenv(renderWorkerTestEnv) == "1" {
		if err := RunRenderWorker(os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testPNG(t *testing.T, w, h int, extra []byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return append(b.Bytes(), extra...) // trailing bytes after IEND are ignored by decoders
}

func TestRenderPool(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// 512 MB of heap and 16 times that of address space, as pgdock-edge sets.
	pool := newRenderPool([]string{self}, []string{renderWorkerTestEnv + "=1", "GOMEMLIMIT=512MiB",
		RenderWorkerASEnv + "=" + "8589934592", RenderWorkerCrashEnv + "=CRASH-ME-NOW"}, 2, log)
	defer pool.close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	src := testPNG(t, 300, 200, nil)

	// Every format, in a worker under its caps (WebP and AVIF are WebAssembly).
	for _, f := range []string{"png", "jpeg", "webp", "avif"} {
		out, err := pool.render(ctx, src, files.Transform{Width: 100, Height: 100, Resize: "cover"}, f)
		if err != nil || len(out) == 0 {
			t.Fatalf("%s: %d bytes, %v", f, len(out), err)
		}
		if f == "png" {
			img, err := png.Decode(bytes.NewReader(out))
			if err != nil || img.Bounds().Dx() != 100 || img.Bounds().Dy() != 100 {
				t.Fatalf("png out: %v %v", img.Bounds(), err)
			}
		}
	}
	// A client error comes back as one, and the worker carries on.
	if _, err := pool.render(ctx, []byte("not an image"), files.Transform{Width: 10}, "png"); err == nil {
		t.Fatal("garbage rendered")
	} else {
		var a *apiErr
		if !errors.As(err, &a) || a.code != "invalid_image" || a.status != 400 {
			t.Fatalf("garbage: %v", err)
		}
	}
	before := pool.Started()

	// A crashing render: transform_failed, and the next render on a new
	// worker works, while others run beside it.
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = pool.render(ctx, src, files.Transform{Width: 50}, "png")
		}()
	}
	_, err = pool.render(ctx, testPNG(t, 20, 20, []byte("CRASH-ME-NOW")), files.Transform{Width: 10}, "png")
	var a *apiErr
	if !errors.As(err, &a) || a.code != "transform_failed" || a.status != 500 {
		t.Fatalf("crash: %v", err)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("render %d beside the crash: %v", i, err)
		}
	}
	// Both slots at once: the crashed one's starts a new worker.
	after := make([]error, 2)
	for i := range after {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, after[i] = pool.render(ctx, src, files.Transform{Width: 30}, "png")
		}()
	}
	wg.Wait()
	for i, err := range after {
		if err != nil {
			t.Fatalf("render %d after the crash: %v", i, err)
		}
	}
	if pool.Started() <= before {
		t.Fatalf("no new worker after the crash: %d then %d", before, pool.Started())
	}
}
