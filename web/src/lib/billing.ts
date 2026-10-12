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

/** Cents as dollars: 1000 → "$10.00". */
export function usd(cents: number): string {
  const sign = cents < 0 ? "-" : "";
  const abs = Math.abs(Math.round(cents));
  const whole = Math.floor(abs / 100).toLocaleString("en-US");
  return `${sign}$${whole}.${String(abs % 100).padStart(2, "0")}`;
}

/** Dollars typed by a person ("10", "$1,000.50") as cents, or null. */
export function parseUsd(s: string): number | null {
  const t = s.replace(/[$,\s]/g, "");
  if (t === "") return null;
  if (!/^\d+(\.\d{1,2})?$/.test(t)) return null;
  const [w, f = ""] = t.split(".");
  return Number(w) * 100 + Number(f.padEnd(2, "0"));
}

/** Minor units of a currency: USD in cents, anything else (NGN) in kobo. */
export function money(currency: string | undefined, minor: number): string {
  return currency === "USD" ? usd(minor) : naira(minor);
}

/** A unit price in cents, which may be a fraction of a cent: 250 → "$2.50",
 * 1.5 → "$0.015", 0.0002 → "0.0002¢". */
export function usdUnit(cents: number): string {
  if (cents >= 1) {
    const dollars = (cents / 100).toLocaleString("en-US", {
      minimumFractionDigits: 2,
      maximumFractionDigits: 6,
    });
    return `$${dollars}`;
  }
  return `${Number(cents.toPrecision(6))}¢`;
}

/** The part of an FX quote the helpers need. */
export type Rate = { effective_rate: string } | null | undefined;

/** A naira-per-dollar rate (a decimal string) as "₦1,515"; decimals only when it has them. */
export function nairaRate(r: string | number | undefined): string {
  const n = Number(r);
  if (r === undefined || !Number.isFinite(n)) return "";
  const text = n.toLocaleString("en-NG", {
    minimumFractionDigits: Number.isInteger(n) ? 0 : 2,
    maximumFractionDigits: 2,
  });
  return `₦${text}`;
}

/** "₦1,515 per $1" (decimals only when the rate has them). */
export function rate(fx: Rate): string {
  return fx ? `${nairaRate(fx.effective_rate)} per $1` : "";
}

/** Dollars (as cents) at a rate in whole naira: "≈ ₦15,150", or "" without a rate. */
export function approxNaira(cents: number, fx: Rate): string {
  const r = Number(fx?.effective_rate);
  if (!fx || !Number.isFinite(r)) return "";
  const n = Math.round((cents / 100) * r);
  return `≈ ₦${n.toLocaleString("en-NG")}`;
}

/** Basis points as a percentage: 100 → "1%", 150 → "1.5%". */
export function bps(n: number): string {
  return `${Number((n / 100).toFixed(2))}%`;
}

/** How a payment arrived, from its channel. */
export function channelLabel(channel: string): string {
  switch (channel) {
    case "wallet":
      return "Wallet debit";
    case "transfer":
      return "Bank transfer";
    case "manual":
      return "Recorded by PGDock";
    case "card":
    case "saved_card":
      return "Card";
    case "mandate":
    case "stablecoin":
      return "iSpend";
    default:
      return channel.replaceAll("_", " ");
  }
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
