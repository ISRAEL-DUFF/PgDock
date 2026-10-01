// Package isocheck verifies the shared tier's tenant isolation checklist
// (spec §7.1) against a live cluster: its configuration, every project on
// it, and two throwaway tenants that try to reach each other. CI runs it
// against the test cluster; pgdock-server runs it weekly on every shared
// cluster as the isolation_check operation, and a failed run raises an
// alert.
package isocheck

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/israel-duff/pgdock/internal/provision"
	"github.com/israel-duff/pgdock/internal/store"
)

// Finding is one checklist item a cluster fails.
type Finding struct {
	Check  string `json:"check"`
	Detail string `json:"detail"`
}

func (f Finding) String() string { return f.Check + ": " + f.Detail }

// EscapeProne are the extensions and languages the shared tier never
// allows (spec §7.1).
var EscapeProne = []string{"plpython3u", "plperlu", "dblink", "postgres_fdw", "file_fdw", "adminpack"}

// Cluster checks cluster-wide settings over a superuser connection to the
// maintenance database: statement logging, pg_hba.conf, and the
// maintenance databases' grants.
func Cluster(ctx context.Context, admin *pgx.Conn) ([]Finding, error) {
	var out []Finding
	var logStatement, minDuration, logDuration string
	if err := admin.QueryRow(ctx, `SELECT current_setting('log_statement'), current_setting('log_min_duration_statement'),
		current_setting('log_duration')`).Scan(&logStatement, &minDuration, &logDuration); err != nil {
		return nil, err
	}
	if logStatement != "none" {
		out = append(out, Finding{"log_statement", fmt.Sprintf("is %q; project SQL may contain secrets, keep it none", logStatement)})
	}
	if minDuration == "0" {
		out = append(out, Finding{"log_min_duration_statement", "is 0, which logs every statement"})
	}
	if logDuration == "on" {
		out = append(out, Finding{"log_duration", "is on; only log_min_duration_statement should be set"})
	}

	rows, err := admin.Query(ctx, `SELECT line_number, type, coalesce(address, ''), coalesce(netmask, ''), coalesce(auth_method, ''), coalesce(error, '')
		FROM pg_hba_file_rules ORDER BY line_number`)
	if err != nil {
		return nil, err
	}
	type rule struct {
		Line                                int
		Type, Address, Netmask, Method, Err string
	}
	rules, err := pgx.CollectRows(rows, pgx.RowToStructByPos[rule])
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if f, ok := hbaFinding(r.Type, r.Address, r.Netmask, r.Method, r.Err); !ok {
			out = append(out, Finding{"pg_hba.conf", fmt.Sprintf("line %d: %s", r.Line, f)})
		}
	}

	for _, db := range []string{"postgres", "template1"} {
		var connect bool
		if err := admin.QueryRow(ctx, `SELECT has_database_privilege('public', $1, 'CONNECT')`, db).Scan(&connect); err != nil {
			return nil, err
		}
		if connect {
			out = append(out, Finding{"maintenance databases", "PUBLIC may connect to " + db})
		}
	}
	return out, nil
}

// hbaFinding reports whether a pg_hba.conf rule is acceptable: network
// rules must use SCRAM (or reject or cert), name explicit addresses, and
// use hostssl for anything outside private and loopback ranges.
func hbaFinding(typ, address, netmask, method, ruleErr string) (string, bool) {
	if ruleErr != "" {
		return "invalid rule: " + ruleErr, false
	}
	if typ == "local" {
		return "", true
	}
	switch method {
	case "scram-sha-256", "cert", "reject":
	default:
		return fmt.Sprintf("%s login uses %s, not scram-sha-256", typ, method), false
	}
	if method == "reject" {
		return "", true
	}
	switch address {
	case "all", "samenet", "":
		return fmt.Sprintf("accepts %s from any address (%q); name the poolers' and control plane's addresses", typ, address), false
	case "samehost":
		return "", true
	}
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return fmt.Sprintf("address %q is a host name; use addresses", address), false
	}
	bits := ip.BitLen()
	if netmask != "" {
		m, err := netip.ParseAddr(netmask)
		if err != nil {
			return fmt.Sprintf("netmask %q", netmask), false
		}
		bits = maskBits(m)
	}
	prefix := netip.PrefixFrom(ip, bits).Masked()
	if bits < 8 {
		return fmt.Sprintf("accepts logins from %s; name the poolers' and control plane's addresses", prefix), false
	}
	private := prefix.Addr().IsPrivate() || prefix.Addr().IsLoopback()
	if !private && typ != "hostssl" {
		return fmt.Sprintf("accepts non-TLS logins from %s, which is not a private network; use hostssl", prefix), false
	}
	return "", true
}

