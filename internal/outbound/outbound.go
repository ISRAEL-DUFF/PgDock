// Package outbound is the only way tenants make PGDock send traffic
// (webhooks and HTTP jobs, V2 §9.1, §10.7): requests go only to public
// addresses unless the platform admin allow-listed the host for the
// organisation, connect to the address that was checked (no DNS
// rebinding), never follow redirects, are signed, rate limited per
// organisation, and counted per destination host.
package outbound

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/store"
)

var (
	// ErrRefused means the destination is not allowed.
	ErrRefused = errors.New("destination refused")
	// ErrDisabled means outbound traffic is off for the organisation
	// (V2 §10.7) or the organisation is not active.
	ErrDisabled = errors.New("outbound traffic is disabled for this organisation")
)

// SnippetBytes is how much of a response body is kept (V2 §9.1).
const SnippetBytes = 4 << 10

// blocked are the ranges no tenant request may reach unless allow-listed
// (RFC 1918, loopback, CGNAT, unique local, and other special ranges).
var blocked = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "172.16.0.0/12", "192.0.0.0/24",
	"192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "fc00::/7", "ff00::/8", "64:ff9b:1::/48",
)

// never are refused even for an allow-listed host: link-local, which holds
// cloud metadata endpoints (169.254.169.254, fd00:ec2::254 via fe80::).
var never = mustPrefixes("169.254.0.0/16", "fe80::/10", "fd00:ec2::254/128")

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

var (
	nat64  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4 = netip.MustParsePrefix("2002::/16")
)

// canon is the address a is checked as: IPv4 for IPv4-mapped, NAT64
// (64:ff9b::/96) and 6to4 (2002::/16) addresses, which reach the IPv4
// address they embed (64:ff9b::a9fe:a9fe is 169.254.169.254).
func canon(a netip.Addr) netip.Addr {
	a = a.Unmap()
	b := a.As16()
	switch {
	case nat64.Contains(a):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	case sixTo4.Contains(a):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})
	}
	return a
}

