import { createHmac } from "node:crypto";

function base32Decode(s: string): Buffer {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0;
  let value = 0;
  const out: number[] = [];
  for (const c of s.replace(/=+$/, "").toUpperCase()) {
    const i = alphabet.indexOf(c);
    if (i < 0) throw new Error(`bad base32 character ${c}`);
    value = (value << 5) | i;
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Buffer.from(out);
}

/** RFC 6238 TOTP (SHA-1, 6 digits, 30s), as an authenticator app computes it. */
export function totp(secret: string, at = Date.now()): string {
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const h = createHmac("sha1", base32Decode(secret)).update(counter).digest();
  const off = h[h.length - 1] & 0x0f;
  const v = (h.readUInt32BE(off) & 0x7fffffff) % 1_000_000;
  return v.toString().padStart(6, "0");
}

let lastStep = -1;

/**
 * A code from a time step not used before in this run: PGDock rejects
 * replayed codes, so consecutive sign-ins wait for the next 30s window.
 */
export async function freshTotp(secret: string): Promise<string> {
  let step = Math.floor(Date.now() / 30_000);
  if (step <= lastStep) {
    const wait = (lastStep + 1) * 30_000 - Date.now() + 250;
    await new Promise((r) => setTimeout(r, wait));
    step = Math.floor(Date.now() / 30_000);
  }
  lastStep = step;
  return totp(secret);
}
