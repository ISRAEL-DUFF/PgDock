package testenv

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/israel-duff/pgdock/internal/edge"
)

// The test environment's API domain and edge secret.
const (
	EdgeDomain = "api.pgdock.test"
	EdgeSecret = "test-edge-secret-0123456789abcdefghij"
)

// Edge is an in-process pgdock-edge serving EdgeDomain.
type Edge struct {
	*edge.Edge
	URL string // its listener; requests carry the project's Host
	t   testing.TB
}

// StartEdge runs pgdock-edge against this environment's server and
// poolers, and waits for its first configuration.
func (e *Env) StartEdge(opts ...func(*edge.Config)) *Edge {
	e.t.Helper()
	cfg := edge.Config{Name: "edge-test", ControlURL: e.URL, Secret: EdgeSecret, Domain: EdgeDomain,
		PoolerSSLMode: "require", PollWait: 2 * time.Second, ResyncEvery: time.Hour, ReportEvery: time.Hour, AuthRateScale: 20}
	for _, o := range opts {
		o(&cfg)
	}
	ed := edge.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); ed.Run(ctx) }()
	srv := httptest.NewServer(ed)
	e.t.Cleanup(func() {
		srv.Close()
		cancel()
		<-done
	})
	deadline := time.Now().Add(30 * time.Second)
	for !ed.Ready() {
		if time.Now().After(deadline) {
			e.t.Fatal("pgdock-edge never loaded its configuration")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &Edge{Edge: ed, URL: srv.URL, t: e.t}
}

// Do sends a request to project ref's API: method, path, headers (pairs),
// and returns the status and body.
func (x *Edge) Do(ref, method, path string, body io.Reader, headers ...string) (int, http.Header, string) {
	x.t.Helper()
	req, err := http.NewRequest(method, x.URL+path, body)
	if err != nil {
		x.t.Fatal(err)
	}
	req.Host = ref + "." + EdgeDomain
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := noRedirects.Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, strings.TrimSpace(string(b))
}

// noRedirects returns a redirect as it is (an auth link's 303).
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
