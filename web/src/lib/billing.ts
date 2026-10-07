/** Kobo as naira: 1500000 → "₦15,000.00". */
export function naira(kobo: number): string {
  const sign = kobo < 0 ? "-" : "";
  const abs = Math.abs(Math.round(kobo));
  const whole = Math.floor(abs / 100).toLocaleString("en-NG");
  return `${sign}₦${whole}.${String(abs % 100).padStart(2, "0")}`;
}

/** Naira typed by a person ("15,000" or "15000.50") as kobo, or null. */
export function parseNaira(s: string): number | null {
  const t = s.replace(/[₦,\s]/g, "");
  if (t === "") return null;
  if (!/^\d+(\.\d{1,2})?$/.test(t)) return null;
  const [w, f = ""] = t.split(".");
  return Number(w) * 100 + Number(f.padEnd(2, "0"));
}

/** "2026-10" for the month containing d (UTC). */
export function periodOf(d: Date): string {
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, "0")}`;
}

/** "2026-10" → "October 2026". */
export function periodLabel(p: string): string {
  const [y, m] = p.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, 1)).toLocaleDateString("en-GB", {
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  });
}

export const INVOICE_STATUS: Record<
  string,
  { label: string; tone: "ok" | "warn" | "danger" | "muted" | "accent" }
> = {
  draft: { label: "Draft", tone: "muted" },
  issued: { label: "Issued", tone: "accent" },
  paid: { label: "Paid", tone: "ok" },
  paid_wht_pending: { label: "Paid (WHT pending)", tone: "ok" },
  partially_paid: { label: "Partially paid", tone: "warn" },
  void: { label: "Void", tone: "muted" },
};
