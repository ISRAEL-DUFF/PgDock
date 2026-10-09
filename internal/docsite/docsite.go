// Package docsite builds the documentation site (V4 §8, M36): every
// Markdown file under docs/ rendered to HTML with the site's navigation,
// a search index, and links between pages checked. Links to .md files
// become links to their pages; links that leave docs/ go to the
// repository.
package docsite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Config is docs/site.json.
type Config struct {
	Title string `json:"title"`
	// Repo is where links that leave docs/ go: …/blob/main.
	Repo     string    `json:"repo"`
	Sections []Section `json:"sections"`
}

// Section is a group of pages in the navigation.
type Section struct {
	Title string   `json:"title"`
	Pages []string `json:"pages"` // paths under docs/
}

type page struct {
	src     string // docs-relative, slash-separated, e.g. guides/nextjs.md
	out     string // e.g. guides/nextjs.html
	title   string
	html    template.HTML
	ids     map[string]bool
	links   []string // internal hrefs, relative to the page
	text    string
	heads   []string
	section string
}

// Build renders src (docs/) into out and returns the broken links, if any.
func Build(src, out string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(src, "site.json"))
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("site.json: %w", err)
	}
	pages := map[string]*page{}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		pages[rel] = &page{src: rel, out: strings.TrimSuffix(rel, ".md") + ".html", ids: map[string]bool{}}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, s := range cfg.Sections {
		for _, p := range s.Pages {
			pg := pages[p]
			if pg == nil {
				return nil, fmt.Errorf("site.json: %s (in %q) doesn't exist", p, s.Title)
			}
			pg.section = s.Title
		}
	}
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
	dirs := map[string]bool{} // directories under docs/ (README.md pages)
	for k := range pages {
		dirs[path.Dir(k)] = true
	}
	for _, pg := range pages {
		b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(pg.src)))
		if err != nil {
			return nil, err
		}
		doc := md.Parser().Parse(text.NewReader(b))
		rewrite(doc, b, pg, pages, cfg.Repo)
		var buf bytes.Buffer
		if err := md.Renderer().Render(&buf, b, doc); err != nil {
			return nil, fmt.Errorf("%s: %w", pg.src, err)
		}
		pg.html = template.HTML(buf.String())
		if pg.title == "" {
			pg.title = strings.TrimSuffix(path.Base(pg.src), ".md")
		}
		pg.text = plain(buf.String())
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	tpl := template.Must(template.New("page").Parse(pageTemplate))
	type navPage struct {
		Title, Href string
		Current     bool
	}
	type navSection struct {
		Title string
		Pages []navPage
	}
	type searchEntry struct {
		Title    string   `json:"title"`
		Href     string   `json:"href"`
		Section  string   `json:"section,omitempty"`
		Headings []string `json:"headings"`
		Text     string   `json:"text"`
	}
	var index []searchEntry
	keys := make([]string, 0, len(pages))
	for k := range pages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		pg := pages[k]
		var nav []navSection
		for _, s := range cfg.Sections {
			ns := navSection{Title: s.Title}
			for _, p := range s.Pages {
				ns.Pages = append(ns.Pages, navPage{Title: pages[p].title, Href: relHref(pg.out, pages[p].out), Current: p == pg.src})
			}
			nav = append(nav, ns)
		}
		var buf bytes.Buffer
		err := tpl.Execute(&buf, map[string]any{
			"Site": cfg.Title, "Title": pg.title, "Body": pg.html, "Nav": nav, "Section": pg.section,
			"Root": relHref(pg.out, ""), "Source": cfg.Repo + "/docs/" + pg.src,
		})
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(out, filepath.FromSlash(pg.out))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
			return nil, err
		}
		t := pg.text
		if len(t) > 4000 {
			t = t[:4000]
		}
		index = append(index, searchEntry{Title: pg.title, Href: pg.out, Section: pg.section, Headings: pg.heads, Text: t})
	}
	ib, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(out, "search.json"), ib, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, "site.css"), []byte(siteCSS), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, "search.js"), []byte(searchJS), 0o644); err != nil {
		return nil, err
	}

	// Every internal link must reach a page, and its #anchor a heading.
	var broken []string
	byOut := map[string]*page{}
	for _, pg := range pages {
		byOut[pg.out] = pg
	}
	for _, k := range keys {
		pg := pages[k]
		for _, l := range pg.links {
			target, frag, _ := strings.Cut(l, "#")
			dest := pg
			if target != "" {
				dest = byOut[path.Clean(path.Join(path.Dir(pg.out), target))]
			}
			switch {
			case dest == nil:
				broken = append(broken, fmt.Sprintf("%s: %s doesn't exist", pg.src, l))
			case frag != "" && !dest.ids[frag]:
				broken = append(broken, fmt.Sprintf("%s: %s has no heading #%s", pg.src, dest.src, frag))
			}
		}
	}
	return broken, nil
}

var schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

// rewrite turns .md links into .html, links leaving docs/ into repo links,
// and records the page's title, heading ids and internal links.
func rewrite(doc ast.Node, src []byte, pg *page, pages map[string]*page, repo string) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch x := n.(type) {
		case *ast.Heading:
			if id, ok := x.AttributeString("id"); ok {
				if b, ok := id.([]byte); ok {
					pg.ids[string(b)] = true
				}
			}
			t := nodeText(x, src)
			if x.Level == 1 && pg.title == "" {
				pg.title = t
			} else if x.Level <= 3 {
				pg.heads = append(pg.heads, t)
			}
		case *ast.Link:
			x.Destination = []byte(fixLink(string(x.Destination), pg, pages, repo))
		}
		return ast.WalkContinue, nil
	})
}

func fixLink(dst string, pg *page, pages map[string]*page, repo string) string {
	if dst == "" || schemeRe.MatchString(dst) || strings.HasPrefix(dst, "//") {
		return dst
	}
	if strings.HasPrefix(dst, "#") {
		pg.links = append(pg.links, dst)
		return dst
	}
	target, frag, hasFrag := strings.Cut(dst, "#")
	joined := path.Clean(path.Join(path.Dir(pg.src), target))
	suffix := ""
	if hasFrag {
		suffix = "#" + frag
	}
	if strings.HasPrefix(joined, "../") || joined == ".." {
		// Leaves docs/: the repository.
		return repo + "/" + strings.TrimPrefix(joined, "../") + suffix
	}
	if strings.HasSuffix(target, ".md") {
		href := strings.TrimSuffix(target, ".md") + ".html" + suffix
		pg.links = append(pg.links, href)
		return href
	}
	// A directory with a README, or another file under docs/.
	if p, ok := pages[strings.TrimSuffix(joined, "/")+"/README.md"]; ok {
		href := relHref(pg.out, p.out) + suffix
		pg.links = append(pg.links, href)
		return href
	}
	return repo + "/docs/" + joined + suffix
}

// relHref is the link from page from to page to ("" is the site root).
func relHref(from, to string) string {
	depth := strings.Count(from, "/")
	prefix := strings.Repeat("../", depth)
	if to == "" {
		if prefix == "" {
			return "./"
		}
		return prefix
	}
	return prefix + to
}

func nodeText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if t, ok := c.(*ast.Text); ok {
				b.Write(t.Segment.Value(src))
			} else if s, ok := c.(*ast.String); ok {
				b.Write(s.Value)
			}
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

var tagRe = regexp.MustCompile(`<[^>]+>`)
var spaceRe = regexp.MustCompile(`\s+`)

func plain(h string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(template.HTMLEscapeString(tagRe.ReplaceAllString(h, " ")), " "))
}

const pageTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · {{.Site}}</title>
<link rel="stylesheet" href="{{.Root}}site.css">
</head>
<body>
<header class="top">
  <a class="brand" href="{{.Root}}index.html">{{.Site}}</a>
  <input id="q" type="search" placeholder="Search the docs" autocomplete="off" aria-label="Search the docs" data-root="{{.Root}}">
  <ol id="results" hidden></ol>
