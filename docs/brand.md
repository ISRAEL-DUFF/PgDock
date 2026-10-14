# Brand

Xenibase's name, mark, colours and voice, in one place, so the web app, the
emails, the invoices and this documentation look like one product.

## The idea

A place where your data lives safely and is easy to reach. The mark is a
doorway: an arch with rows of data inside, and a small light above them. It
says the door is open to you, and someone is looking after what's behind it.

The line that goes with it is **"Postgres, looked after."**

## The mark

| File | For |
| --- | --- |
| `brand/mark.svg` | The mark on a transparent background, in colour. The master. |
| `brand/icon.svg` | The app icon: white doorway on a teal square, full-bleed (the platform rounds the corners). |
| `web/public/favicon.svg`, `favicon-32.png` | The browser tab. |
| `web/public/apple-touch-icon.png`, `icon-192.png`, `icon-512.png` | Home-screen and installed-app icons. |
| `web/public/brand/mark-96.png`, `mark-192.png` | The logo at the top of an email (email clients can't show SVG). |
| `web/public/og-image.png` | The card shown when a link is shared (1200×630). |

The PNGs are made from the two SVGs by `brand/render.mjs`; edit the SVGs and
run it, never edit a PNG. The mark in the app is `LogoMark` in
`web/src/components/shell/Logo.tsx`.

Rules:

- The mark stands on its own or beside the word **Xenibase** set in Inter,
  semi-bold. Keep clear space around it equal to the width of one data row.
- Don't recolour, rotate, stretch, outline or add effects. On a teal
  background use `brand/icon.svg`.
- Smallest size: 16px (the tab). Below 24px the rows merge, which is
  expected.

## Colour

| Name | Hex | Use |
| --- | --- | --- |
| Teal | `#0f766e` | Buttons and links on light backgrounds; the accent in both themes. White text on it is 5.5:1. |
| Deep teal | `#115e59` | Pressed and strong states; text on tinted panels (7.6:1 on white). |
| Light teal | `#5eead4` | The accent's text colour on dark backgrounds (12:1 on the app's dark surface). |
| Mint | `#effcf9` | The tinted panel behind an email's header. |
| Glow | `#fef3c7` | The small light in the mark. Not used elsewhere. |
| Ink / Muted / Line | `#171717` / `#5f5f5f` / `#e5e5e5` | Text, secondary text, hairlines. |

The app is dark by default, with a light theme. Neutrals are Supabase
Studio's greys. Green (`--ok`) is for success and is deliberately a
different green from the teal. The values live in two places that must
agree: `web/src/styles.css` (CSS variables) and `internal/brand/brand.go`
(used by emails); `internal/brand`'s test fails if they drift.

## Voice

Warm, plain and exact. Readers are business people and developers, so:

- Say what happened and what to do next, in that order. "Your payment didn't
  go through. Add funds to your wallet by Friday and we'll try again."
- Use the plain word: "sign in", not "authenticate"; "database", not
  "persistence layer". Keep the technical term where a developer needs it,
  and explain it once if a business reader might meet it.
- Be exact about money, dates and amounts. Warmth is never vagueness.
- Greet people by name when we have it. Thank them when they've done
  something for us. Don't use exclamation marks, emoji, or "Oops!".
- Say sorry once, specifically, when we caused the problem.