func in(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Config tunes the client.
type Config struct {
	// Blocked are extra ranges to refuse, such as the nodes' network.
	Blocked []netip.Prefix
	// Resolve looks a host up (default: the system resolver).
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Now is the clock (tests move it).
	Now func() time.Time
	// Allowed, when set, answers allow-list lookups instead of the
	// metadata DB (unit tests).
	Allowed func(orgID uuid.UUID, host string) bool
}

// Service sends tenants' requests.
type Service struct {
	db  *pgxpool.Pool
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	buckets map[string]*bucket
}

// New returns a Service.
func New(db *pgxpool.Pool, cfg Config, log *slog.Logger) *Service {
	if cfg.Resolve == nil {
		cfg.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{db: db, cfg: cfg, log: log, buckets: map[string]*bucket{}}
}

// Now is the service's clock.
func (s *Service) Now() time.Time { return s.cfg.Now() }

// Target is a checked destination.
type Target struct {
	URL  *url.URL
	Host string
	// Addr is the address to connect to, checked.
	Addr netip.AddrPort
}

// NormalizeHost lowercases a host for the allow-list.
func NormalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// ValidAllowHost checks an allow-list entry: a host name or an IP address,
// never a link-local or metadata address.
func ValidAllowHost(h string) error {
	h = NormalizeHost(h)
	if a, err := netip.ParseAddr(h); err == nil {
		if in(never, canon(a)) {
			return errors.New("link-local and cloud metadata addresses can't be allowed")
		}
		return nil
	}
	if h == "" || len(h) > 253 || strings.ContainsAny(h, "/:@?# ") {
		return errors.New("a host name or an IP address, without a scheme or port")
	}
	return nil
}

func (s *Service) allowed(ctx context.Context, orgID uuid.UUID, host string) (bool, error) {
	if s.cfg.Allowed != nil {
		return s.cfg.Allowed(orgID, NormalizeHost(host)), nil
	}
	return store.New(s.db).OutboundHostAllowed(ctx, store.OutboundHostAllowedParams{OrgID: orgID, Host: NormalizeHost(host)})
}

// Check validates rawURL for orgID: https (or http to an allow-listed
// host), a host that resolves only to public addresses (or is allow-listed),
// and never link-local or metadata addresses.
func (s *Service) Check(ctx context.Context, orgID uuid.UUID, rawURL string) (Target, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil {
		return Target{}, fmt.Errorf("%w: not an absolute URL", ErrRefused)
	}
	host := NormalizeHost(u.Hostname())
	if a, err := netip.ParseAddr(host); err == nil && in(never, canon(a)) {
		return Target{}, fmt.Errorf("%w: %s is a link-local or cloud metadata address", ErrRefused, a)
	}
	allow, err := s.allowed(ctx, orgID, host)
	if err != nil {
		return Target{}, err
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allow {
			return Target{}, fmt.Errorf("%w: plain http:// is only allowed to hosts the platform admin allow-listed; use https://", ErrRefused)
		}
	default:
		return Target{}, fmt.Errorf("%w: the URL must be https://", ErrRefused)
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Target{}, fmt.Errorf("%w: bad port", ErrRefused)
		}
		port = n
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err = s.cfg.Resolve(rctx, host)
		cancel()
		if err != nil || len(addrs) == 0 {
			return Target{}, fmt.Errorf("%w: %s does not resolve", ErrRefused, host)
		}
	}
	// Every address must pass, so a host can't mix a public address with
	// an internal one.
	for _, a := range addrs {
		a = canon(a)
		switch {
		case in(never, a):
			return Target{}, fmt.Errorf("%w: %s is a link-local or cloud metadata address", ErrRefused, a)
		case !allow && (in(blocked, a) || in(s.cfg.Blocked, a) || a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() || a.IsMulticast()):
			return Target{}, fmt.Errorf("%w: %s resolves to %s, a private or internal address", ErrRefused, host, a)
		}
	}
	return Target{URL: u, Host: host, Addr: netip.AddrPortFrom(addrs[0].Unmap(), uint16(port))}, nil
}

// Request is one outbound call.
type Request struct {
	OrgID   uuid.UUID
	Method  string
	URL     string
	Header  http.Header
	Body    []byte
	Timeout time.Duration
	// Secret, when set, signs the request (PGDock-Signature).
	Secret string
}

// Response is what came back.
type Response struct {
	StatusCode int
	Snippet    string
	Latency    time.Duration
}

// Sign is the PGDock-Signature header value: t=<unix>,v1=<hex HMAC-SHA256
// of "t.body"> (V2 §9.1).
func Sign(secret string, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a PGDock-Signature header against body within tolerance
// of now (the receiver side, documented for users and used by tests).
func Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || sig == "" {
		return errors.New("malformed signature header")
	}
	if d := now.Sub(time.Unix(n, 0)); d > tolerance || d < -tolerance {
		return errors.New("signature timestamp outside the tolerance")
	}
	want := Sign(secret, time.Unix(n, 0), body)
	if !hmac.Equal([]byte(want), []byte("t="+ts+",v1="+sig)) {
		return errors.New("signature mismatch")
	}
	return nil
}

// Gate refuses traffic for an organisation that is suspended or has
// outbound traffic disabled.
func (s *Service) Gate(ctx context.Context, orgID uuid.UUID) error {
	o, err := store.New(s.db).GetOrg(ctx, orgID)
	if err != nil {
		return err
	}
	if o.OutboundDisabled || o.Status != "active" {
		return ErrDisabled
	}
	return nil
}

// Do checks and sends r. A refused destination or a disabled organisation
// returns an error without sending; a sent request returns its response
// (any status) and is counted.
func (s *Service) Do(ctx context.Context, r Request) (Response, error) {
	if err := s.Gate(ctx, r.OrgID); err != nil {
		return Response{}, err
	}
	t, err := s.Check(ctx, r.OrgID, r.URL)
	if err != nil {
		return Response{}, err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: nil, // straight to the checked address
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, t.Addr.String())
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	method := r.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, t.URL.String(), strings.NewReader(string(r.Body)))
	if err != nil {
		return Response{}, err
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", "PGDock-Webhooks/1")
	if r.Secret != "" {
		// The receiver checks the timestamp against its own clock.
		req.Header.Set("PGDock-Signature", Sign(r.Secret, time.Now(), r.Body))
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	s.count(ctx, r.OrgID, t.Host, err == nil && resp.StatusCode < 400)
	if err != nil {
		return Response{Latency: latency}, err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, SnippetBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return Response{StatusCode: resp.StatusCode, Snippet: string(snippet), Latency: latency}, nil
}

// count records one request to host for the organisation (V2 §10.7:
// hosts and counts only).
func (s *Service) count(ctx context.Context, orgID uuid.UUID, host string, ok bool) {
	var failures int64
	if !ok {
		failures = 1
	}
	if err := store.New(s.db).CountOutbound(context.WithoutCancel(ctx), store.CountOutboundParams{
		OrgID: orgID, Host: host, Day: Day(s.cfg.Now()), Failures: failures,
	}); err != nil {
		s.log.Warn("count outbound request", "err", err)
	}
}

// bucket is a token bucket refilled at rate per second up to burst.
type bucket struct {
	tokens float64
	at     time.Time
}

// Take spends one token of the organisation's kind bucket, which holds
// perPeriod tokens refilled evenly over period (0: unlimited). False means
// the organisation is over its rate: the caller queues the work.
func (s *Service) Take(orgID uuid.UUID, kind string, perPeriod int64, period time.Duration) bool {
	if perPeriod <= 0 {
		return true
	}
	now := s.cfg.Now()
	key := kind + ":" + orgID.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(perPeriod), at: now}
		s.buckets[key] = b
	}
	rate := float64(perPeriod) / period.Seconds()
	b.tokens = min(float64(perPeriod), b.tokens+now.Sub(b.at).Seconds()*rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Sweep drops counters older than 30 days.
func (s *Service) Sweep(ctx context.Context) error {
	_, err := store.New(s.db).SweepOutboundCounters(ctx, Day(s.cfg.Now().AddDate(0, 0, -30)))
	return err
}

// Day is t's UTC date.
func Day(t time.Time) pgtype.Date {
	return pgtype.Date{Time: t.UTC().Truncate(24 * time.Hour), Valid: true}
}
