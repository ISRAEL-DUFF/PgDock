package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// SharedCluster is a shared-tier cluster registered at startup. Until node
// agents arrive (M4), this is how the first node is added.
type SharedCluster struct {
	// AdminURL (PGDOCK_SHARED_ADMIN_URL) connects as the cluster superuser
	// pgdock_admin. Empty disables registration.
	AdminURL string
	// NodeName (PGDOCK_SHARED_NODE_NAME, default "local").
	NodeName string
	// PoolerHost/PoolerPort (PGDOCK_SHARED_POOLER_HOST/_PORT) are how the
	// poolers reach the cluster; default to AdminURL's host and port.
	PoolerHost string
	PoolerPort int
	// NodeRole (PGDOCK_SHARED_NODE_ROLE, default "both") is the node's role
	// when first registered; later it is changed in the UI.
	NodeRole string
}

// Pooler configures pgdock-server's control of the edge PgBouncers.
type Pooler struct {
	// ConfigDir (PGDOCK_POOLER_CONFIG_DIR) receives databases.ini and
	// userlist.txt. Empty disables provisioning.
	ConfigDir string
	// FileMode (PGDOCK_POOLER_FILE_MODE, octal, default 0640).
	FileMode os.FileMode
	// SessionAddr and PooledAddr (PGDOCK_POOLER_SESSION_ADDR/_POOLED_ADDR)
	// are host:port of the two poolers as pgdock-server reaches them, for
	// the admin console and smoke tests. Default to the public address.
	SessionAddr string
	PooledAddr  string
	// AdminUser/AdminPassword (PGDOCK_POOLER_ADMIN_USER, default "pgdock";
	// PGDOCK_POOLER_ADMIN_PASSWORD or _FILE) log in to the admin console.
	AdminUser     string
	AdminPassword string
	// SSLMode for admin and smoke-test connections (PGDOCK_POOLER_SSLMODE,
	// default "prefer").
	SSLMode string

	// Local (PGDOCK_POOLER_LOCAL, default true): SessionAddr and PooledAddr
	// are poolers pgdock-server administers itself, next to it, reading
	// ConfigDir. With a standby pooler pair (V3 §2.1) set it to false and
	// point SessionAddr/PooledAddr at the floating IP for smoke tests; the
	// pooler hosts are then administered through their nodes.
	Local bool
	// HostSessionPort and HostPooledPort (PGDOCK_POOLER_HOST_SESSION_PORT,
	// _POOLED_PORT, default 5432 and 6543) are the admin consoles on each
	// pooler host's private address.
	HostSessionPort int
	HostPooledPort  int
	// FloatingIP is the edge pooler's floating IP, when there is one.
	FloatingIP FloatingIP
}

// FloatingIP configures the Hetzner floating IP in front of the pooler
// hosts (V3 §2.1), which pgdock-server checks and corrects.
type FloatingIP struct {
	// ID (PGDOCK_FLOATING_IP_ID); empty means no floating IP to manage.
	ID string
	// Token (PGDOCK_HETZNER_TOKEN or _FILE) and API (PGDOCK_HETZNER_API,
	// default the Hetzner Cloud API).
	Token string
	API   string
}

// Public is the connection info clients receive (spec §5.1).
type Public struct {
	// Host (PGDOCK_DB_HOST, default "localhost"), e.g. db.example.com.
	Host string
	// SessionPort (PGDOCK_DB_SESSION_PORT, default 5432) and PooledPort
	// (PGDOCK_DB_POOLED_PORT, default 6543).
	SessionPort int
	PooledPort  int
	// SSLMode (PGDOCK_DB_SSLMODE, default "require").
	SSLMode string
}

var sslModes = map[string]bool{"disable": true, "allow": true, "prefer": true, "require": true, "verify-ca": true, "verify-full": true}

