package logical

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5"
)

// Removal is something a Postgres major removed or renamed that a schema
// may still use (V4.1 §6.2): found in functions, views or settings, it is
// a warning on the upgrade, not a block (the schema trial decides that).
type Removal struct {
	Major int    // the release that removed it
	Name  string // the identifier, matched as a whole word
	What  string
}

// Removals are the known removals, by release. Kept short and certain:
// each is checked against that release's catalog.
var Removals = []Removal{
	// Postgres 17: checkpoint counters left pg_stat_bgwriter.
	{17, "buffers_checkpoint", "moved from pg_stat_bgwriter to pg_stat_checkpointer (as buffers_written)"},
	{17, "checkpoints_timed", "moved from pg_stat_bgwriter to pg_stat_checkpointer (as num_timed)"},
	{17, "checkpoints_req", "moved from pg_stat_bgwriter to pg_stat_checkpointer (as num_requested)"},
	{17, "checkpoint_write_time", "moved from pg_stat_bgwriter to pg_stat_checkpointer (as write_time)"},
	{17, "checkpoint_sync_time", "moved from pg_stat_bgwriter to pg_stat_checkpointer (as sync_time)"},
	{17, "buffers_backend", "removed from pg_stat_bgwriter (see pg_stat_io)"},
	{17, "buffers_backend_fsync", "removed from pg_stat_bgwriter (see pg_stat_io)"},
	{17, "blk_read_time", "renamed in pg_stat_statements to shared_blk_read_time"},
	{17, "blk_write_time", "renamed in pg_stat_statements to shared_blk_write_time"},
	{17, "old_snapshot_threshold", "the setting was removed"},
	{17, "db_user_namespace", "the setting was removed"},
	// Postgres 18: I/O timings left pg_stat_wal for pg_stat_io.
	{18, "wal_write", "removed from pg_stat_wal (see pg_stat_io)"},
	{18, "wal_sync", "removed from pg_stat_wal (see pg_stat_io)"},
	{18, "wal_write_time", "removed from pg_stat_wal (see pg_stat_io)"},
	{18, "wal_sync_time", "removed from pg_stat_wal (see pg_stat_io)"},
}

// Finding is a removal found in a schema.
type Finding struct {
	Object  string // "view public.x", "function public.f(int)", "setting"
	Removal Removal
}

func (f Finding) String() string {
	return fmt.Sprintf("%s uses %s: %s in Postgres %d", f.Object, f.Removal.Name, f.Removal.What, f.Removal.Major)
}

// ScanDeprecated looks in db's functions, views and database settings for
// what Postgres releases after from up to to removed.
func ScanDeprecated(ctx context.Context, db *pgx.Conn, from, to int) ([]Finding, error) {
	var rules []Removal
	for _, r := range Removals {
		if r.Major > from && r.Major <= to {
			rules = append(rules, r)
		}
	}
	if len(rules) == 0 {
		return nil, nil
	}
	// The project's own objects: not the catalogs, not PGDock's schemas,
	// not what extensions installed.
	rows, err := db.Query(ctx, `
		SELECT 'function ' || format('%I.%I(%s)', n.nspname, p.proname, pg_get_function_identity_arguments(p.oid)), p.prosrc
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'pgdock', 'pgdock_move', 'pgd_auth', 'pgd_storage', 'pgd_realtime')
		  AND n.nspname NOT LIKE 'pg_toast%' AND p.prolang <> (SELECT oid FROM pg_language WHERE lanname = 'c')
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
		UNION ALL
		SELECT (CASE c.relkind WHEN 'm' THEN 'materialized view ' ELSE 'view ' END) || format('%I.%I', n.nspname, c.relname), pg_get_viewdef(c.oid)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('v', 'm') AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pgdock', 'pgdock_move', 'pgd_auth', 'pgd_storage', 'pgd_realtime')
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
		UNION ALL
		SELECT 'setting', array_to_string(s.setconfig, ' ')
		FROM pg_db_role_setting s WHERE s.setdatabase = (SELECT oid FROM pg_database WHERE datname = current_database())`)
	if err != nil {
		return nil, err
	}
	type obj struct{ name, text string }
	objs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (obj, error) {
		var o obj
		var text *string
		if err := r.Scan(&o.name, &text); err != nil {
			return o, err
		}
		if text != nil {
			o.text = *text
		}
		return o, nil
	})
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, r := range rules {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(r.Name) + `\b`)
		for _, o := range objs {
			if re.MatchString(o.text) {
				out = append(out, Finding{Object: o.name, Removal: r})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Object < out[j].Object })
	return out, nil
}
