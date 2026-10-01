package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/israel-duff/pgdock/internal/crypto"
	"github.com/israel-duff/pgdock/internal/store"
)

// AdminSecret is the per-node pgdock_admin credential, stored encrypted in
// nodes.pg_admin_secret (spec §7.3).
type AdminSecret struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

func adminSecretAAD(nodeID uuid.UUID) []byte {
	return []byte("nodes.pg_admin_secret:" + nodeID.String())
}

func sealAdminSecret(k *crypto.Keyring, nodeID uuid.UUID, s AdminSecret) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return k.Encrypt(b, adminSecretAAD(nodeID))
}

func openAdminSecret(k *crypto.Keyring, nodeID uuid.UUID, blob []byte) (AdminSecret, error) {
	var s AdminSecret
	if len(blob) == 0 {
		return s, fmt.Errorf("node %s has no admin credential", nodeID)
	}
	b, err := k.Decrypt(blob, adminSecretAAD(nodeID))
	if err != nil {
		return s, fmt.Errorf("node %s admin credential: %w", nodeID, err)
	}
	return s, json.Unmarshal(b, &s)
}

func instanceSecretAAD(instanceID uuid.UUID) []byte {
	return []byte("instances.admin_secret:" + instanceID.String())
}

// SealInstanceSecret seals an instance's own superuser credential
// (instances.admin_secret).
func SealInstanceSecret(k *crypto.Keyring, instanceID uuid.UUID, s AdminSecret) ([]byte, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return k.Encrypt(b, instanceSecretAAD(instanceID))
}

// OpenInstanceSecret opens instances.admin_secret.
func OpenInstanceSecret(k *crypto.Keyring, instanceID uuid.UUID, blob []byte) (AdminSecret, error) {
	var s AdminSecret
	b, err := k.Decrypt(blob, instanceSecretAAD(instanceID))
	if err != nil {
		return s, fmt.Errorf("instance %s admin credential: %w", instanceID, err)
	}
	return s, json.Unmarshal(b, &s)
}

// target is how to reach an instance as its superuser.
type target struct {
	store.GetInstanceTargetRow
	Secret AdminSecret
}

// adminAddr is the control plane's address for the instance.
func (t target) adminAddr() (string, int) {
	host, port := t.Host, int(t.Port)
	if t.AdminHost != nil && *t.AdminHost != "" {
		host = *t.AdminHost
	}
	if t.AdminPort != nil && *t.AdminPort > 0 {
		port = int(*t.AdminPort)
	}
	return host, port
}

func (s *Service) instanceTarget(ctx context.Context, instanceID uuid.UUID) (target, error) {
	row, err := store.New(s.db).GetInstanceTarget(ctx, instanceID)
	if err != nil {
		return target{}, fmt.Errorf("load instance %s: %w", instanceID, err)
	}
	t := target{GetInstanceTargetRow: row}
	if len(row.AdminSecret) > 0 {
		t.Secret, err = OpenInstanceSecret(s.keyring, row.ID, row.AdminSecret)
	} else {
		t.Secret, err = openAdminSecret(s.keyring, row.NodeID, row.PgAdminSecret)
	}
	return t, err
}

// connectInstance opens an admin connection to database on an instance,
// using the control-plane address (admin_host/admin_port when set).
func (s *Service) connectInstance(ctx context.Context, instanceID uuid.UUID, database string) (*pgx.Conn, error) {
	cfg, node, err := s.adminConfig(ctx, instanceID, database)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to %s on node %s: %w", database, node, err)
	}
	return conn, nil
}

// adminConfig is the superuser connection config for database on an
// instance, at the control-plane address, and the instance's node name.
func (s *Service) adminConfig(ctx context.Context, instanceID uuid.UUID, database string) (*pgx.ConnConfig, string, error) {
	t, err := s.instanceTarget(ctx, instanceID)
	if err != nil {
		return nil, "", err
	}
	host, port := t.adminAddr()
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d sslmode=%s", host, port, s.cfg.AdminSSLMode))
	if err != nil {
		return nil, "", err
	}
	cfg.User, cfg.Password, cfg.Database = t.Secret.User, t.Secret.Password, database
	cfg.RuntimeParams["application_name"] = "pgdock-server"
	cfg.ConnectTimeout = 10 * time.Second
	return cfg, t.NodeName, nil
}

// ident quotes a Postgres identifier.
func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

// literal quotes a string literal for utility statements (CREATE ROLE,
// ALTER ROLE SET, ...) that cannot take bind parameters.
func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var durationRe = regexp.MustCompile(`^[0-9]{1,9}(us|ms|s|min|h|d)?$`)

func validDuration(s string) bool { return durationRe.MatchString(s) }

// numeric converts a float for a numeric column.
func numeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(strconv.FormatFloat(f, 'f', -1, 64))
	return n
}
