// The console's query history lives in this browser only (spec §8.5):
// nothing but an audit entry is stored on the server.

const max = 50;
const key = (projectId: string) => `pgdock.sql-history.${projectId}`;

export function loadHistory(projectId: string): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(key(projectId)) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

/** Puts query first, without duplicates, keeping the newest 50. */
export function pushHistory(projectId: string, query: string): string[] {
  const q = query.trim();
  const next = [q, ...loadHistory(projectId).filter((h) => h !== q)].slice(0, max);
  try {
    localStorage.setItem(key(projectId), JSON.stringify(next));
  } catch {
    // Storage full or blocked: history is a convenience.
  }
  return next;
}

export function clearHistory(projectId: string) {
  try {
    localStorage.removeItem(key(projectId));
  } catch {
    // ignore
  }
}

/** A random query id for cancel; randomUUID needs a secure context. */
export function newQueryId(): string {
  const c: Crypto = globalThis.crypto;
  if (typeof c.randomUUID === "function") return c.randomUUID();
  const b = new Uint8Array(16);
  c.getRandomValues(b);
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