</header>
<div class="layout">
<nav class="side" aria-label="Documentation">
{{range .Nav}}<h2>{{.Title}}</h2>
<ul>{{range .Pages}}<li><a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Title}}</a></li>{{end}}</ul>
{{end}}</nav>
<main>
{{if .Section}}<p class="crumb">{{.Section}}</p>{{end}}
<article>{{.Body}}</article>
<p class="source"><a href="{{.Source}}">Edit this page</a></p>
</main>
</div>
<script src="{{.Root}}search.js" defer></script>
</body>
</html>
`

const siteCSS = `:root{--bg:#fff;--fg:#1c1f23;--muted:#5d6670;--line:#e3e6ea;--accent:#0b6e4f;--code:#f4f6f8}
@media (prefers-color-scheme:dark){:root{--bg:#121417;--fg:#e6e8eb;--muted:#9aa3ad;--line:#2a2f35;--accent:#4cc38a;--code:#1b1f24}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.6 system-ui,-apple-system,"Segoe UI",sans-serif}
a{color:var(--accent)}.top{position:sticky;top:0;z-index:2;display:flex;gap:16px;align-items:center;padding:10px 16px;border-bottom:1px solid var(--line);background:var(--bg)}
.brand{font-weight:700;text-decoration:none;color:var(--fg)}#q{flex:1;max-width:420px;padding:6px 10px;border:1px solid var(--line);border-radius:6px;background:var(--bg);color:var(--fg)}
#results{position:absolute;top:48px;left:16px;right:16px;max-width:560px;margin:0;padding:6px;list-style:none;background:var(--bg);border:1px solid var(--line);border-radius:8px;box-shadow:0 8px 24px rgba(0,0,0,.15)}
#results li a{display:block;padding:6px 8px;text-decoration:none;border-radius:4px}#results li a:hover,#results li a:focus{background:var(--code)}#results small{color:var(--muted);display:block}
.layout{display:flex;max-width:1200px;margin:0 auto}.side{width:260px;flex:none;padding:16px;border-right:1px solid var(--line);position:sticky;top:53px;height:calc(100vh - 53px);overflow:auto}
.side h2{font-size:12px;text-transform:uppercase;letter-spacing:.06em;color:var(--muted);margin:18px 0 6px}.side ul{list-style:none;margin:0;padding:0}.side li a{display:block;padding:3px 0;text-decoration:none;color:var(--fg)}
.side a[aria-current]{color:var(--accent);font-weight:600}main{flex:1;min-width:0;padding:16px 32px 64px}.crumb{color:var(--muted);font-size:14px;margin:0}
article h1{margin-top:8px}article pre{background:var(--code);padding:12px;border-radius:6px;overflow:auto;font-size:14px}article code{background:var(--code);padding:1px 4px;border-radius:4px;font-size:.92em}article pre code{padding:0;background:none}
article table{border-collapse:collapse;display:block;overflow:auto}article th,article td{border:1px solid var(--line);padding:6px 10px;text-align:left;vertical-align:top}.source{color:var(--muted);font-size:14px;margin-top:48px}
@media (max-width:800px){.layout{display:block}.side{position:static;width:auto;height:auto;border-right:0;border-bottom:1px solid var(--line)}main{padding:16px}}
`

const searchJS = `(() => {
  const q = document.getElementById("q"), out = document.getElementById("results");
  if (!q) return;
  const root = q.dataset.root || "./";
  let index = null;
  const load = () => index ? Promise.resolve(index) : fetch(root + "search.json").then(r => r.json()).then(j => (index = j));
  const score = (e, words) => {
    let s = 0;
    for (const w of words) {
      const t = e.title.toLowerCase(), h = e.headings.join(" ").toLowerCase(), x = e.text.toLowerCase();
      if (t.includes(w)) s += 10; else if (h.includes(w)) s += 4; else if (x.includes(w)) s += 1; else return 0;
    }
    return s;
  };
  q.addEventListener("input", async () => {
    const words = q.value.toLowerCase().split(/\s+/).filter(Boolean);
    if (!words.length) { out.hidden = true; return; }
    const idx = await load();
    const hits = idx.map(e => [score(e, words), e]).filter(([s]) => s > 0).sort((a, b) => b[0] - a[0]).slice(0, 10);
    out.replaceChildren(...hits.map(([, e]) => {
      const li = document.createElement("li"), a = document.createElement("a"), small = document.createElement("small");
      a.href = root + e.href; a.textContent = e.title; small.textContent = e.section || "";
      a.append(small); li.append(a); return li;
    }));
    out.hidden = hits.length === 0;
  });
  q.addEventListener("keydown", e => { if (e.key === "Escape") { q.value = ""; out.hidden = true; } });
  document.addEventListener("keydown", e => { if (e.key === "/" && document.activeElement !== q) { e.preventDefault(); q.focus(); } });
})();
`
