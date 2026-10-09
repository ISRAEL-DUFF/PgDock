import { PgdockError, type Result, fail, ok } from "./errors.js";
import { json, send, type Transport } from "./http.js";

// ---- Types from `pgdock gen types --lang ts` ----------------------------------

/** A table's generated types. */
export interface GenericTable {
  Row: Record<string, unknown>;
  Insert: Record<string, unknown>;
  Update: Record<string, unknown>;
}

/** A schema's generated types. */
export interface GenericSchema {
  Tables: Record<string, GenericTable>;
  Views: Record<string, { Row: Record<string, unknown> }>;
  Functions: Record<string, { Args: Record<string, unknown>; Returns: unknown }>;
}

/** Any database: rows are plain objects. */
export interface AnyDatabase {
  [schema: string]: GenericSchema;
}

type TableNames<S extends GenericSchema> = Extract<keyof S["Tables"] | keyof S["Views"], string>;

export type RowOf<S extends GenericSchema, T extends string> = T extends keyof S["Tables"]
  ? S["Tables"][T]["Row"]
  : T extends keyof S["Views"]
    ? S["Views"][T]["Row"]
    : Record<string, unknown>;

export type InsertOf<S extends GenericSchema, T extends string> = T extends keyof S["Tables"]
  ? S["Tables"][T]["Insert"]
  : Record<string, unknown>;

export type UpdateOf<S extends GenericSchema, T extends string> = T extends keyof S["Tables"]
  ? S["Tables"][T]["Update"]
  : Record<string, unknown>;

// ---- Filters --------------------------------------------------------------------

export type Operator =
  | "eq"
  | "neq"
  | "lt"
  | "lte"
  | "gt"
  | "gte"
  | "in"
  | "like"
  | "ilike"
  | "is"
  | "contains"
  | "contained_by"
  | "search";

/** A condition, or a group of them (the JSON query form). */
export type Filter =
  | { column: string; op: Operator; value: unknown }
  | { and: Filter[] }
  | { or: Filter[] }
  | { not: Filter };

/** cond makes a condition for or(), not() and and(). */
export function cond(column: string, op: Operator, value: unknown): Filter {
  return { column, op, value };
}

