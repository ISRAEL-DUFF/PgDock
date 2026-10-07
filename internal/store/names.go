package store

// PoolerNames are the database names a project answers to on the poolers:
// its database, and the V1 alias existing connection strings use (V2
// §10.2). Pause, kill, and resume act on all of them.
func PoolerNames(p Project) []string {
	if p.AliasDbName != nil {
		return []string{p.DbName, *p.AliasDbName}
	}
	return []string{p.DbName}
}

// ClientDBName is the database name a project's owner connection string
// uses: the V1 alias until the project switches to opaque credentials,
// otherwise its (opaque) database.
func ClientDBName(p Project) string {
	if p.AliasDbName != nil && p.LegacyUntil == nil {
		return *p.AliasDbName
	}
	return p.DbName
}

// ProbeRole is the SLA probe login of the project whose database is db: it
// may connect and run SELECT 1, nothing else (V3 §2.7).
func ProbeRole(db string) string { return db + "_sla" }
