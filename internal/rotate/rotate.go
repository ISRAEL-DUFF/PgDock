// Package rotate re-encrypts every secret PGDock stores under the primary
// master key (spec §7.3: "The master key can be rotated with a CLI command
// that re-encrypts the stored secrets").
//
// Rotation: start pgdock-server once with -rotate-master-key, the new key
// as PGDOCK_MASTER_KEY and the old one in PGDOCK_MASTER_KEY_PREVIOUS. When
// it reports success, drop the old key from the environment.
package rotate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/israel-duff/pgdock/internal/crypto"
)

// The associated data each secret is sealed with. They must match the
// packages that seal them (auth, backup, provision, agentca, alerts);
// TestRotation decrypts everything with the new key alone to prove it.
func totpAAD(id uuid.UUID) []byte     { return []byte("operators.totp_secret:" + id.String()) }
func storageAAD(id uuid.UUID) []byte  { return []byte("storage_targets.credentials:" + id.String()) }
func nodeAAD(id uuid.UUID) []byte     { return []byte("nodes.pg_admin_secret:" + id.String()) }
func instanceAAD(id uuid.UUID) []byte { return []byte("instances.admin_secret:" + id.String()) }
func opAAD(kind string, p uuid.UUID) []byte {
	return []byte("operations.secrets.password:" + kind + ":" + p.String())
}

// Result counts what was looked at and rewritten, per kind of secret.
type Result struct {
	Checked   map[string]int
	Rewrapped map[string]int
	// Cleared is how many short-lived sign-in challenges were dropped
	// (their owners just sign in again).
	Cleared int
}

// mode is either rewrap (re-encrypt what needs it) or verify (decrypt only).
type mode int

const (
	rewrap mode = iota
	verify
)

// Run re-encrypts, in one transaction, every stored secret not already
// under the keyring's primary key.
func Run(ctx context.Context, db *pgxpool.Pool, k *crypto.Keyring) (Result, error) {
	return walk(ctx, db, k, rewrap)
}

// Verify decrypts every stored secret with k and fails on the first one it
// cannot open (run it with only the new key after a rotation).
func Verify(ctx context.Context, db *pgxpool.Pool, k *crypto.Keyring) (Result, error) {
	return walk(ctx, db, k, verify)
}

type secret struct {
	what string
	blob []byte
	aad  []byte
	save func(tx pgx.Tx, blob []byte) error
}

func walk(ctx context.Context, db *pgxpool.Pool, k *crypto.Keyring, m mode) (Result, error) {
	res := Result{Checked: map[string]int{}, Rewrapped: map[string]int{}}
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		secrets, err := collect(ctx, tx)
		if err != nil {
			return err
		}
		for _, s := range secrets {
			res.Checked[s.what]++
			if m == verify {
				if _, err := k.Decrypt(s.blob, s.aad); err != nil {
					return fmt.Errorf("%s: %w", s.what, err)
				}
				continue
			}
			if !k.NeedsRotation(s.blob) {
				continue
			}
			nb, err := k.Reencrypt(s.blob, s.aad)
			if err != nil {
				return fmt.Errorf("%s: %w (is the old key in PGDOCK_MASTER_KEY_PREVIOUS?)", s.what, err)
			}
			if err := s.save(tx, nb); err != nil {
				return fmt.Errorf("%s: save: %w", s.what, err)
			}
			res.Rewrapped[s.what]++
		}
		if m == rewrap {
			tag, err := tx.Exec(ctx, `DELETE FROM auth_challenges`)
			if err != nil {
				return err
			}
			res.Cleared = int(tag.RowsAffected())
		}
		return nil
	})
	return res, err
}