function scalar(v: unknown): string {
  if (v === null) return "null";
  if (v instanceof Date) return v.toISOString();
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

function listItem(v: unknown): string {
  const s = scalar(v);
  return /[",\\]/.test(s) ? '"' + s.replace(/[\\"]/g, (c) => "\\" + c) + '"' : s;
}

/** The URL form of a condition: column:op:value. */
export function encodeCondition(column: string, op: Operator, value: unknown): string {
  if (op === "in") {
    const items = Array.isArray(value) ? value : [value];
    return `${column}:in:${items.map(listItem).join(",")}`;
  }
  return `${column}:${op}:${scalar(value)}`;
}

function jsonValue(op: Operator, v: unknown): unknown {
  if (op === "in") return (Array.isArray(v) ? v : [v]).map((x) => (x instanceof Date ? x.toISOString() : x));
  if (v instanceof Date) return v.toISOString();
  return v;
}

function jsonFilter(f: Filter): unknown {
  if ("and" in f) return { and: f.and.map(jsonFilter) };
  if ("or" in f) return { or: f.or.map(jsonFilter) };
  if ("not" in f) return { not: jsonFilter(f.not) };
  return { column: f.column, op: f.op, value: jsonValue(f.op, f.value) };
}

// ---- Results --------------------------------------------------------------------

/** A page of rows. */
export type ListResult<Row> =
  | { data: Row[]; error: null; count: number | null; nextCursor: string | null }
  | { data: null; error: PgdockError; count: null; nextCursor: null };

/** Written rows (none with returning: "minimal"). */
export type WriteResult<Row> =
  | { data: Row[]; error: null; affected: number }
  | { data: null; error: PgdockError; affected: null };

interface Page<Row> {
  data: Row[];
  count?: number | null;
  next_cursor?: string | null;
}

// ---- Builders -------------------------------------------------------------------

/** What every builder runs with. */
export interface DataContext {
  t: Transport;
  /** Subscribes to a table's changes, for live(); returns the unsubscribe. */
  watch?: (schema: string, table: string, onChange: () => void) => () => void;
}

/** The filter methods shared by reads, updates and deletes. */
abstract class Filtered<Self> {
  protected conds: { column: string; op: Operator; value: unknown }[] = [];
  protected groups: Filter[] = [];

  /** Rows where column op value (all conditions must hold). */
  where(column: string, op: Operator, value: unknown): Self {
    this.conds.push({ column, op, value });
    return this as unknown as Self;
  }
  eq(column: string, value: unknown): Self {
    return this.where(column, "eq", value);
  }
  neq(column: string, value: unknown): Self {
    return this.where(column, "neq", value);
  }
  gt(column: string, value: unknown): Self {
    return this.where(column, "gt", value);
  }
  gte(column: string, value: unknown): Self {
    return this.where(column, "gte", value);
  }
  lt(column: string, value: unknown): Self {
    return this.where(column, "lt", value);
  }
  lte(column: string, value: unknown): Self {
    return this.where(column, "lte", value);
  }
  in(column: string, values: unknown[]): Self {
    return this.where(column, "in", values);
  }
  like(column: string, pattern: string): Self {
    return this.where(column, "like", pattern);
  }
  ilike(column: string, pattern: string): Self {
    return this.where(column, "ilike", pattern);
  }
  is(column: string, value: null | boolean): Self {
    return this.where(column, "is", value);
  }
  contains(column: string, value: unknown): Self {
    return this.where(column, "contains", value);
  }
  search(column: string, query: string): Self {
    return this.where(column, "search", query);
  }
  /** One group of which any condition may hold: or(cond(…), cond(…)). */
  or(...any: Filter[]): Self {
    this.groups.push({ or: any });
    return this as unknown as Self;
  }
  /** Rows where f doesn't hold. */
  not(f: Filter): Self {
    this.groups.push({ not: f });
    return this as unknown as Self;
  }

  /** Whether the URL form can say it (no not(), no nesting in or()). */
  protected simple(): boolean {
    return this.groups.every((g) => "or" in g && g.or.every((x) => "column" in x && !/,/.test(scalar(x.value))));
  }
  protected urlFilters(): { where?: string[]; or?: string[] } {
    const where = this.conds.map((c) => encodeCondition(c.column, c.op, c.value));
    const or = this.groups.map((g) =>
      (g as { or: { column: string; op: Operator; value: unknown }[] }).or
        .map((c) => encodeCondition(c.column, c.op, c.value))
        .join(","),
    );
    return { where: where.length ? where : undefined, or: or.length ? or : undefined };
  }
  protected jsonWhere(): unknown {
    const all: Filter[] = [...this.conds.map((c) => cond(c.column, c.op, c.value)), ...this.groups];
    return all.length ? jsonFilter({ and: all }) : undefined;
  }
}

/** A read: data.from("todos").select("id,title").eq("done", false). */
export class SelectQuery<Row> extends Filtered<SelectQuery<Row>> implements PromiseLike<ListResult<Row>> {
  private orders: string[] = [];
  private lim?: number;
  private off?: number;
  private cur?: string;
  private cnt?: "exact" | "estimated";
  private replicaOK = false;

  constructor(
    private ctx: DataContext,
    private table: string,
    private schema: string,
    private columns: string,
  ) {
    super();
  }

  /** Sort by column ("asc" by default); call again for more columns. */
  order(column: string, direction: "asc" | "desc" = "asc"): this {
    this.orders.push(`${column}:${direction}`);
    return this;
  }
  /** At most n rows (100 by default, 1,000 at most). */
  limit(n: number): this {
    this.lim = n;
    return this;
  }
  offset(n: number): this {
    this.off = n;
    return this;
  }
  /** The page after a previous page's nextCursor. */
  cursor(c: string | null | undefined): this {
    this.cur = c ?? undefined;
    return this;
  }
  /** Also count the matching rows. */
  count(kind: "exact" | "estimated" = "exact"): this {
    this.cnt = kind;
    return this;
  }
  /** May be served by a read replica (a little behind the primary). */
  replica(allowed = true): this {
    this.replicaOK = allowed;
    return this;
  }

  private path(): string {
    return "/data/v1/" + encodeURIComponent(this.schema === "public" ? this.table : `${this.schema}.${this.table}`);
  }

  async run(signal?: AbortSignal): Promise<ListResult<Row>> {
    const headers: Record<string, string> = this.replicaOK ? { "Read-Replica": "allowed" } : {};
    let r: Result<Page<Row>>;
    if (this.simple()) {
      r = await json<Page<Row>>(this.ctx.t, this.path(), {
        query: {
          select: this.columns,
          ...this.urlFilters(),
          order: this.orders.length ? this.orders.join(",") : undefined,
          limit: this.lim,
          offset: this.off,
          cursor: this.cur,
          count: this.cnt,
        },
        headers,
        signal,
      });
    } else {
      r = await json<Page<Row>>(this.ctx.t, this.path() + "/query", {
        body: {
          select: this.columns,
          where: this.jsonWhere(),
          order: this.orders.length ? this.orders.join(",") : undefined,
          limit: this.lim,
          offset: this.off,
          cursor: this.cur,
          count: this.cnt,
        },
        headers,
        signal,
      });
    }
    if (r.error) return { data: null, error: r.error, count: null, nextCursor: null };
    return { data: r.data.data, error: null, count: r.data.count ?? null, nextCursor: r.data.next_cursor ?? null };
  }

  then<A = ListResult<Row>, B = never>(
    onfulfilled?: ((v: ListResult<Row>) => A | PromiseLike<A>) | null,
    onrejected?: ((e: unknown) => B | PromiseLike<B>) | null,
  ): PromiseLike<A | B> {
    return this.run().then(onfulfilled, onrejected);
  }

  /**
   * Keeps the result current: runs the query now and again whenever the
   * table changes (as realtime reports it), after a resync, and after a
   * reconnect. Turn realtime on for the table first. Returns the stop
   * function.
   */
  live(onResult: (r: ListResult<Row>) => void, opts: { debounceMs?: number } = {}): () => void {
    if (!this.ctx.watch) throw new PgdockError(0, "realtime_unavailable", "live() needs realtime (a WebSocket)");
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;
    let seq = 0;
    const refresh = () => {
      const mine = ++seq;
      void this.run().then((r) => {
        if (!stopped && mine === seq) onResult(r);
      });
    };
    const soon = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(refresh, opts.debounceMs ?? 100);
    };
    const unwatch = this.ctx.watch(this.schema, this.table, soon);
    refresh();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      unwatch();
    };
  }
}

