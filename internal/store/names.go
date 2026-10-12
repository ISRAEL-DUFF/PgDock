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

// Backend services' roles in the project whose database is db (V4 §2.3).
// Each is a login of its own: pgdock-edge connects as anon, user or
// service for a request and as the auth hook role for a hook, and as the
// edge login only for auth, so tenant SQL never runs where pgd_auth is
// readable and can't switch to another role.
func EdgeRole(db string) string    { return db + "_edge" }
func AnonRole(db string) string    { return db + "_anon" }
func UserRole(db string) string    { return db + "_user" }
func ServiceRole(db string) string { return db + "_service" }

// AuthHookRole runs the project's Postgres auth hooks (V4 §4.7): it holds
// only what the owner grants it.
func AuthHookRole(db string) string { return db + "_auth_hook" }

// RequestRoles are the logins tenant SQL runs as.
func RequestRoles(db string) []string {
	return []string{AnonRole(db), UserRole(db), ServiceRole(db), AuthHookRole(db)}
}

// ServiceRoles are all five, the edge login first.
func ServiceRoles(db string) []string {
	return []string{EdgeRole(db), AnonRole(db), UserRole(db), ServiceRole(db), AuthHookRole(db)}
}
