package provision

import "slices"

// SharedExtensions is the shared tier's extension allow-list (spec §7.4).
var SharedExtensions = []string{
	"pgcrypto", "uuid-ossp", "citext", "pg_trgm", "hstore", "unaccent",
	"btree_gin", "btree_gist", "pg_stat_statements", "vector", "hypopg",
}

// DedicatedExtensions are the extensions dedicated instances add.
var DedicatedExtensions = []string{"postgis", "pg_partman", "timescaledb", "postgres_fdw", "pg_cron"}

// AllowedExtensions is the allow-list for a tier.
func AllowedExtensions(tier string) []string {
	if tier == TierDedicated {
		return slices.Concat(SharedExtensions, DedicatedExtensions)
	}
	return SharedExtensions
}
