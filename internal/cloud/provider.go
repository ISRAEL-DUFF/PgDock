// Package cloud creates and deletes the machines PGDock runs on (V3 §5.1):
// a provider interface, Hetzner Cloud as its first implementation, and a
// manual provider for machines registered by hand. Capacity automation
// (package capacity) decides when; this package only talks to providers.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Provider names.
const (
	Manual  = "manual"
	Hetzner = "hetzner"
)

// Errors.
var (
	// ErrManual: the provider can't create machines; an operator adds them.
	ErrManual   = errors.New("this provider has no API: register the machine by hand")
	ErrNotFound = errors.New("not found at the provider")
)

// ServerSpec is a server to create.
type ServerSpec struct {
	Name     string
	Type     string // the provider's server type, e.g. "cpx31"
	Location string // e.g. "fsn1"
	Image    string // e.g. "ubuntu-24.04"
	// UserData is the cloud-init document the server boots with.
	UserData       string
	PlacementGroup string
	Network        string // a private network to attach
	SSHKeys        []string
	Labels         map[string]string
}

// Server is a machine at the provider.
type Server struct {
	ID         string
	Name       string
	Type       string
	Location   string
	Status     string
	PublicIPv4 string
	PrivateIP  string
	Labels     map[string]string
}

// Filter narrows ListServers.
type Filter struct {
	// Labels the servers must all carry.
	Labels map[string]string
}

// VolumeSpec is a block volume to create.
type VolumeSpec struct {
	Name     string
	SizeGB   int
	Location string
	Labels   map[string]string
}

// Volume is a block volume.
type Volume struct {
	ID     string
	Name   string
	SizeGB int
}

// ServerPrice is one server type's price in one location, from the
// provider's catalog: what cost attribution and proposals price with.
type ServerPrice struct {
	Type     string  `json:"type"`
	Location string  `json:"location"`
	CPUs     int     `json:"cpus"`
	MemoryGB float64 `json:"memory_gb"`
	DiskGB   int     `json:"disk_gb"`
	// MonthlyMinor is the monthly price before VAT, in minor units of
	// Currency (cents).
	MonthlyMinor int64  `json:"monthly_minor"`
	Currency     string `json:"currency"`
}

// Provider is a source of machines (V3 §5.1).
type Provider interface {
	Name() string
	CreateServer(ctx context.Context, spec ServerSpec) (Server, error)
	DeleteServer(ctx context.Context, id string) error
	ListServers(ctx context.Context, f Filter) ([]Server, error)
	CreateVolume(ctx context.Context, spec VolumeSpec) (Volume, error)
	AttachVolume(ctx context.Context, volumeID, serverID string) error
	AssignFloatingIP(ctx context.Context, ipID, serverID string) error
	PriceCatalog(ctx context.Context) ([]ServerPrice, error)
}

// ManualProvider is for machines PGDock can't create (colocated servers,
// V3 §6.2): an operator registers them, and PGDock treats them as a fixed
// pool. Its catalog is what the operator entered.
type ManualProvider struct {
	Catalog []ServerPrice
}

// Name is "manual".
func (ManualProvider) Name() string { return Manual }

// CreateServer can't: a proposal for a manual region waits for a machine.
func (ManualProvider) CreateServer(context.Context, ServerSpec) (Server, error) {
	return Server{}, ErrManual
}

// DeleteServer can't: the operator decommissions the machine.
func (ManualProvider) DeleteServer(context.Context, string) error { return ErrManual }

// ListServers lists nothing: manual machines are only in PGDock's nodes.
func (ManualProvider) ListServers(context.Context, Filter) ([]Server, error) { return nil, nil }

// CreateVolume can't.
func (ManualProvider) CreateVolume(context.Context, VolumeSpec) (Volume, error) {
	return Volume{}, ErrManual
}

// AttachVolume can't.
func (ManualProvider) AttachVolume(context.Context, string, string) error { return ErrManual }

// AssignFloatingIP can't.
func (ManualProvider) AssignFloatingIP(context.Context, string, string) error { return ErrManual }

// PriceCatalog is the operator's entries.
func (m ManualProvider) PriceCatalog(context.Context) ([]ServerPrice, error) {
	return append([]ServerPrice(nil), m.Catalog...), nil
}

// Cheapest returns the cheapest server type in location (any location
// when empty) with at least cpus, memGB and diskGB.
func Cheapest(catalog []ServerPrice, location string, cpus int, memGB float64, diskGB int) (ServerPrice, error) {
	var fit []ServerPrice
	for _, p := range catalog {
		if (location == "" || p.Location == location) && p.CPUs >= cpus && p.MemoryGB >= memGB && p.DiskGB >= diskGB {
			fit = append(fit, p)
		}
	}
	if len(fit) == 0 {
		return ServerPrice{}, fmt.Errorf("no server type in %s has %d vCPU, %g GB RAM and %d GB disk", orAny(location), cpus, memGB, diskGB)
	}
	sort.Slice(fit, func(i, j int) bool {
		if fit[i].MonthlyMinor != fit[j].MonthlyMinor {
			return fit[i].MonthlyMinor < fit[j].MonthlyMinor
		}
		return fit[i].Type < fit[j].Type
	})
	return fit[0], nil
}

// Price finds typ in location.
func Price(catalog []ServerPrice, typ, location string) (ServerPrice, bool) {
	for _, p := range catalog {
		if strings.EqualFold(p.Type, typ) && (location == "" || p.Location == location) {
			return p, true
		}
	}
	return ServerPrice{}, false
}

func orAny(l string) string {
	if l == "" {
		return "any location"
	}
	return l
}
