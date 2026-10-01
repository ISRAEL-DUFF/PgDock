import type { components } from "./schema";

export type Version = components["schemas"]["Version"];
export type ApiError = components["schemas"]["Error"];

export class ApiRequestError extends Error {
  constructor(
    readonly status: number,
    readonly body: ApiError | undefined,
  ) {
    super(body?.message ?? `request failed with status ${status}`);
  }
}

export async function getJSON<T>(path: string, fetchFn: typeof fetch = fetch): Promise<T> {
  const res = await fetchFn(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    let body: ApiError | undefined;
    try {
      body = (await res.json()) as ApiError;
    } catch {
      body = undefined;
    }
    throw new ApiRequestError(res.status, body);
  }
  return (await res.json()) as T;
}

export function getVersion(fetchFn?: typeof fetch): Promise<Version> {
  return getJSON<Version>("/api/v1/version", fetchFn);
}