func loadProvisioning(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	var errs []error
	port := func(name string, def int) int {
		v := getenv(name)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			errs = append(errs, fmt.Errorf("%s: must be a port number, got %q", name, v))
		}
		return n
	}
	str := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	sslmode := func(name, def string) string {
		v := str(name, def)
		if !sslModes[v] {
			errs = append(errs, fmt.Errorf("%s: unknown sslmode %q", name, v))
		}
		return v
	}
	addr := func(name, def string) string {
		v := str(name, def)
		if _, _, err := net.SplitHostPort(v); err != nil {
			errs = append(errs, fmt.Errorf("%s: must be host:port, got %q", name, v))
		}
		return v
	}

	cfg.Public = Public{
		Host:        str("PGDOCK_DB_HOST", "localhost"),
		SessionPort: port("PGDOCK_DB_SESSION_PORT", 5432),
		PooledPort:  port("PGDOCK_DB_POOLED_PORT", 6543),
		SSLMode:     sslmode("PGDOCK_DB_SSLMODE", "require"),
	}
	cfg.PGVersions = []int{17, 18}
	if v := getenv("PGDOCK_PG_VERSIONS"); v != "" {
		cfg.PGVersions = nil
		for _, f := range strings.Split(v, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil || n < 13 || n > 99 {
				errs = append(errs, fmt.Errorf("PGDOCK_PG_VERSIONS: %q is not a Postgres major version", f))
				continue
			}
			cfg.PGVersions = append(cfg.PGVersions, n)
		}
	}

	cfg.Shared = SharedCluster{
		AdminURL:   getenv("PGDOCK_SHARED_ADMIN_URL"),
		NodeName:   str("PGDOCK_SHARED_NODE_NAME", "local"),
		PoolerHost: getenv("PGDOCK_SHARED_POOLER_HOST"),
		PoolerPort: port("PGDOCK_SHARED_POOLER_PORT", 0),
		NodeRole:   str("PGDOCK_SHARED_NODE_ROLE", "both"),
	}
	if r := cfg.Shared.NodeRole; r != "shared" && r != "dedicated" && r != "both" {
		errs = append(errs, fmt.Errorf("PGDOCK_SHARED_NODE_ROLE: must be shared, dedicated, or both, got %q", r))
	}

	p := Pooler{ConfigDir: getenv("PGDOCK_POOLER_CONFIG_DIR"), FileMode: 0o640}
	if p.ConfigDir == "" {
		cfg.Pooler = p
		return errs
	}
	if v := getenv("PGDOCK_POOLER_FILE_MODE"); v != "" {
		m, err := strconv.ParseUint(v, 8, 32)
		if err != nil || m > 0o777 || m&0o600 != 0o600 {
			errs = append(errs, fmt.Errorf("PGDOCK_POOLER_FILE_MODE: octal mode readable and writable by the owner, got %q", v))
		}
		p.FileMode = os.FileMode(m)
	}
	pub := cfg.Public
	p.SessionAddr = addr("PGDOCK_POOLER_SESSION_ADDR", net.JoinHostPort(pub.Host, strconv.Itoa(pub.SessionPort)))
	p.PooledAddr = addr("PGDOCK_POOLER_POOLED_ADDR", net.JoinHostPort(pub.Host, strconv.Itoa(pub.PooledPort)))
	p.AdminUser = str("PGDOCK_POOLER_ADMIN_USER", "pgdock")
	p.SSLMode = sslmode("PGDOCK_POOLER_SSLMODE", "prefer")

	inline, file := getenv("PGDOCK_POOLER_ADMIN_PASSWORD"), getenv("PGDOCK_POOLER_ADMIN_PASSWORD_FILE")
	switch {
	case inline != "" && file != "":
		errs = append(errs, errors.New("set only one of PGDOCK_POOLER_ADMIN_PASSWORD and PGDOCK_POOLER_ADMIN_PASSWORD_FILE"))
	case inline != "":
		p.AdminPassword = inline
	case file != "":
		b, err := readFile(file)
		if err != nil {
			errs = append(errs, fmt.Errorf("PGDOCK_POOLER_ADMIN_PASSWORD_FILE: %w", err))
		}
		p.AdminPassword = strings.TrimRight(string(b), "\r\n")
	default:
		errs = append(errs, errors.New("PGDOCK_POOLER_ADMIN_PASSWORD or _FILE is required with PGDOCK_POOLER_CONFIG_DIR"))
	}
	p.Local = getenv("PGDOCK_POOLER_LOCAL") != "false"
	p.HostSessionPort = port("PGDOCK_POOLER_HOST_SESSION_PORT", 5432)
	p.HostPooledPort = port("PGDOCK_POOLER_HOST_POOLED_PORT", 6543)
	p.FloatingIP = FloatingIP{ID: getenv("PGDOCK_FLOATING_IP_ID"), API: getenv("PGDOCK_HETZNER_API")}
	if p.FloatingIP.ID != "" {
		tok, tokFile := getenv("PGDOCK_HETZNER_TOKEN"), getenv("PGDOCK_HETZNER_TOKEN_FILE")
		switch {
		case tok != "" && tokFile != "":
			errs = append(errs, errors.New("set only one of PGDOCK_HETZNER_TOKEN and PGDOCK_HETZNER_TOKEN_FILE"))
		case tok != "":
			p.FloatingIP.Token = tok
		case tokFile != "":
			b, err := readFile(tokFile)
			if err != nil {
				errs = append(errs, fmt.Errorf("PGDOCK_HETZNER_TOKEN_FILE: %w", err))
			}
			p.FloatingIP.Token = strings.TrimSpace(string(b))
		default:
			errs = append(errs, errors.New("PGDOCK_HETZNER_TOKEN or _FILE is required with PGDOCK_FLOATING_IP_ID"))
		}
	}
	cfg.Pooler = p
	return errs
}

