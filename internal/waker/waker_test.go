package waker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/waker"
)

type fakeWaker struct {
	mu    sync.Mutex
	woken []string
	state map[string]waker.State
}

func (f *fakeWaker) Wake(_ context.Context, db string) (waker.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.woken = append(f.woken, db)
	return f.state[db], nil
}

func serve(t *testing.T, w waker.Waker) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := waker.New(w, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return ln.Addr().String()
}

func connect(t *testing.T, addr, db, sslmode string) *pgconn.PgError {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	host, port, _ := net.SplitHostPort(addr)
	conn, err := pgx.Connect(ctx, "postgres://u:p@"+host+":"+port+"/"+db+"?sslmode="+sslmode)
	if err == nil {
		_ = conn.Close(ctx)
		t.Fatal("connected to the waker")
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		t.Fatalf("not a server error: %v", err)
	}
	return pe
}

func TestWakerMessages(t *testing.T) {
	f := &fakeWaker{state: map[string]waker.State{"paused_db": waker.Resuming, "archived_db": waker.Restoring}}
	addr := serve(t, f)

	// TLS is declined and the client falls back (as PgBouncer's "prefer").
	pe := connect(t, addr, "paused_db", "prefer")
	if pe.Code != "08004" || pe.Message != waker.MsgResuming {
		t.Errorf("paused: %s %q", pe.Code, pe.Message)
	}
	pe = connect(t, addr, "archived_db", "disable")
	if pe.Code != "08004" || !strings.Contains(pe.Message, "restored from its archive") {
		t.Errorf("archived: %s %q", pe.Code, pe.Message)
	}
	pe = connect(t, addr, "nope", "disable")
	if pe.Code != "3D000" {
		t.Errorf("unknown: %s %q", pe.Code, pe.Message)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Join(f.woken, ",") != "paused_db,archived_db,nope" {
		t.Errorf("woken: %v", f.woken)
	}
}

func TestWakerRejectsJunk(t *testing.T) {
	addr := serve(t, &fakeWaker{})
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	// A length far over the limit: the waker hangs up without reading on.
	if _, err := c.Write([]byte{0x7f, 0xff, 0xff, 0xff, 0, 3, 0, 0}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if n, err := c.Read(buf); err == nil {
		t.Errorf("got %q after a bad startup packet", buf[:n])
	}
}
