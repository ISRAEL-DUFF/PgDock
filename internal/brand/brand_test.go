package brand

import (
	"os"
	"strings"
	"testing"
)

// The web app's accent tokens and the colours email uses are one palette.
func TestThemeMatchesTheEmailPalette(t *testing.T) {
	css, err := os.ReadFile("../../web/src/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--accent: " + Teal + ";",
		"--accent-strong: " + TealDeep + ";",
		"--accent-text: " + TealLight + ";",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("web/src/styles.css doesn't have %q: update it or internal/brand together", want)
		}
	}
	svg, err := os.ReadFile("../../brand/mark.svg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(svg), Glow) {
		t.Errorf("brand/mark.svg doesn't use the glow colour %s", Glow)
	}
}
