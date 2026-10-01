// Package agentapi defines the agent's fixed command API (spec §3.3: "a
// narrow, fixed command API with no arbitrary shell execution"), shared by
// pgdock-agent and pgdock-server's agent client.
package agentapi

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/israel-duff/pgdock/internal/storage"
)

// Paths.
const (
	PathHealth   = "/v1/health"
	PathMetrics  = "/v1/host/metrics"
	PathDump     = "/v1/dump"
	PathRestore  = "/v1/restore"
	PathCopy     = "/v1/copy"
	PathRegister = "/api/v1/agent/register" // on pgdock-server

	// Instances (spec §10 agent API). {id} is the instance ID.
	PathInstances      = "/v1/instances"
	PathInstance       = "/v1/instances/{id}"
	PathInstanceStart  = "/v1/instances/{id}/start"
	PathInstanceStop   = "/v1/instances/{id}/stop"
	PathWALGBackup     = "/v1/instances/{id}/walg/backup"
	PathWALGBackupList = "/v1/instances/{id}/walg/backups"
)

// PGConn is how the agent reaches a Postgres server. Passwords travel only
// in request bodies over mTLS and reach pg_dump/pg_restore through the
// environment, never argv.
type PGConn struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	SSLMode  string `json:"sslmode,omitempty"`
	Database string `json:"database"`
}

// Redacted describes the connection without its password, for logs.
func (c PGConn) Redacted() string {
	return fmt.Sprintf("%s@%s/%s", c.User, net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), c.Database)
}

// ParseURL turns a postgres:// connection string into a PGConn.
func ParseURL(raw string) (PGConn, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return PGConn{}, fmt.Errorf("not a postgres:// connection string")
	}
	c := PGConn{Host: u.Hostname(), Port: 5432, User: u.User.Username(), Database: strings.TrimPrefix(u.Path, "/"), SSLMode: u.Query().Get("sslmode")}
	if p, ok := u.User.Password(); ok {
		c.Password = p
	}
	if u.Port() != "" {
		c.Port, err = strconv.Atoi(u.Port())
		if err != nil {
			return PGConn{}, fmt.Errorf("bad port %q", u.Port())
		}
	}
	if c.Database == "" {
		c.Database = "postgres"
	}
	if c.User == "" {
		return PGConn{}, fmt.Errorf("the connection string needs a user")
	}
	return c, nil
}

// Health is GET /v1/health.
type Health struct {
	Version   string `json:"version"`
	Hostname  string `json:"hostname"`
	NodeID    string `json:"node_id"`
	PGDump    string `json:"pg_dump"`
	PGRestore string `json:"pg_restore"`
	// Docker is "ok" when the agent can manage instances, else why not.
	Docker string `json:"docker"`
	// Image is the Postgres image instances run.
	Image string `json:"image"`
}

// HostMetrics is GET /v1/host/metrics.
type HostMetrics struct {
	CPUs              int     `json:"cpus"`
	Load1             float64 `json:"load1"`
	Load5             float64 `json:"load5"`
	MemTotalBytes     int64   `json:"mem_total_bytes"`
	MemAvailableBytes int64   `json:"mem_available_bytes"`
	DiskPath          string  `json:"disk_path"`
	DiskTotalBytes    int64   `json:"disk_total_bytes"`
	DiskFreeBytes     int64   `json:"disk_free_bytes"`
	// Cumulative counters since boot; rates come from differences.
	CPUBusyTicks   uint64 `json:"cpu_busy_ticks"`
	CPUTotalTicks  uint64 `json:"cpu_total_ticks"`
	DiskReadBytes  uint64 `json:"disk_read_bytes"`
	DiskWriteBytes uint64 `json:"disk_write_bytes"`
}

// Upload is where an agent writes an encrypted object. FileKey is the
// object's own key (never the backup key); WrappedKey goes in the header.
type Upload struct {
	Storage    storage.Target `json:"storage"`
	ObjectKey  string         `json:"object_key"`
	FileKey    []byte         `json:"file_key"`
	WrappedKey []byte         `json:"wrapped_key"`
}

// Download is an encrypted object to read.
type Download struct {
	Storage   storage.Target `json:"storage"`
	ObjectKey string         `json:"object_key"`
	FileKey   []byte         `json:"file_key"`
}

// DumpOptions select what pg_dump includes.
type DumpOptions struct {
	Schemas        []string `json:"schemas,omitempty"`         // -n
	ExcludeSchemas []string `json:"exclude_schemas,omitempty"` // -N
	NoOwner        bool     `json:"no_owner,omitempty"`
	NoACL          bool     `json:"no_acl,omitempty"`
}

// RestoreOptions control pg_restore. Objects are restored without their
// original owners and grants, owned by Role (the project owner), unless
// KeepOwners.
type RestoreOptions struct {
	Role string `json:"role,omitempty"`
	// AllowErrors keeps going past failing statements and reports them as
	// warnings (imports, where some objects reference skipped schemas).
	AllowErrors bool `json:"allow_errors,omitempty"`
	// KeepOwners restores owners and grants as dumped (promotion: the same
	// roles exist on the target), instead of handing everything to Role.
	KeepOwners bool `json:"keep_owners,omitempty"`
}

// DumpRequest is POST /v1/dump: pg_dump -Fc of PG, encrypted, to Upload.
type DumpRequest struct {
	PG      PGConn      `json:"pg"`
	Options DumpOptions `json:"options"`
	Upload  Upload      `json:"upload"`
}

