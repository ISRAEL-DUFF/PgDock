package provision

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// activityPrivacy hides other sessions from tenants on a shared cluster
// (V2 §10.2): pg_stat_activity, and the function behind it, are readable
// only by roles granted pg_read_all_stats (PGDock's admin). Catalog ACLs
// are per database, so this runs in every project database.
var activityPrivacy = []string{
	"REVOKE SELECT ON pg_catalog.pg_stat_activity FROM PUBLIC",
	"REVOKE EXECUTE ON FUNCTION pg_catalog.pg_stat_get_activity(integer) FROM PUBLIC",
}

// RestrictActivity applies the activity restriction in conn's database.
func RestrictActivity(ctx context.Context, conn *pgx.Conn) error {
	for _, stmt := range activityPrivacy {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("restrict pg_stat_activity: %w", err)
		}
	}
	return nil
}
