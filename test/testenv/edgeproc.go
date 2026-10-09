package testenv

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

var (
	edgeBinOnce sync.Once
	edgeBin     string
	edgeBinErr  error
)

// EdgeProcess is pgdock-edge running as its own process, for failure
// injection: Kill ends it with SIGKILL, as a crash would, mid-request.
type EdgeProcess struct {
	URL  string
	cmd  *exec.Cmd
	logs *lockedBuffer
	done chan struct{}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// StartEdgeProcess builds pgdock-edge (once per test binary) and runs it
// against this environment, waiting until it serves.
func (e *Env) StartEdgeProcess(name string) *EdgeProcess {
	e.t.Helper()
	edgeBinOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(file), "..", "..")
		edgeBin = filepath.Join(root, "tmp", "bin", "pgdock-edge-test")
		cmd := exec.Command("go", "build", "-o", edgeBin, "./cmd/edge")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			edgeBinErr = fmt.Errorf("build pgdock-edge: %w\n%s", err, out)
		}
	})
	if edgeBinErr != nil {
		e.t.Fatal(edgeBinErr)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	p := &EdgeProcess{URL: "http://" + addr, logs: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd = exec.Command(edgeBin)
	p.cmd.Env = append(os.Environ(), "PGDOCK_EDGE_NAME="+name, "PGDOCK_EDGE_CONTROL_URL="+e.URL, "PGDOCK_EDGE_SECRET="+EdgeSecret,
		"PGDOCK_EDGE_DOMAIN="+EdgeDomain, "PGDOCK_EDGE_LISTEN="+addr, "PGDOCK_EDGE_POOLER_SSLMODE=require", "PGDOCK_EDGE_LOG_FORMAT=text")
	p.cmd.Stdout, p.cmd.Stderr = p.logs, p.logs
	if err := p.cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	e.t.Cleanup(p.Kill)
	deadline := time.Now().Add(30 * time.Second)
	for {
		res, err := http.Get(p.URL + "/healthz")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return p
			}
		}
		select {
		case <-p.done:
			e.t.Fatalf("pgdock-edge exited:\n%s", p.logs)
		default:
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("pgdock-edge never became ready:\n%s", p.logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Kill ends the process at once (SIGKILL) and waits for it.
func (p *EdgeProcess) Kill() {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGKILL)
	}
	<-p.done
}

// Logs is what it wrote.
func (p *EdgeProcess) Logs() string { return p.logs.String() }
