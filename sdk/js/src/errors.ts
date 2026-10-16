/**
 * Every failure the client reports is a XenibaseError: the API's errors keep
 * their status, code and request id; a network failure is status 0 with
 * code "network_error".
 */
export class XenibaseError extends Error {
  /** The HTTP status (0 when the request never got an answer). */
  readonly status: number;
  /** The API's error code, such as "rls_required" or "invalid_credentials". */
  readonly code: string;
  /** The request's id, for support and the API logs. */
  readonly requestId?: string;
  /** Extra detail some errors carry (a batch's failing operation, …). */
  readonly details?: unknown;
  /** Seconds to wait before retrying, when the API said. */
  readonly retryAfter?: number;
  /** For a network failure, what the runtime reported (its error, with a code such as ECONNRESET). */
  readonly cause?: unknown;

  constructor(
    status: number,
    code: string,
    message: string,
    extra: { requestId?: string; details?: unknown; retryAfter?: number; cause?: unknown } = {},
  ) {
    super(message);
    this.name = "XenibaseError";
    this.status = status;
    this.code = code;
    this.requestId = extra.requestId;
    this.details = extra.details;
    this.retryAfter = extra.retryAfter;
    this.cause = extra.cause;
  }
}

/** A call's outcome: data, or an error, never both. */
export type Result<T> =
  | { data: T; error: null }
  | { data: null; error: XenibaseError };

export function ok<T>(data: T): Result<T> {
  return { data, error: null };
}

export function fail<T>(error: XenibaseError): Result<T> {
  return { data: null, error };
}

/**
 * networkReason says why a request never got an answer. fetch reports every
 * failure as "fetch failed" and puts the reason in its cause chain (Node's
 * undici: a code such as ECONNRESET, UND_ERR_CONNECT_TIMEOUT or
 * ERR_SSL_…); this reads it, so a reset can be told from a timeout.
 */
export function networkReason(e: unknown): string {
  const parts: string[] = [];
  let cur: unknown = e;
  for (let i = 0; i < 4 && cur && typeof cur === "object"; i++) {
    const o = cur as { code?: unknown; message?: unknown; cause?: unknown };
    const code = typeof o.code === "string" ? o.code : "";
    const message = typeof o.message === "string" ? o.message : "";
    const part = [code, message].filter(Boolean).join(": ");
    if (part && !parts.includes(part)) parts.push(part);
    cur = o.cause;
  }
  return parts.join(" <- ") || String(e);
}

export function toError(e: unknown): XenibaseError {
  if (e instanceof XenibaseError) return e;
  return new XenibaseError(0, "network_error", networkReason(e), { cause: e });
}