func maskBits(m netip.Addr) int {
	n := 0
	for _, b := range m.AsSlice() {
		for i := 7; i >= 0; i-- {
			if b&(1<<i) == 0 {
				return n
			}
			n++
		}
	}
	return n
}

// Tenant is a project database and its roles as the audit sees them.
type Tenant struct {
	DB, Role string
}

// Roles audits project roles (and their console logins): no dangerous
// attributes and no membership in predefined or privileged roles.
func Roles(ctx context.Context, admin *pgx.Conn, tenants []Tenant) ([]Finding, error) {
	var out []Finding
	for _, t := range tenants {
		for _, role := range []string{t.Role, provision.ConsoleRole(t.DB)} {
			var exists, super, createdb, createrole, repl, bypass bool
			err := admin.QueryRow(ctx, `SELECT true, rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls FROM pg_roles WHERE rolname = $1`, role).
				Scan(&exists, &super, &createdb, &createrole, &repl, &bypass)
			if errors.Is(err, pgx.ErrNoRows) {
				if role == t.Role {
					out = append(out, Finding{"project role", fmt.Sprintf("%s (database %s) does not exist", role, t.DB)})
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			var bad []string
			for name, on := range map[string]bool{"SUPERUSER": super, "CREATEDB": createdb, "CREATEROLE": createrole, "REPLICATION": repl, "BYPASSRLS": bypass} {
				if on {
					bad = append(bad, name)
				}
			}
			if len(bad) > 0 {
				out = append(out, Finding{"role attributes", fmt.Sprintf("%s has %s", role, strings.Join(bad, ", "))})
			}
			rows, err := admin.Query(ctx, `SELECT r.rolname FROM pg_roles r
				WHERE r.rolname <> $1 AND pg_has_role($1, r.oid, 'MEMBER')
				  AND (r.rolname LIKE 'pg\_%' OR r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls)
				ORDER BY 1`, role)
			if err != nil {
				return nil, err
			}
			member, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return nil, err
			}
			if len(member) > 0 {
				out = append(out, Finding{"role memberships", fmt.Sprintf("%s is a member of %s", role, strings.Join(member, ", "))})
			}
		}
		var connect, temp bool
		if err := admin.QueryRow(ctx, `SELECT has_database_privilege('public', $1, 'CONNECT'), has_database_privilege('public', $1, 'TEMPORARY')`, t.DB).
			Scan(&connect, &temp); err != nil {
			return nil, err
		}
		if connect || temp {
			out = append(out, Finding{"database grants", fmt.Sprintf("PUBLIC has CONNECT=%v TEMPORARY=%v on %s", connect, temp, t.DB)})
		}
	}
	return out, nil
}

// Database audits one project database over a superuser connection to it:
// CREATE on public revoked from PUBLIC, and no escape-prone extensions or
// untrusted languages installed.
func Database(ctx context.Context, conn *pgx.Conn) ([]Finding, error) {
	var out []Finding
	db := conn.Config().Database
	var create bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'public') AND has_schema_privilege('public', 'public', 'CREATE')`).Scan(&create); err != nil {
		return nil, err
	}
	if create {
		out = append(out, Finding{"schema public", "PUBLIC may CREATE in " + db})
	}
	rows, err := conn.Query(ctx, `SELECT extname FROM pg_extension WHERE extname = ANY($1)
		UNION SELECT lanname FROM pg_language WHERE NOT lanpltrusted AND lanname NOT IN ('internal', 'c')`, EscapeProne)
	if err != nil {
		return nil, err
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if len(found) > 0 {
		out = append(out, Finding{"escape-prone extensions", fmt.Sprintf("%s has %s installed", db, strings.Join(found, ", "))})
	}
	return out, nil
}

// connectAs opens a connection as a tenant at the instance's address.
func connectAs(ctx context.Context, ps *provision.Service, t store.Project, password, database string) (*pgx.Conn, error) {
	cfg, err := ps.AdminConfig(ctx, t.InstanceID, database)
	if err != nil {
		return nil, err
	}
	cfg.User, cfg.Password = t.OwnerRole, password
	cfg.RuntimeParams["application_name"] = "pgdock-isocheck"
	return pgx.ConnectConfig(ctx, cfg)
}

// Probes are the cross-tenant attempts of spec §7.1, made by tenant a
// against tenant b. Each must fail.
func Probes(ctx context.Context, ps *provision.Service, a, b store.Project, aPassword, bPassword string) ([]Finding, error) {
	var out []Finding
	fail := func(what string) { out = append(out, Finding{"cross-tenant", "tenant A could " + what}) }

	for _, db := range []string{b.DbName, "postgres", "template1"} {
		if c, err := connectAs(ctx, ps, a, aPassword, db); err == nil {
			_ = c.Close(context.Background())
			fail("connect to " + db)
		}
	}
	ca, err := connectAs(ctx, ps, a, aPassword, a.DbName)
	if err != nil {
		return nil, fmt.Errorf("tenant A cannot connect to its own database: %w", err)
	}
	defer ca.Close(context.Background())
	cb, err := connectAs(ctx, ps, b, bPassword, b.DbName)
	if err != nil {
		return nil, fmt.Errorf("tenant B cannot connect to its own database: %w", err)
	}
	defer cb.Close(context.Background())
	if _, err := cb.Exec(ctx, `CREATE TABLE secret (v text); INSERT INTO secret VALUES ('b-only')`); err != nil {
		return nil, err
	}
	var bPID int32
	if err := cb.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&bPID); err != nil {
		return nil, err
	}

	attempts := []struct{ what, sql string }{
		{"read server files", `SELECT pg_read_file('/etc/passwd')`},
		{"list the data directory", `SELECT pg_ls_dir('.')`},
		{"run programs", `CREATE TEMP TABLE sink (l text); COPY sink FROM PROGRAM 'id'`},
		{"read files with COPY", `CREATE TEMP TABLE sink2 (l text); COPY sink2 FROM '/etc/passwd'`},
		{"import server files as large objects", `SELECT lo_import('/etc/passwd')`},
		{"take B's role", "SET ROLE " + pgx.Identifier{b.OwnerRole}.Sanitize()},
		{"grant itself B's role", "GRANT " + pgx.Identifier{b.OwnerRole}.Sanitize() + " TO " + pgx.Identifier{a.OwnerRole}.Sanitize()},
		{"change B's role", "ALTER ROLE " + pgx.Identifier{b.OwnerRole}.Sanitize() + " PASSWORD 'x'"},
		{"drop B's database", "DROP DATABASE " + pgx.Identifier{b.DbName}.Sanitize()},
		{"end B's session", fmt.Sprintf(`SELECT CASE WHEN pg_terminate_backend(%d) THEN 1 ELSE 1/0 END`, bPID)},
		{"create a role", `CREATE ROLE pgdock_isocheck_escape LOGIN`},
		{"create a database", `CREATE DATABASE pgdock_isocheck_escape`},
		{"create an untrusted function", `CREATE FUNCTION escape() RETURNS int AS 'libc.so.6', 'getpid' LANGUAGE c`},
		{"change superuser-only settings", `SET log_statement = 'none'`},
	}
	for _, x := range EscapeProne {
		attempts = append(attempts, struct{ what, sql string }{"create " + x, "CREATE EXTENSION " + x})
	}
	for _, at := range attempts {
		if _, err := ca.Exec(ctx, at.sql); err == nil {
			fail(at.what)
		} else if !isRefusal(err) {
			return nil, fmt.Errorf("%s: unexpected error: %w", at.what, err)
		}
	}

	// B's queries and data stay invisible.
	var visible int
	if err := ca.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid = $1 AND query IS NOT NULL AND query <> '<insufficient privilege>'`, bPID).Scan(&visible); err != nil {
		return nil, err
	}
	if visible > 0 {
		fail("see B's queries in pg_stat_activity")
	}
	var predefined []string
	rows, err := ca.Query(ctx, `SELECT rolname FROM pg_roles WHERE rolname IN ('pg_read_all_data', 'pg_write_all_data', 'pg_read_server_files',
		'pg_write_server_files', 'pg_execute_server_program', 'pg_read_all_settings', 'pg_read_all_stats', 'pg_monitor', 'pg_signal_backend')
		AND pg_has_role(current_user, oid, 'MEMBER')`)
	if err != nil {
		return nil, err
	}
	if predefined, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, err
	}
	if len(predefined) > 0 {
		fail("read B's data through " + strings.Join(predefined, ", "))
	}
	return out, nil
}

