// Package settings holds operator-editable global settings stored in the
// metadata DB's settings table, cached in memory.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/store"
)

const keyDBHost = "db_host"

// Store caches settings and notifies listeners of changes.
type Store struct {
	db          *pgxpool.Pool
	defaultHost string

	mu        sync.RWMutex
	dbHost    string
	listeners []func(host string)
}

// New returns a Store. defaultHost (PGDOCK_DB_HOST) applies until the
// operator sets a hostname.
func New(db *pgxpool.Pool, defaultHost string) *Store {
	return &Store{db: db, defaultHost: defaultHost}
}

// Load reads settings from the database.
func (s *Store) Load(ctx context.Context) error {
	raw, err := store.New(s.db).GetSetting(ctx, keyDBHost)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var host string
	if err := json.Unmarshal(raw, &host); err != nil {
		return fmt.Errorf("settings %s: %w", keyDBHost, err)
	}
	s.mu.Lock()
	s.dbHost = host
	s.mu.Unlock()
	return nil
}

// DBHost is the hostname clients use for databases.
func (s *Store) DBHost() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.dbHost != "" {
		return s.dbHost
	}
	return s.defaultHost
}

// Configured reports whether the operator has set a hostname.
func (s *Store) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dbHost != ""
}

// OnDBHostChange registers f to run after the hostname changes.
func (s *Store) OnDBHostChange(f func(host string)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, f)
	s.mu.Unlock()
}

var hostnameRe = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$`)

// ValidateHost checks a DB hostname: a DNS name or an IP address.
func ValidateHost(host string) (string, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || len(host) > 253 {
		return "", errors.New("enter a hostname such as db.example.com")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return host, nil
	}
	if !hostnameRe.MatchString(host) {
		return "", fmt.Errorf("%q is not a valid hostname", host)
	}
	return host, nil
}

// SetDBHost validates, stores, and broadcasts a new hostname.
func (s *Store) SetDBHost(ctx context.Context, host string) (string, error) {
	host, err := ValidateHost(host)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(host)
	if err := store.New(s.db).PutSetting(ctx, store.PutSettingParams{Key: keyDBHost, Value: b}); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.dbHost = host
	listeners := append([]func(string){}, s.listeners...)
	s.mu.Unlock()
	for _, f := range listeners {
		f(host)
	}
	return host, nil
}

// DNSCheck is the result of CheckDNS.
type DNSCheck struct {
	Host            string
	Addresses       []string
	ServerAddresses []string
	PointsHere      bool
	Err             error
}

// CheckDNS resolves host and compares it with the addresses this server is
// known by: its own public addresses (PGDOCK_PUBLIC_IPS), its interfaces,
// and whatever the hostname the operator reached the UI through resolves to
// (the UI and the poolers share the control node).
func CheckDNS(ctx context.Context, host, uiHost string, publicIPs []netip.Addr) DNSCheck {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out := DNSCheck{Host: host}
	r := net.DefaultResolver

	mine := map[netip.Addr]bool{}
	for _, a := range publicIPs {
		mine[a.Unmap()] = true
	}
	if ifaces, err := net.InterfaceAddrs(); err == nil {
		for _, ia := range ifaces {
			if p, err := netip.ParsePrefix(ia.String()); err == nil && !p.Addr().IsLoopback() && !p.Addr().IsLinkLocalUnicast() {
				mine[p.Addr().Unmap()] = true
			}
		}
	}
	if uiHost != "" {
		if h, _, err := net.SplitHostPort(uiHost); err == nil {
			uiHost = h
		}
		if a, err := netip.ParseAddr(uiHost); err == nil {
			mine[a.Unmap()] = true
		} else if addrs, err := r.LookupNetIP(ctx, "ip", uiHost); err == nil {
			for _, a := range addrs {
				mine[a.Unmap()] = true
			}
		}
	}
	for a := range mine {
		out.ServerAddresses = append(out.ServerAddresses, a.String())
	}
	sort.Strings(out.ServerAddresses)

	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		addrs, out.Err = r.LookupNetIP(ctx, "ip", host)
	}
	for _, a := range addrs {
		a = a.Unmap()
		out.Addresses = append(out.Addresses, a.String())
		if mine[a] || a.IsLoopback() && len(publicIPs) == 0 {
			out.PointsHere = true
		}
	}
	sort.Strings(out.Addresses)
	return out
}
