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

  constructor(
    status: number,
    code: string,
    message: string,
    extra: { requestId?: string; details?: unknown; retryAfter?: number } = {},
  ) {
    super(message);
    this.name = "XenibaseError";
    this.status = status;
    this.code = code;
    this.requestId = extra.requestId;
    this.details = extra.details;
    this.retryAfter = extra.retryAfter;
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

export function toError(e: unknown): XenibaseError {
  if (e instanceof XenibaseError) return e;
  const msg = e instanceof Error ? e.message : String(e);
  return new XenibaseError(0, "network_error", msg);
}
