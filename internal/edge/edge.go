// Package edge is pgdock-edge (V4 §2.1): the per-region gateway that
// serves every project's backend services at https://<ref>.<domain>. It
// keeps an in-memory copy of each project's configuration, followed from
// pgdock-server's feed, resolves exactly one project per request from the
// hostname, checks the API key (and a user's token) against that project
// only, and runs the request in one transaction through the edge pooler,
// as the project's anon, user or service role with the caller's claims
// set locally. It meters requests and sends request logs back.
package edge

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/edgeapi"
	"github.com/israel-duff/pgdock/internal/jwtes"
)

// Config is pgdock-edge's configuration.
type Config struct {
	// Name identifies this edge in reports (its hostname).
	Name string
	// ControlURL and Secret reach pgdock-server's edge feed.
	ControlURL string
	Secret     string
	// Region limits the edge to one region's projects ("" for all).
	Region string
	// Domain is the hostname suffix: <ref>.<Domain>.
	Domain string
	// PoolerAddr overrides the pooler address the feed gives (host:port),
	// for an edge that reaches the pooler another way (the mesh).
	PoolerAddr string
	// PoolerSSLMode is the sslmode to the pooler (default "require").
	PoolerSSLMode string
	// TrustedProxies may set X-Forwarded-For.
	TrustedProxies []netip.Prefix
	// PollWait, ResyncEvery and ReportEvery time the feed and reports.
	PollWait    time.Duration
	ResyncEvery time.Duration
	ReportEvery time.Duration
	Log         *slog.Logger
}

func (c *Config) defaults() {
	if c.PollWait == 0 {
		c.PollWait = 25 * time.Second
	}
	if c.ResyncEvery == 0 {
		c.ResyncEvery = time.Minute
	}
	if c.ReportEvery == 0 {
		c.ReportEvery = 10 * time.Second
	}
	if c.PoolerSSLMode == "" {
		c.PoolerSSLMode = "require"
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	c.Domain = strings.ToLower(strings.Trim(c.Domain, "."))
}

// Edge is a running gateway.
type Edge struct {
	cfg    Config
	client *edgeapi.Client

	mu     sync.RWMutex
	byRef  map[string]*project
	seq    int64
	ready  bool
	meter  *meter
	limits *limiter
	waking sync.Map // ref -> time.Time of the last wake asked
}

// project is one project's configuration and its database pool.
type project struct {
	cfg  edgeapi.Project
	keys map[string]edgeapi.Key // by hash
	jwks map[string]*ecdsa.PublicKey
	// db is shared with the next copy of the configuration while how it
	// connects doesn't change; catalog always is.
	db      *dbconn
	catalog *catalogState
}

// exposed are the schemas the data API serves.
func (p *project) exposed() []string {
	if len(p.cfg.ExposedSchemas) == 0 {
		return []string{"public"}
	}
	return p.cfg.ExposedSchemas
}

// publicTable reports whether t may be read without row-level security.
func (p *project) publicTable(t *Table) bool {
	for _, n := range p.cfg.PublicTables {
		if n == t.Schema+"."+t.Name || (t.Schema == "public" && n == t.Name) {
			return true
		}
	}
	return false
}

type dbconn struct {
	mu     sync.Mutex
	pool   *pgxpool.Pool
	closed bool
}

func (d *dbconn) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.pool != nil {
		d.pool.Close()
		d.pool = nil
	}
}

// New returns an edge; Run starts following the feed and reporting.
func New(cfg Config) *Edge {
	cfg.defaults()
	return &Edge{
		cfg:    cfg,
		client: &edgeapi.Client{URL: cfg.ControlURL, Secret: cfg.Secret},
		byRef:  map[string]*project{},
		meter:  newMeter(),
		limits: newLimiter(),
	}
}

// Run follows the configuration feed and sends reports until ctx ends.
func (e *Edge) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e.follow(ctx) }()
	go func() { defer wg.Done(); e.report(ctx) }()
	wg.Wait()
	e.closePools()
	e.flush(context.Background())
}

// Ready reports whether the first full configuration has loaded.
func (e *Edge) Ready() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ready
}

func (e *Edge) follow(ctx context.Context) {
	backoff := time.Second
	lastFull := time.Time{}
	for ctx.Err() == nil {
		var err error
		if time.Since(lastFull) >= e.cfg.ResyncEvery {
			if err = e.resync(ctx); err == nil {
				lastFull = time.Now()
			}
		} else {
			err = e.poll(ctx)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.cfg.Log.Warn("edge configuration feed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(2*backoff, 30*time.Second)
			continue
		}
		backoff = time.Second
	}
}

// resync reads the whole feed from the start: the backstop for any change
// an incremental poll could have missed.
func (e *Edge) resync(ctx context.Context) error {
	var since int64
	for {
		c, err := e.client.Config(ctx, e.cfg.Region, since, 0)
		if err != nil {
			return err
		}
		e.apply(c.Projects)
		since = c.Next
		if !c.More {
			break
		}
	}
	e.mu.Lock()
	if since > e.seq {
		e.seq = since
	}
	e.ready = true
	e.mu.Unlock()
	return nil
}

func (e *Edge) poll(ctx context.Context) error {
	e.mu.RLock()
	since := e.seq
	e.mu.RUnlock()
	c, err := e.client.Config(ctx, e.cfg.Region, since, e.cfg.PollWait)
	if err != nil {
		return err
	}
	e.apply(c.Projects)
	e.mu.Lock()
	if c.Next > e.seq {
		e.seq = c.Next
	}
	e.mu.Unlock()
	return nil
}

// apply updates the cache. A project's pool is kept unless how it connects
// changed.
func (e *Edge) apply(ps []edgeapi.Project) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, pc := range ps {
		old := e.byRef[pc.Ref]
		if old != nil && old.cfg.Version > pc.Version {
			continue // an older copy (from a resync racing a poll)
		}
		if pc.State == edgeapi.StateDisabled {
			if old != nil {
				delete(e.byRef, pc.Ref)
				go old.close()
			}
			continue
		}
		np := &project{cfg: pc, keys: map[string]edgeapi.Key{}, jwks: map[string]*ecdsa.PublicKey{}, db: &dbconn{},
			catalog: &catalogState{}}
		for _, k := range pc.Keys {
			np.keys[k.Hash] = k
		}
		for _, raw := range pc.JWKs {
			var j jwtes.JWK
			if json.Unmarshal(raw, &j) != nil {
				continue
			}
			if pub, err := j.Public(); err == nil {
				np.jwks[j.Kid] = pub
			}
		}
		if old != nil {
			np.catalog = old.catalog
			if sameConn(old.cfg, pc) {
				np.db = old.db
			} else {
				go old.close()
			}
		}
		e.byRef[pc.Ref] = np
	}
}

