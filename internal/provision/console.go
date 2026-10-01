package provision

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ConsoleRole names the login the SQL console and table browser use for a
// project database (spec §8.5). It can SET ROLE to the owner but inherits
// nothing, so RESET ROLE leaves a session with no privileges rather than
// the superuser's.
func ConsoleRole(dbName string) string { return dbName + "_console" }

// DropConsoleRole removes a console role, first revoking what it holds on
// database (when that database still exists).
func DropConsoleRole(ctx context.Context, conn *pgx.Conn, role, database string) error {
	var exists, dbExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1),
		EXISTS (SELECT 1 FROM pg_database WHERE datname = $2)`, role, database).Scan(&exists, &dbExists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if dbExists {
		if _, err := conn.Exec(ctx, "REVOKE ALL ON DATABASE "+ident(database)+" FROM "+ident(role)); err != nil {
			return err
		}
	}
	_, err := conn.Exec(ctx, "DROP ROLE "+ident(role))
	return err
}
