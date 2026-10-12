import { AuthClient, memoryStorage, type AuthOptions } from "./auth.js";
import { DataClient, type AnyDatabase } from "./data.js";
import type { Fetch, Transport } from "./http.js";
import { RealtimeClient, type RealtimeOptions } from "./realtime.js";
import { StorageClient } from "./storage.js";

export * from "./auth.js";
export * from "./data.js";
export * from "./errors.js";
export * from "./realtime.js";
export * from "./storage.js";

export interface ClientOptions {
  auth?: AuthOptions;
  realtime?: RealtimeOptions;
  /** A fetch implementation (the global one by default). */
  fetch?: Fetch;
  /** Headers sent with every request. */
  headers?: Record<string, string>;
  /** The exposed schema data.from() reads (public by default). */
  schema?: string;
  /**
   * Act as the user with this access token instead of a stored session:
   * on a server, the token from the request's cookie or header (verify it
   * first). The client then neither stores nor refreshes a session.
   */
  accessToken?: () =>
    | string
    | null
    | undefined
    | Promise<string | null | undefined>;
}

/** A project's client: data, auth, storage and realtime. */
export class PgdockClient<DB extends AnyDatabase = AnyDatabase> {
  readonly auth: AuthClient;
  readonly data: DataClient<DB>;
  readonly storage: StorageClient;
  readonly realtime: RealtimeClient;

  constructor(
    readonly url: string,
    readonly key: string,
    opts: ClientOptions = {},
  ) {
    if (!/^https?:\/\//.test(url))
      throw new TypeError("the project URL is https://<ref>.<domain>");
    if (!key) throw new TypeError("the project's publishable key is required");
    const f = opts.fetch ?? globalThis.fetch.bind(globalThis);
    const base: Omit<Transport, "token"> = {
      url,
      key,
      fetch: f,
      headers: { "X-Client-Info": "pgdock-js/0.1.0", ...opts.headers },
    };
    // A secret key acts as the service role: no user session.
    const secret = key.startsWith("pgd_sec_");
    const external = opts.accessToken;
    const sessionless = secret || !!external;
    this.auth = new AuthClient(
      base,
      sessionless
        ? {
            storage: memoryStorage(),
            persistSession: false,
            autoRefresh: false,
            detectSessionInUrl: false,
            ...opts.auth,
          }
        : opts.auth,
    );
    const t: Transport = external
      ? { ...base, token: async () => (await external()) ?? null }
      : secret
        ? { ...base, token: async () => null }
        : this.auth.transport();
    this.realtime = new RealtimeClient(
      url,
      key,
      () => t.token(),
      opts.realtime,
    );
    this.auth.onAuthStateChange((event, session) => {
      if (
        event === "TOKEN_REFRESHED" ||
        event === "SIGNED_IN" ||
        event === "SIGNED_OUT" ||
        event === "MFA_VERIFIED"
      ) {
        void this.realtime.setToken(session?.access_token ?? null);
      }
    });
    let watchN = 0;
    this.data = new DataClient<DB>(
      {
        t,
        watch: (schema, table, onChange) => {
          watchN += 1;
          const ch = this.realtime
            .channel(`pgdock-live:${schema}.${table}:${watchN}`)
            .onChange({ event: "*", schema, table }, onChange)
            .onResync(onChange)
            .subscribe();
          return () => void ch.unsubscribe();
        },
      },
      opts.schema ?? "public",
    );
    this.storage = new StorageClient(t);
  }
}

/**
 * createClient makes a client from the project URL and its publishable
 * key (or, on servers only, its secret key). Pass the generated Database
 * type for typed rows: createClient<Database>(url, key).
 */
export function createClient<DB extends AnyDatabase = AnyDatabase>(
  url: string,
  key: string,
  opts: ClientOptions = {},
): PgdockClient<DB> {
  return new PgdockClient<DB>(url, key, opts);
}
