package docsite

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuild(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, src, "site.json", `{"title":"T","repo":"https://example.test/blob/main","sections":[{"title":"S","pages":["index.md","guides/a.md"]}]}`)
	write(t, src, "index.md", "# Home\n\nSee [a](guides/a.md#two-words), [ex](examples/x/), [code](../cmd/x.go) and [web](https://w.test).\n")
	write(t, src, "guides/a.md", "# Guide A\n\n## Two words\n\n[home](../index.md) [missing](nope.md) [bad anchor](../index.md#nothing) [self](#two-words)\n")
	write(t, src, "examples/x/README.md", "# Example\n")
	broken, err := Build(src, out)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(broken)
	want := []string{"guides/a.md: index.md has no heading #nothing", "guides/a.md: nope.html doesn't exist"}
	if !slices.Equal(broken, want) {
		t.Fatalf("broken: %q", broken)
	}
	home, _ := os.ReadFile(filepath.Join(out, "index.html"))
	for _, s := range []string{`href="guides/a.html#two-words"`, `href="examples/x/README.html"`, `href="https://example.test/blob/main/cmd/x.go"`,
		`href="https://w.test"`, "<title>Home · T</title>", `aria-current="page">Home`} {
		if !strings.Contains(string(home), s) {
			t.Errorf("index.html lacks %s", s)
		}
	}
	a, _ := os.ReadFile(filepath.Join(out, "guides", "a.html"))
	if !strings.Contains(string(a), `href="../site.css"`) || !strings.Contains(string(a), `<h2 id="two-words">`) {
		t.Errorf("guides/a.html: %s", a)
	}
	idx, _ := os.ReadFile(filepath.Join(out, "search.json"))
	if !strings.Contains(string(idx), `"title":"Guide A"`) || !strings.Contains(string(idx), `"Two words"`) {
		t.Errorf("search.json: %s", idx)
	}
}

// TestDocs builds the real docs: every link between pages must resolve.
func TestDocs(t *testing.T) {
	broken, err := Build(filepath.Join("..", "..", "docs"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range broken {
		t.Error(b)
	}
}
