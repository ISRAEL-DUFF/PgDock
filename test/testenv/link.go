package testenv

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Link is a TCP proxy a test can cut, to simulate losing a network path
// (e.g. agents to S3) and restore it.
type Link struct {
	Addr   string
	target string
	ln     net.Listener
	cut    atomic.Bool
	bytes  atomic.Int64
	stall  atomic.Int64 // forward no more once bytes reach it (0: off)
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
}

// NewLink listens on listenAddr ("host:0") and forwards to target.
func NewLink(t testing.TB, listenAddr, target string) *Link {
	t.Helper()
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		t.Fatal(err)
	}
	l := &Link{Addr: ln.Addr().String(), target: target, ln: ln, conns: map[net.Conn]struct{}{}}
	go l.accept()
	t.Cleanup(func() { _ = ln.Close(); l.Cut() })
	return l
}

func (l *Link) accept() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			return
		}
		if l.cut.Load() {
			_ = c.Close()
			continue
		}
		up, err := net.Dial("tcp", l.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		l.track(c, up)
		go l.pipe(c, up)
		go l.pipe(up, c)
	}
}

func (l *Link) track(cs ...net.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range cs {
		l.conns[c] = struct{}{}
	}
}

func (l *Link) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			for at := l.stall.Load(); at > 0 && l.bytes.Load() >= at && !l.cut.Load(); at = l.stall.Load() {
				time.Sleep(10 * time.Millisecond)
			}
			if l.cut.Load() {
				break
			}
			l.bytes.Add(int64(n))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break // io.EOF or a closed connection
		}
	}
	_ = dst.Close()
	_ = src.Close()
}

// Cut drops every connection and refuses new ones until Restore.
func (l *Link) Cut() {
	l.cut.Store(true)
	l.mu.Lock()
	defer l.mu.Unlock()
	for c := range l.conns {
		_ = c.Close()
	}
	l.conns = map[net.Conn]struct{}{}
}

// Restore lets connections through again, at full speed.
func (l *Link) Restore() { l.stall.Store(0); l.cut.Store(false) }

// StallAfter stops forwarding once n more bytes have crossed the link, as
// if the path hung, until Cut or Restore.
func (l *Link) StallAfter(n int64) { l.stall.Store(l.bytes.Load() + n) }

// Bytes is how much has crossed the link.
func (l *Link) Bytes() int64 { return l.bytes.Load() }
