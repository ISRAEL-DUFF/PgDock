// Renders brand/*.svg into the PNGs the web app serves (web/public), with
// Chromium through Playwright (already used by the e2e tests).
// From the repository root:
//   npm i --no-save playwright-core && npx playwright-core install chromium
//   node brand/render.mjs
// Set CHROMIUM=/path/to/chrome to use a browser you already have.
import { chromium } from "playwright-core";
import { readFileSync, mkdirSync } from "node:fs";

const mark = readFileSync("brand/mark.svg", "utf8");
const icon = readFileSync("brand/icon.svg", "utf8");
const out = "web/public";
mkdirSync(`${out}/brand`, { recursive: true });

const browser = await chromium.launch({ executablePath: process.env.CHROMIUM || undefined });
const page = await browser.newPage();

/** shot renders html at w x h and saves a PNG, transparent where the page is. */
async function shot(html, w, h, file) {
  await page.setViewportSize({ width: w, height: h });
  await page.setContent(
    `<style>html,body{margin:0;background:transparent}svg{display:block;width:${w}px;height:${h}px}</style>${html}`,
  );
  await page.screenshot({ path: `${out}/${file}`, omitBackground: true, clip: { x: 0, y: 0, width: w, height: h } });
}

for (const [svg, size, file] of [
  [mark, 32, "favicon-32.png"],
  [mark, 96, "brand/mark-96.png"],
  [mark, 192, "brand/mark-192.png"],
  [icon, 180, "apple-touch-icon.png"],
  [icon, 192, "icon-192.png"],
  [icon, 512, "icon-512.png"],
]) {
  await shot(svg, size, size, file);
}

// The sharing card (link previews): 1200x630.
const inner = mark.replace(/<\/?svg[^>]*>/g, "").replace('id="xb-g"', 'id="xb-card"').replace("url(#xb-g)", "url(#xb-card)");
await page.setViewportSize({ width: 1200, height: 630 });
await page.setContent(`<style>html,body{margin:0}</style>
<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630">
  <defs><linearGradient id="bg" x1="0" y1="0" x2="1200" y2="630" gradientUnits="userSpaceOnUse">
    <stop offset="0" stop-color="#0b1f1d"/><stop offset="1" stop-color="#134e4a"/></linearGradient></defs>
  <rect width="1200" height="630" fill="url(#bg)"/>
  <g transform="translate(120 205) scale(3.4)">${inner}</g>
  <text x="380" y="300" font-family="Inter, Helvetica Neue, Helvetica, Arial, sans-serif" font-size="104" font-weight="700" fill="#ffffff" letter-spacing="-2">Xenibase</text>
  <text x="384" y="372" font-family="Inter, Helvetica Neue, Helvetica, Arial, sans-serif" font-size="40" fill="#99f6e4">Postgres, looked after.</text>
</svg>`);
await page.screenshot({ path: `${out}/og-image.png`, clip: { x: 0, y: 0, width: 1200, height: 630 } });

await browser.close();
console.log("rendered");