func collect(ctx context.Context, tx pgx.Tx) ([]secret, error) {
	var out []secret
	add := func(what, query string, aad func(uuid.UUID) []byte, update string) error {
		rows, err := tx.Query(ctx, query)
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		type row struct {
			ID   uuid.UUID
			Blob []byte
		}
		rs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		for _, r := range rs {
			id := r.ID
			out = append(out, secret{what: what, blob: r.Blob, aad: aad(id), save: func(tx pgx.Tx, b []byte) error {
				_, err := tx.Exec(ctx, update, id, b)
				return err
			}})
		}
		return nil
	}
	for _, c := range []struct {
		what, query string
		aad         func(uuid.UUID) []byte
		update      string
	}{
		{"operator TOTP secret", `SELECT id, totp_secret FROM operators WHERE totp_secret IS NOT NULL FOR UPDATE`, totpAAD,
			`UPDATE operators SET totp_secret = $2 WHERE id = $1`},
		{"storage credentials", `SELECT id, credentials FROM storage_targets WHERE credentials IS NOT NULL FOR UPDATE`, storageAAD,
			`UPDATE storage_targets SET credentials = $2 WHERE id = $1`},
		{"node admin credential", `SELECT id, pg_admin_secret FROM nodes WHERE pg_admin_secret IS NOT NULL FOR UPDATE`, nodeAAD,
			`UPDATE nodes SET pg_admin_secret = $2 WHERE id = $1`},
		{"instance admin credential", `SELECT id, admin_secret FROM instances WHERE admin_secret IS NOT NULL FOR UPDATE`, instanceAAD,
			`UPDATE instances SET admin_secret = $2 WHERE id = $1`},
	} {
		if err := add(c.what, c.query, c.aad, c.update); err != nil {
			return nil, err
		}
	}

	// Settings rows hold sealed blobs inside JSON.
	for _, f := range []struct {
		what, key, aad string
		path           []string
	}{
		{"agent CA key", "agent_ca", "settings.agent_ca.key", []string{"key_sealed"}},
		{"backup key", "backup_key", "settings.backup_key", []string{"sealed"}},
		{"alert webhook secret", "alerts", "settings.alerts.webhook_secret", []string{"webhook_secret"}},
		{"alert SMTP password", "alerts", "settings.alerts.smtp_password", []string{"smtp", "password"}},
	} {
		s, err := settingSecret(ctx, tx, f.what, f.key, f.aad, f.path)
		if err != nil {
			return nil, err
		}
		if s != nil {
			out = append(out, *s)
		}
	}

	// Passwords handed to in-flight operations (create, restore, rotate…).
	rows, err := tx.Query(ctx, `SELECT id, kind, project_id, params->'secrets'->>'password' FROM operations
		WHERE project_id IS NOT NULL AND coalesce(params->'secrets'->>'password', '') <> '' FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	type opRow struct {
		ID, Project uuid.UUID
		Kind, Sec   string
	}
	var ops []opRow
	for rows.Next() {
		var r opRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Project, &r.Sec); err != nil {
			rows.Close()
			return nil, err
		}
		ops = append(ops, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range ops {
		blob, err := base64.StdEncoding.DecodeString(r.Sec)
		if err != nil {
			return nil, fmt.Errorf("operation %s: password handoff: %w", r.ID, err)
		}
		id := r.ID
		out = append(out, secret{what: "operation password handoff", blob: blob, aad: opAAD(r.Kind, r.Project), save: func(tx pgx.Tx, b []byte) error {
			_, err := tx.Exec(ctx, `UPDATE operations SET params = jsonb_set(params, '{secrets,password}', to_jsonb($2::text)) WHERE id = $1`,
				id, base64.StdEncoding.EncodeToString(b))
			return err
		}})
	}
	return out, nil
}

// settingSecret finds a sealed blob at path inside a settings row (JSON
// encodes []byte as base64).
func settingSecret(ctx context.Context, tx pgx.Tx, what, key, aad string, path []string) (*secret, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1 FOR UPDATE`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	node := doc
	for _, p := range path[:len(path)-1] {
		next, ok := node[p].(map[string]any)
		if !ok {
			return nil, nil
		}
		node = next
	}
	leaf := path[len(path)-1]
	enc, ok := node[leaf].(string)
	if !ok || enc == "" {
		return nil, nil
	}
	blob, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return &secret{what: what, blob: blob, aad: []byte(aad), save: func(tx pgx.Tx, b []byte) error {
		// Re-read: two secrets can live in one row.
		var cur []byte
		if err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&cur); err != nil {
			return err
		}
		var d map[string]any
		if err := json.Unmarshal(cur, &d); err != nil {
			return err
		}
		n := d
		for _, p := range path[:len(path)-1] {
			n = n[p].(map[string]any)
		}
		n[leaf] = base64.StdEncoding.EncodeToString(b)
		out, err := json.Marshal(d)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE settings SET value = $2, updated_at = now() WHERE key = $1`, key, out)
		return err
	}}, nil
}