/** One row by its primary key: data.from("todos").get(42). */
export class RowQuery<Row> implements PromiseLike<Result<Row>> {
  constructor(
    private ctx: DataContext,
    private path: string,
    private columns: string,
  ) {}

  async run(): Promise<Result<Row>> {
    const r = await json<{ data: Row }>(this.ctx.t, this.path, { query: { select: this.columns } });
    return r.error ? r : ok(r.data.data);
  }

  then<A = Result<Row>, B = never>(
    onfulfilled?: ((v: Result<Row>) => A | PromiseLike<A>) | null,
    onrejected?: ((e: unknown) => B | PromiseLike<B>) | null,
  ): PromiseLike<A | B> {
    return this.run().then(onfulfilled, onrejected);
  }
}

export interface WriteOptions {
  /** Columns of the written rows to return. */
  select?: string;
  /** "minimal" returns only the count. */
  returning?: "representation" | "minimal";
}

/** An insert, update or delete. */
export class WriteQuery<Row> extends Filtered<WriteQuery<Row>> implements PromiseLike<WriteResult<Row>> {
  private maxAffected?: number;
  private key?: string | number;

  constructor(
    private ctx: DataContext,
    private method: "POST" | "PATCH" | "DELETE",
    private base: string,
    private body: unknown,
    private opts: WriteOptions & { onConflict?: string[]; ignore?: boolean } = {},
  ) {
    super();
  }

  /** The row with this primary key (instead of filters). */
  byKey(key: string | number): this {
    this.key = key;
    return this;
  }
  /** Refuse, changing nothing, if more than n rows would change. */
  atMost(n: number): this {
    this.maxAffected = n;
    return this;
  }

  async run(): Promise<WriteResult<Row>> {
    if (!this.simple()) {
      return {
        data: null,
        error: new PgdockError(0, "invalid_filter", "updates and deletes take where() and or() groups of conditions; use data.batch for more"),
        affected: null,
      };
    }
    const path = this.key !== undefined ? `${this.base}/${encodeURIComponent(String(this.key))}` : this.base;
    const r = await json<{ affected: number; data?: Row[] }>(this.ctx.t, path, {
      method: this.method,
      body: this.body,
      query: {
        ...this.urlFilters(),
        select: this.opts.select,
        return: this.opts.returning,
        on_conflict: this.opts.onConflict?.join(","),
        resolution: this.opts.ignore ? "ignore" : undefined,
        max_affected: this.maxAffected,
      },
    });
    if (r.error) return { data: null, error: r.error, affected: null };
    return { data: r.data.data ?? [], error: null, affected: r.data.affected };
  }

  then<A = WriteResult<Row>, B = never>(
    onfulfilled?: ((v: WriteResult<Row>) => A | PromiseLike<A>) | null,
    onrejected?: ((e: unknown) => B | PromiseLike<B>) | null,
  ): PromiseLike<A | B> {
    return this.run().then(onfulfilled, onrejected);
  }
}

/** A table or view of the exposed schemas. */
export class Table<Row, Ins, Upd> {
  constructor(
    private ctx: DataContext,
    private table: string,
    private schema: string,
  ) {}

  private get base(): string {
    return "/data/v1/" + encodeURIComponent(this.schema === "public" ? this.table : `${this.schema}.${this.table}`);
  }

