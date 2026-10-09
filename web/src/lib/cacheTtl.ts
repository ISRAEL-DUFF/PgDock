// The API settings' anonymous-read cache (V4.1 §10) as text: one
// "relation seconds" per line, e.g. "public.products 60" or
// "rpc.search_products 30".

export function formatCacheTTLs(m: Record<string, number> | undefined): string {
  return Object.entries(m ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k} ${v}`)
    .join("\n");
}

/** The map, or the first line that isn't "name seconds". */
export function parseCacheTTLs(
  text: string,
): { ok: true; value: Record<string, number> } | { ok: false; error: string } {
  const value: Record<string, number> = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const m = /^([^\s=:]+)(?:\s*[=:]\s*|\s+)(\d+)$/.exec(line);
    if (!m) {
      return {
        ok: false,
        error: `"${line}": write a table or function and its seconds, like public.products 60`,
      };
    }
    value[m[1]] = Number(m[2]);
  }
  return { ok: true, value };
}
