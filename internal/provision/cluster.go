package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

// connectInstance opens an admin connection to database on an instance,
// using the control-plane address (admin_host/admin_port when set).
func (s *Service) connectInstance(ctx context.Context, instanceID uuid.UUID, database string) (*pgx.Conn, error) {
	t, err := store.New(s.db).GetInstanceTarget(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("load instance %s: %w", instanceID, err)
	}
	secret, err := openAdminSecret(s.keyring, t.NodeID, t.PgAdminSecret)
	if err != nil {
		return nil, err
	}
	host, port := t.PrivateAddr, int(t.Port)
	if t.AdminHost != nil && *t.AdminHost != "" {
		host = *t.AdminHost
	}
	if t.AdminPort != nil && *t.AdminPort > 0 {
		port = int(*t.AdminPort)
	}
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d sslmode=%s", host, port, s.cfg.AdminSSLMode))
	if err != nil {
		return nil, err
	}
	cfg.User, cfg.Password, cfg.Database = secret.User, secret.Password, database
	cfg.RuntimeParams["application_name"] = "pgdock-server"
	cfg.ConnectTimeout = 10 * time.Second
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to %s on node %s: %w", database, t.NodeName, err)
	}
	return conn, nil
}

// ident quotes a Postgres identifier.
func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

// literal quotes a string literal for utility statements (CREATE ROLE,
// ALTER ROLE SET, ...) that cannot take bind parameters.
func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var durationRe = regexp.MustCompile(`^[0-9]{1,9}(us|ms|s|min|h|d)?$`)

func validDuration(s string) bool { return durationRe.MatchString(s) }