  /** Read rows: columns, related rows (author(name)), JSON paths. */
  select(columns = "*"): SelectQuery<Row> {
    return new SelectQuery<Row>(this.ctx, this.table, this.schema, columns);
  }
  /** One row by its (single-column) primary key; 404 not_found when hidden or absent. */
  get(key: string | number, columns = "*"): RowQuery<Row> {
    return new RowQuery<Row>(this.ctx, `${this.base}/${encodeURIComponent(String(key))}`, columns);
  }
  /** Insert one row or a list (up to 1,000); the new rows come back. */
  insert(rows: Ins | Ins[], opts: WriteOptions = {}): WriteQuery<Row> {
    return new WriteQuery<Row>(this.ctx, "POST", this.base, rows, opts);
  }
  /** Insert, or update the rows that conflict on onConflict's columns (or leave them, with ignore). */
  upsert(rows: Ins | Ins[], opts: WriteOptions & { onConflict: string[]; ignore?: boolean }): WriteQuery<Row> {
    return new WriteQuery<Row>(this.ctx, "POST", this.base, rows, opts);
  }
  /** Change the rows a filter (or byKey) picks. */
  update(values: Upd, opts: WriteOptions = {}): WriteQuery<Row> {
    return new WriteQuery<Row>(this.ctx, "PATCH", this.base, values, opts);
  }
  /** Delete the rows a filter (or byKey) picks. */
  delete(opts: WriteOptions = {}): WriteQuery<Row> {
    return new WriteQuery<Row>(this.ctx, "DELETE", this.base, undefined, opts);
  }
}

/** One write of a batch. */
export type BatchOp =
  | { op: "insert"; table: string; rows: unknown; return?: "representation" | "minimal" }
  | { op: "upsert"; table: string; rows: unknown; on_conflict: string[]; return?: "representation" | "minimal" }
  | { op: "update"; table: string; set: Record<string, unknown>; where?: Filter; key?: string | number }
  | { op: "delete"; table: string; where?: Filter; key?: string | number; return?: "representation" | "minimal" };

/** client.data: tables, functions and batches. */
export class DataClient<DB extends AnyDatabase = AnyDatabase, SchemaName extends Extract<keyof DB, string> = "public" & Extract<keyof DB, string>> {
  constructor(
    private ctx: DataContext,
    private schemaName: string = "public",
  ) {}

  /** A table or view (another exposed schema: from("api.items"), or schema()). */
  from<T extends TableNames<DB[SchemaName]> | (string & {})>(
    table: T,
  ): Table<RowOf<DB[SchemaName], T>, InsertOf<DB[SchemaName], T>, UpdateOf<DB[SchemaName], T>> {
    let schema = this.schemaName;
    let name: string = table;
    const dot = name.indexOf(".");
    if (dot > 0) {
      schema = name.slice(0, dot);
      name = name.slice(dot + 1);
    }
    return new Table(this.ctx, name, schema);
  }

  /** The same client on another exposed schema. */
  schema<S extends Extract<keyof DB, string>>(name: S): DataClient<DB, S> {
    return new DataClient<DB, S>(this.ctx, name);
  }

  /**
   * Calls a function as the caller. STABLE and IMMUTABLE functions may be
   * called with { get: true } (cacheable, and replicas can serve them).
   */
  async rpc<F extends Extract<keyof DB[SchemaName]["Functions"], string> | (string & {})>(
    fn: F,
    args: F extends keyof DB[SchemaName]["Functions"] ? DB[SchemaName]["Functions"][F]["Args"] : Record<string, unknown> = {} as never,
    opts: { get?: boolean } = {},
  ): Promise<Result<F extends keyof DB[SchemaName]["Functions"] ? DB[SchemaName]["Functions"][F]["Returns"] : unknown>> {
    const name = this.schemaName === "public" ? fn : `${this.schemaName}.${fn}`;
    const path = "/data/v1/rpc/" + encodeURIComponent(name);
    const a = args as Record<string, unknown>;
    const r = opts.get
      ? await json<{ data: never }>(this.ctx.t, path, {
          query: Object.fromEntries(Object.entries(a).map(([k, v]) => [k, scalar(v)])),
        })
      : await json<{ data: never }>(this.ctx.t, path, { body: a });
    return r.error ? r : ok(r.data.data);
  }

  /** Up to 50 writes in one transaction: all happen or none do. */
  async batch(operations: BatchOp[]): Promise<Result<unknown[]>> {
    const ops = operations.map((o) => ("where" in o && o.where ? { ...o, where: jsonFilter(o.where) } : o));
    const r = await json<{ results: unknown[] }>(this.ctx.t, "/data/v1/batch", { body: { operations: ops } });
    return r.error ? r : ok(r.data.results);
  }

  /** Whether the API answers, and as whom. */
  async health(): Promise<Result<{ status: string; project: string; role: string; region: string; replica?: boolean }>> {
    const r = await send(this.ctx.t, "/data/v1/health");
    if (r.error) return fail(r.error);
    return ok(await r.data.json());
  }
}
