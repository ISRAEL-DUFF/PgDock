package edge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/israel-duff/pgdock/internal/files"
)

// Render workers (V4.1 §12.4): image transforms run in child processes,
// `pgdock-edge render-worker`, each rendering one image at a time over its
// stdin and stdout. A decoder that panics or runs out of memory ends that
// worker; the request gets 500 transform_failed and the next one starts a
// new worker, while the edge carries on.

// RenderWorkerCrashEnv names a byte string that makes a worker crash when
// an image contains it: a test seam for the crash path, unset in service.
const RenderWorkerCrashEnv = "PGDOCK_RENDER_CRASH_MARKER"

// Frame limits: a request is a source image (at most maxSourceBytes) and a
// header; a reply is an encoded image or an error.
const (
	maxFrame       = maxSourceBytes + 1<<20
	maxHeaderFrame = 64 << 10
)

type renderRequest struct {
	Transform files.Transform `json:"transform"`
	Format    string          `json:"format"`
}

type renderReply struct {
	Status  int    `json:"status,omitempty"` // 0: ok
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func writeFrame(w io.Writer, b []byte) error {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readFrame(r io.Reader, limit int) ([]byte, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(n[:]))
	if size > limit {
		return nil, fmt.Errorf("render frame of %d bytes is over %d", size, limit)
	}
	b := make([]byte, size)
	_, err := io.ReadFull(r, b)
	return b, err
}

// RunRenderWorker serves render requests on in, replying on out, until in
// closes (`pgdock-edge render-worker`). It doesn't recover a panic: a
// crashing decoder ends the process, which is the point.
func RunRenderWorker(in io.Reader, out io.Writer) error {
	limitMemory()
	marker := []byte(os.Getenv(RenderWorkerCrashEnv))
	r, w := bufio.NewReader(in), bufio.NewWriter(out)
	for {
		head, err := readFrame(r, maxHeaderFrame)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		data, err := readFrame(r, maxFrame)
		if err != nil {
			return err
		}
		var req renderRequest
		if err := json.Unmarshal(head, &req); err != nil {
			return fmt.Errorf("render request: %w", err)
		}
		if len(marker) > 0 && bytes.Contains(data, marker) {
			panic("render-worker: the test crash marker")
		}
		img, err := renderBytes(data, req.Transform, req.Format)
		var reply renderReply
		var a *apiErr
		switch {
		case errors.As(err, &a):
			reply = renderReply{Status: a.status, Code: a.code, Message: a.msg}
		case err != nil:
			reply = renderReply{Status: http.StatusInternalServerError, Code: "transform_failed", Message: err.Error()}
		}
		rh, _ := json.Marshal(reply)
		if err := writeFrame(w, rh); err != nil {
			return err
		}
		if err := writeFrame(w, img); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
}

// errTransformFailed answers a render whose worker died.
var errTransformFailed = refuse(http.StatusInternalServerError, "transform_failed", "the image couldn't be transformed")

// renderProc is one running worker.
type renderProc struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func (p *renderProc) kill() {
	_ = p.in.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
}

// renderPool is the edge's workers: a slot per concurrent render, each
// holding a worker started on first use and replaced when it dies.
type renderPool struct {
	command []string
	env     []string
	log     *slog.Logger
	slots   chan *renderProc // nil: not started (or died)
	mu      sync.Mutex
	started int // workers started, for the logs and tests
}

func newRenderPool(command, env []string, size int, log *slog.Logger) *renderPool {
	p := &renderPool{command: command, env: env, log: log, slots: make(chan *renderProc, size)}
	for range size {
		p.slots <- nil
	}
	return p
}

func (p *renderPool) start() (*renderProc, error) {
	cmd := exec.Command(p.command[0], p.command[1:]...)
	cmd.Env = append(os.Environ(), p.env...)
	cmd.Stderr = &logWriter{log: p.log}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start a render worker: %w", err)
	}
	p.mu.Lock()
	p.started++
	p.mu.Unlock()
	return &renderProc{cmd: cmd, in: in, out: bufio.NewReader(out)}, nil
}

// Started is how many workers the pool has started.
func (p *renderPool) Started() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started
}

// render runs one render on a worker; a worker that dies or overruns ctx
// is killed and its slot starts a new one next time.
func (p *renderPool) render(ctx context.Context, data []byte, t files.Transform, format string) ([]byte, error) {
	var proc *renderProc
	select {
	case proc = <-p.slots:
	case <-ctx.Done():
		return nil, refuse(http.StatusServiceUnavailable, "busy", "too many images are being transformed; retry shortly")
	}
	healthy := false
	defer func() {
		if !healthy && proc != nil {
			proc.kill()
			proc = nil
		}
		p.slots <- proc
	}()
	if proc == nil {
		var err error
		if proc, err = p.start(); err != nil {
			p.log.Error("render worker", "err", err)
			return nil, errTransformFailed
		}
	}
	type result struct {
		img   []byte
		reply renderReply
		err   error
	}
	done := make(chan result, 1)
	go func() {
		head, _ := json.Marshal(renderRequest{Transform: t, Format: format})
		if err := writeFrame(proc.in, head); err != nil {
			done <- result{err: err}
			return
		}
		if err := writeFrame(proc.in, data); err != nil {
			done <- result{err: err}
			return
		}
		rh, err := readFrame(proc.out, maxHeaderFrame)
		if err != nil {
			done <- result{err: err}
			return
		}
		img, err := readFrame(proc.out, maxFrame)
		var reply renderReply
		if err == nil {
			err = json.Unmarshal(rh, &reply)
		}
		done <- result{img: img, reply: reply, err: err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			p.log.Warn("render worker died", "err", res.err)
			return nil, errTransformFailed
		}
		healthy = true
		if res.reply.Status != 0 {
			return nil, refuse(res.reply.Status, res.reply.Code, res.reply.Message)
		}
		return res.img, nil
	case <-ctx.Done():
		return nil, refuse(http.StatusGatewayTimeout, "transform_timeout", "the image took too long to transform")
	}
}

// close stops the idle workers.
func (p *renderPool) close() {
	for range cap(p.slots) {
		select {
		case proc := <-p.slots:
			if proc != nil {
				proc.kill()
			}
		case <-time.After(10 * time.Second):
			return
		}
	}
}

// logWriter logs a worker's stderr (a panic's trace, say) line by line.
type logWriter struct {
	log *slog.Logger
	buf []byte
}

func (l *logWriter) Write(b []byte) (int, error) {
	l.buf = append(l.buf, b...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.log.Warn("render worker", "stderr", string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 64<<10 {
		l.buf = l.buf[:0]
	}
	return len(b), nil
}