// isRefusal is an error Postgres returns for a refused action (rather than
// a broken check).
func isRefusal(err error) bool {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return false
	}
	switch pe.Code[:2] {
	case "42", "0A", "58", "22", "25", "55", "3D", "28":
		// insufficient_privilege and syntax/undefined (42xxx), feature not
		// supported, missing files, division by zero (our CASE), invalid
		// state, object in use, unknown database, auth.
		return true
	}
	return false
}

// Instance runs the whole check against one shared instance: cluster
// configuration, every project on it, and two throwaway tenants.
func Instance(ctx context.Context, db store.DBTX, ps *provision.Service, inst store.ListLiveSharedInstancesRow, log func(string, ...any)) ([]Finding, error) {
	admin, err := ps.AdminConn(ctx, inst.ID, "postgres")
	if err != nil {
		return nil, err
	}
	defer admin.Close(context.Background())
	findings, err := Cluster(ctx, admin)
	if err != nil {
		return nil, fmt.Errorf("cluster settings: %w", err)
	}
	log("cluster settings: %d finding(s)", len(findings))

	ps2, err := store.New(db).InstanceProjects(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	tenants := make([]Tenant, len(ps2))
	for i, p := range ps2 {
		tenants[i] = Tenant{DB: p.DbName, Role: p.OwnerRole}
	}
	fs, err := Roles(ctx, admin, tenants)
	if err != nil {
		return nil, fmt.Errorf("project roles: %w", err)
	}
	findings = append(findings, fs...)
	for _, t := range tenants {
		c, err := ps.AdminConn(ctx, inst.ID, t.DB)
		if err != nil {
			findings = append(findings, Finding{"project database", fmt.Sprintf("cannot audit %s: %v", t.DB, err)})
			continue
		}
		fs, err := Database(ctx, c)
		_ = c.Close(context.Background())
		if err != nil {
			return nil, fmt.Errorf("database %s: %w", t.DB, err)
		}
		findings = append(findings, fs...)
	}
	log("%d project(s) audited", len(tenants))

	if err := ps.DropProbes(ctx, inst.ID); err != nil {
		return nil, fmt.Errorf("clear old probes: %w", err)
	}
	defer func() { _ = ps.DropProbes(context.WithoutCancel(ctx), inst.ID) }()
	a, aPW, err := ps.NewProbe(ctx, inst.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("provision tenant A: %w", err)
	}
	b, bPW, err := ps.NewProbe(ctx, inst.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("provision tenant B: %w", err)
	}
	fs, err = Roles(ctx, admin, []Tenant{{a.DbName, a.OwnerRole}, {b.DbName, b.OwnerRole}})
	if err != nil {
		return nil, err
	}
	findings = append(findings, fs...)
	probe, err := Probes(ctx, ps, a, b, aPW, bPW)
	if err != nil {
		return nil, fmt.Errorf("cross-tenant probes: %w", err)
	}
	log("cross-tenant probes: %d finding(s)", len(probe))
	return append(findings, probe...), nil
}
