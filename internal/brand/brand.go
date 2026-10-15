// Package brand holds Xenibase's colours and logo paths in one place for the
// server's own output (email, documents). The web app's theme tokens are in
// web/src/styles.css and must match: brand_test.go checks that they do.
// The design, the files and the rules are in docs/brand.md and brand/.
package brand

// Name is the product's name as it is written.
const Name = "Xenibase"

// Tagline is the one line that says what Xenibase is.
const Tagline = "Postgres, looked after."

// Colours, as #rrggbb. Email can't use CSS variables, so these are the
// values written into messages.
const (
	// Teal is the brand colour: buttons and links on a light background.
	// White text on it is 5.5:1.
	Teal = "#0f766e"
	// TealDeep is the pressed and strong state, and headings on tinted panels.
	TealDeep = "#115e59"
	// TealLight is the brand colour on a dark background.
	TealLight = "#5eead4"
	// Mint is the tinted panel behind a message's header.
	Mint = "#effcf9"
	// Ink is body text; Muted is secondary text; Line is a hairline.
	Ink   = "#171717"
	Muted = "#5f5f5f"
	Line  = "#e5e5e5"
	// Glow is the small light in the mark.
	Glow = "#fef3c7"
)

// Logo files are served by the web app (web/public) at these paths, so a
// message links to them under the public URL: email clients can't show SVG.
const (
	LogoPath       = "/brand/mark-96.png"  // 48px at 2x, for the header of a message
	LogoPathLarge  = "/brand/mark-192.png" // 96px at 2x
	SharingCardURL = "/og-image.png"
)