// PoolerTLS configures the poolers' client-facing certificate.
type PoolerTLS struct {
	// Mode (PGDOCK_POOLER_TLS): self-signed (default), acme, files, or off.
	Mode string
	// DataDir (PGDOCK_DATA_DIR, default /var/lib/pgdock) holds ACME state.
	DataDir string
	// ACMEEmail (PGDOCK_ACME_EMAIL), ACMECA (PGDOCK_ACME_CA, default Let's
	// Encrypt), and ACMECARoots (PGDOCK_ACME_CA_ROOTS, a PEM file trusted for
	// the CA's own HTTPS, for private or test CAs).
	ACMEEmail   string
	ACMECA      string
	ACMECARoots string
	// CertFile/KeyFile (PGDOCK_POOLER_TLS_CERT/_KEY) for mode files.
	CertFile string
	KeyFile  string
}

func loadPoolerTLS(getenv func(string) string, cfg *Config) []error {
	t := PoolerTLS{
		Mode:        getenv("PGDOCK_POOLER_TLS"),
		DataDir:     getenv("PGDOCK_DATA_DIR"),
		ACMEEmail:   getenv("PGDOCK_ACME_EMAIL"),
		ACMECA:      getenv("PGDOCK_ACME_CA"),
		ACMECARoots: getenv("PGDOCK_ACME_CA_ROOTS"),
		CertFile:    getenv("PGDOCK_POOLER_TLS_CERT"),
		KeyFile:     getenv("PGDOCK_POOLER_TLS_KEY"),
	}
	if t.Mode == "" {
		t.Mode = "self-signed"
	}
	if t.DataDir == "" {
		t.DataDir = "/var/lib/pgdock"
	}
	var errs []error
	switch t.Mode {
	case "self-signed", "off":
	case "acme":
		if t.ACMEEmail == "" {
			errs = append(errs, fmt.Errorf("PGDOCK_ACME_EMAIL is required with PGDOCK_POOLER_TLS=acme"))
		}
	case "files":
		if t.CertFile == "" || t.KeyFile == "" {
			errs = append(errs, fmt.Errorf("PGDOCK_POOLER_TLS_CERT and _KEY are required with PGDOCK_POOLER_TLS=files"))
		}
	default:
		errs = append(errs, fmt.Errorf("PGDOCK_POOLER_TLS: want self-signed, acme, files, or off; got %q", t.Mode))
	}
	cfg.PoolerTLS = t
	return errs
}
