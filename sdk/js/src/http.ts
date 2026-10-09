import { PgdockError, type Result, fail, ok, toError } from "./errors.js";

export type Fetch = typeof fetch;

/** What every service shares: the project URL, the key and the session. */
export interface Transport {
  url: string;
  key: string;
  fetch: Fetch;
  headers: Record<string, string>;
  /** The current access token (refreshed first when about to expire). */
  token(): Promise<string | null>;
}

export interface RequestOptions {
  method?: string;
  query?: Record<string, string | number | boolean | undefined | string[]>;
  body?: unknown;
  /** A raw body (a file); its type goes in headers. */
  raw?: BodyInit;
  headers?: Record<string, string>;
  /** Use this token instead of the session's ("" for none). */
  token?: string;
  signal?: AbortSignal;
}

export function buildURL(
  base: string,
  path: string,
  query?: RequestOptions["query"],
): string {
  const u = new URL(base.replace(/\/+$/, "") + path);
  for (const [k, v] of Object.entries(query ?? {})) {
    if (v === undefined) continue;
    if (Array.isArray(v)) for (const x of v) u.searchParams.append(k, x);
    else u.searchParams.set(k, String(v));
  }
  return u.toString();
}

/** apiError reads the API's {"error":{code,message,request_id}} answer. */
export async function apiError(res: Response): Promise<PgdockError> {
  let code = "http_" + res.status;
  let message = res.statusText || "request failed";
  let requestId = res.headers.get("x-request-id") ?? undefined;
  let details: unknown;
  try {
    const text = await res.text();
    const body = text ? JSON.parse(text) : null;
    const e = body?.error ?? body;
    if (e && typeof e === "object") {
      if (typeof e.code === "string") code = e.code;
      if (typeof e.message === "string") message = e.message;
      else if (typeof e.msg === "string") message = e.msg;
      if (typeof e.request_id === "string") requestId = e.request_id;
      details = e.details;
    }
  } catch {
    // not JSON: keep the status
  }
  const ra = Number(res.headers.get("retry-after"));
  return new PgdockError(res.status, code, message, {
    requestId,
    details,
    retryAfter: Number.isFinite(ra) && ra > 0 ? ra : undefined,
  });
}

/** send makes a request and returns the response, or the API's error. */
export async function send(
  t: Transport,
  path: string,
  o: RequestOptions = {},
): Promise<Result<Response>> {
  const headers: Record<string, string> = {
    apikey: t.key,
    ...t.headers,
    ...o.headers,
  };
  try {
    const token = o.token !== undefined ? o.token : await t.token();
    if (token) headers["Authorization"] = "Bearer " + token;
    let body: BodyInit | undefined = o.raw;
    if (o.body !== undefined) {
      body = JSON.stringify(o.body);
      headers["Content-Type"] = "application/json";
    }
    const res = await t.fetch(buildURL(t.url, path, o.query), {
      method: o.method ?? (body === undefined ? "GET" : "POST"),
      headers,
      body,
      signal: o.signal,
    });
    if (!res.ok) return fail(await apiError(res));
    return ok(res);
  } catch (e) {
    return fail(toError(e));
  }
}

/** json makes a request and decodes the JSON answer. */
export async function json<T>(
  t: Transport,
  path: string,
  o: RequestOptions = {},
): Promise<Result<T>> {
  const r = await send(t, path, o);
  if (r.error) return r;
  try {
    const text = await r.data.text();
    return ok((text ? JSON.parse(text) : null) as T);
  } catch (e) {
    return fail(
      new PgdockError(
        r.data.status,
        "invalid_response",
        "the answer isn't JSON: " + toError(e).message,
      ),
    );
  }
}