// DumpResult describes the uploaded object.
type DumpResult struct {
	SizeBytes  int64  `json:"size_bytes"` // encrypted object
	DumpBytes  int64  `json:"dump_bytes"` // plaintext dump
	SHA256     string `json:"sha256"`     // of the encrypted object
	DurationMS int64  `json:"duration_ms"`
}

// RestoreRequest is POST /v1/restore: Download, decrypt, pg_restore into PG.
type RestoreRequest struct {
	Download Download       `json:"download"`
	PG       PGConn         `json:"pg"`
	Options  RestoreOptions `json:"options"`
}

// RestoreResult reports a restore.
type RestoreResult struct {
	DurationMS int64    `json:"duration_ms"`
	Warnings   []string `json:"warnings,omitempty"`
}

// CopyRequest is POST /v1/copy: pg_dump Source | pg_restore Target, with no
// intermediate file (imports, spec §6.8).
type CopyRequest struct {
	Source  PGConn         `json:"source"`
	Dump    DumpOptions    `json:"dump"`
	Target  PGConn         `json:"target"`
	Restore RestoreOptions `json:"restore"`
}

// RegisterRequest is POST /api/v1/agent/register on pgdock-server.
type RegisterRequest struct {
	// Token is a one-time registration token for a node, or the bootstrap
	// token together with Node naming an existing node.
	Token         string `json:"token"`
	Node          string `json:"node,omitempty"`
	CSR           string `json:"csr"`
	AdvertiseHost string `json:"advertise_host"`
	AdvertisePort int    `json:"advertise_port"`
	Version       string `json:"version"`
}

// RegisterResponse carries the signed certificate and the CA to pin.
type RegisterResponse struct {
	NodeID  string `json:"node_id"`
	CertPEM string `json:"cert_pem"`
	CAPEM   string `json:"ca_pem"`
}

// Error is the agent's error body.
type Error struct {
	Error string `json:"error"`
}

// Instance kinds.
const (
	InstanceDedicated = "dedicated"
	InstanceShared    = "shared"
)

// InstanceSpec is POST /v1/instances: create (idempotently) and start a
// Postgres container with a volume (spec §4.2).
type InstanceSpec struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// CPUs and MemoryMB are hard container limits.
	CPUs     float64 `json:"cpus"`
	MemoryMB int     `json:"memory_mb"`
	// AdminUser/AdminPassword create the instance's superuser at initdb
	// (ignored when the data comes from a restore).
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	// Settings are postgresql.conf parameters, passed as -c flags.
	Settings map[string]string `json:"settings"`
	// WALG, when set, enables continuous archiving to it.
	WALG *WALG `json:"walg,omitempty"`
	// Restore fills the volume from a WAL-G base backup and recovers
	// (to TargetTime, if set) before the instance starts serving.
	Restore *WALGRestore `json:"restore,omitempty"`
	// Recreate replaces an existing container (keeping its volume) with
	// one from the agent's current image and this spec: a restart that
	// picks up a new Postgres minor version (spec §11.3).
	Recreate bool `json:"recreate,omitempty"`
}

// WALG is where an instance's base backups and WAL live, and the key that
// encrypts them.
type WALG struct {
	Storage storage.Target `json:"storage"`
	// Prefix is relative to the target's prefix, e.g. "projects/<id>/wal-g".
	Prefix string `json:"prefix"`
	// PGPKey is the armored OpenPGP private key (WALG_PGP_KEY).
	PGPKey string `json:"pgp_key"`
}

// WALGRestore restores another instance's backups into a new volume.
type WALGRestore struct {
	Source     WALG   `json:"source"`
	BackupName string `json:"backup_name"` // or "LATEST"
	// TargetTime is RFC 3339; empty replays all archived WAL.
	TargetTime string `json:"target_time,omitempty"`
}

// Instance describes a container (POST/GET /v1/instances...).
type Instance struct {
	ID          string `json:"id"`
	ContainerID string `json:"container_id"`
	Container   string `json:"container"`
	Volume      string `json:"volume"`
	State       string `json:"state"` // created, running, exited, missing
	Running     bool   `json:"running"`
	// Host and Port reach Postgres from the node's network (the container
	// name on the agent's Docker network, or the published address).
	Host string `json:"host"`
	Port int    `json:"port"`
	// PublishedHost/Port are the port published on the node, if any.
	PublishedHost string `json:"published_host,omitempty"`
	PublishedPort int    `json:"published_port,omitempty"`
	Image         string `json:"image"`
}

// WALGBackupRequest is POST /v1/instances/{id}/walg/backup.
type WALGBackupRequest struct {
	// RetainFull keeps this many full backups afterwards (0: keep all).
	RetainFull int `json:"retain_full"`
}

// WALGBackup is one base backup as WAL-G lists it.
type WALGBackup struct {
	Name             string    `json:"backup_name"`
	StartTime        time.Time `json:"start_time"`
	FinishTime       time.Time `json:"finish_time"`
	WALFileName      string    `json:"wal_file_name"`
	StartLSN         uint64    `json:"start_lsn"`
	FinishLSN        uint64    `json:"finish_lsn"`
	UncompressedSize int64     `json:"uncompressed_size"`
	CompressedSize   int64     `json:"compressed_size"`
}

// WALGBackupResult reports a base backup.
type WALGBackupResult struct {
	Backup     WALGBackup `json:"backup"`
	DurationMS int64      `json:"duration_ms"`
	Deleted    string     `json:"deleted,omitempty"` // retention output
}