func sameConn(a, b edgeapi.Project) bool {
	return a.Database == b.Database && a.EdgeUser == b.EdgeUser && a.Password == b.Password &&
		a.PoolerHost == b.PoolerHost && a.PoolerPort == b.PoolerPort
}

func (p *project) close() { p.db.close() }

func (e *Edge) closePools() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.byRef {
		p.close()
	}
}

// lookup is the project ref names, or nil.
func (e *Edge) lookup(ref string) *project {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.byRef[ref]
}

// dbPool is the project's pool, connecting as its edge login through the
// pooler in transaction mode.
func (e *Edge) dbPool(ctx context.Context, p *project) (*pgxpool.Pool, error) {
	d := p.db
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errConfigChanged
	}
	if d.pool != nil {
		return d.pool, nil
	}
	host, port := p.cfg.PoolerHost, p.cfg.PoolerPort
	if e.cfg.PoolerAddr != "" {
		h, ps, err := net.SplitHostPort(e.cfg.PoolerAddr)
		if err != nil {
			return nil, err
		}
		host = h
		port, _ = strconv.Atoi(ps)
	}
	if host == "" || port == 0 {
		return nil, errors.New("no pooler address for the project's region")
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(p.cfg.EdgeUser, p.cfg.Password),
		Host: net.JoinHostPort(host, strconv.Itoa(port)), Path: "/" + p.cfg.Database,
		RawQuery: url.Values{"sslmode": {e.cfg.PoolerSSLMode}, "application_name": {"pgdock-edge"}}.Encode()}
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, err
	}
	// Transaction pooling: no server-side prepared statements kept across
	// transactions.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.MaxConns, cfg.MinConns = 4, 0
	cfg.MaxConnIdleTime, cfg.MaxConnLifetime = time.Minute, 30*time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	d.pool = pool
	return pool, nil
}

// errConfigChanged: the project's connection settings changed while a
// request was starting; it is retried by the client.
var errConfigChanged = errors.New("the project's configuration changed; retry")

// Request is who is asking: the role, the claims set for the request, and
// the statement timeout.
type Request struct {
	Role    string         // anon | user | service
	Claims  map[string]any // includes "role"
	Timeout time.Duration
}

// dbRole is the project role Role maps to.
func (p *project) dbRole(role string) (string, error) {
	switch role {
	case "anon":
		return p.cfg.AnonRole, nil
	case "user":
		return p.cfg.UserRole, nil
	case "service":
		return p.cfg.ServiceRole, nil
	}
	return "", fmt.Errorf("unknown role %q", role)
}

// WithRequest runs fn in one transaction as req's role with its claims set
// locally (V4 §1.4 "One request, one transaction"), so it works through the
// pooler in transaction mode: nothing outlives the transaction.
func (e *Edge) WithRequest(ctx context.Context, p *project, req Request, fn func(pgx.Tx) error) error {
	pool, err := e.dbPool(ctx, p)
	if err != nil {
		return err
	}
	role, err := p.dbRole(req.Role)
	if err != nil {
		return err
	}
	claims, err := json.Marshal(req.Claims)
	if err != nil {
		return err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('pgd.claims', $1, true), set_config('statement_timeout', $2, true)`,
			string(claims), strconv.FormatInt(timeout.Milliseconds(), 10)); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Explore serves one data API request for the project pc as role with
// claims, without a key or the rate limits: pgdock-server's request
// explorer. The project's pool is kept between calls.
func (e *Edge) Explore(ctx context.Context, pc edgeapi.Project, role string, claims map[string]any, method, path string, body []byte) (int, http.Header, []byte) {
	e.apply([]edgeapi.Project{pc})
	p := e.lookup(pc.Ref)
	rec := httptest.NewRecorder()
	if p == nil {
		rec.WriteHeader(http.StatusNotFound)
		return rec.Code, rec.Header(), nil
	}
	r := httptest.NewRequestWithContext(ctx, method, "http://"+pc.Ref+".explorer"+path, bytes.NewReader(body))
	if len(body) > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	c := &call{id: newRequestID(), start: time.Now(), w: &recorder{ResponseWriter: rec}, r: r, p: p, role: role}
	e.route(c, Request{Role: role, Claims: claims, Timeout: time.Duration(p.cfg.Settings.StatementTimeoutMs) * time.Millisecond})
	return rec.Code, rec.Header(), rec.Body.Bytes()
}
