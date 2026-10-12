# Query insights

Query insights (V3 §8) show which queries take a project's time, how they
run, and which indexes would help. They are under **Project → Query
insights** (and `pgdock insights`), for **Pro and Team** organisations'
shared projects and every **dedicated** project. Other projects get
"plan_required" with a pointer to Billing.

## How it is collected

`pg_stat_statements` is loaded on every shared cluster and dedicated
instance. Every 5 minutes (`PGDOCK_INSIGHTS_INTERVAL`) pgdock-server
reads it as the instance's admin, which sees every row, and keeps each
project's own statements:

- the work done since the last read, per query (calls, time, rows, cache
  hits and reads, the slowest call), in 5-minute buckets for a day, then
  hourly, for 30 days;
- the normalised text, and the latest example with its literals, seen in
  `pg_stat_activity` when the statistics were read (only statements sent
  with their literals inline: an extended-protocol statement shows `$1`
  there, so it has no example).

Tenants never read the view themselves on shared clusters. PGDock's own
sessions (backups, the SQL console, moves) aren't counted. The first read
of an instance is the baseline: work from before PGDock looked isn't
shown.

## The tabs

- **Queries:** the top queries over 1 hour to 30 days, by total time, mean
  time, calls or rows, with each one's share of the project's time. A
  query opens with its mean time and calls over the range and its latest
  example. **Explain** runs `EXPLAIN` (never `ANALYZE`: nothing runs) as
  the project's role in a read-only transaction, on the example, or as a
  generic plan (`GENERIC_PLAN`, parameters unknown) when there is none.
- **Slow queries:** statements over `PGDOCK_SLOW_QUERY_MS` (default
  1000): seen running past it when the statistics were read; a call that
  slow between two reads (from pg_stat_statements' maximum and the
  interval's mean); and statements the shared tier's reaper cancelled.
  Dedicated instances use the same method rather than parsing the server
  log.
- **Indexes:**
  - **Suggestions** come from the plans of the top 10 queries of the last
    day (sequential scans of tables over 10,000 rows that filter on
    columns no index leads with) and from foreign keys without an index.
    Each has its `CREATE INDEX CONCURRENTLY` statement and opens in the
    table editor's review, which can run it or **save it as a migration**
    (plain SQL, goose or dbmate).
  - With the **hypopg** extension enabled (Database → Extensions; it is
    in every dedicated instance's image, and on shared clusters where the
    platform installed it), each suggestion shows the planner's cost of
    those queries with and without a hypothetical index.
  - **Unused indexes** (never scanned since the statistics were last
    reset, not backing a constraint), **duplicate indexes** (identical,
    or a plain btree whose columns lead another), and large tables read in
    full with no filter column to suggest.
- **Bloat:** each table over 1 MB, its size against the size its rows
  need (from `pg_stats` widths and the fillfactor), and dead rows. **Reclaim
  space** runs `VACUUM FULL` as an operation (it locks the table).
- **Locks:** sessions waiting on a lock, the lock, and the sessions
  holding it, refreshed every 5 seconds.

## Settings

| Variable | Default | |
| --- | --- | --- |
| `PGDOCK_INSIGHTS_PLANS` | `pro,team` | Plans whose shared projects get insights; `all` for installations without billing. |
| `PGDOCK_INSIGHTS_INTERVAL` | `5m` | How often pg_stat_statements is read. |
| `PGDOCK_SLOW_QUERY_MS` | `1000` | The slow-query threshold. |

`pg_stat_statements.max` (5000 by default) bounds how many distinct
statements a cluster tracks; on a busy shared cluster the rarest are
evicted between reads and miss an interval.
