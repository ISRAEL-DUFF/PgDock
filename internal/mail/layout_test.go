package mail

import (
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func parts(t *testing.T, raw []byte) (text, html string) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("content type %q: %v", msg.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		switch {
		case strings.HasPrefix(p.Header.Get("Content-Type"), "text/plain"):
			text = b.String()
		case strings.HasPrefix(p.Header.Get("Content-Type"), "text/html"):
			html = b.String()
		}
	}
	return text, html
}

func TestTextMessageGetsAnHTMLPartAndKeepsItsText(t *testing.T) {
	SetPublicURL("https://app.example.com")
	t.Cleanup(func() { SetPublicURL("") })
	body := "Hello Ada,\n\nConfirm your email:\n\nhttps://app.example.com/verify?token=abc&x=1\n\nThanks."
	raw := Render("Xenibase <hello@example.com>", Message{To: []string{"ada@example.com"}, Subject: "[Xenibase] Confirm your email", Body: body}, time.Unix(0, 0))
	text, html := parts(t, raw)
	// Quoted-printable isn't used: the text part is the body, byte for byte.
	if strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n") != body {
		t.Errorf("the text part changed:\n%q", text)
	}
	for _, want := range []string{
		`<h1`, "Confirm your email",
		`src="https://app.example.com/brand/mark-96.png"`,
		`href="https://app.example.com/verify?token=abc&amp;x=1"`, // a button, with the & escaped
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the HTML part lacks %q", want)
		}
	}
	if strings.Contains(html, "[Xenibase]") {
		t.Error("the title keeps the subject's [Xenibase] prefix")
	}
	// The first address in the whole message is the one in the text part.
	if i, j := strings.Index(string(raw), "https://app.example.com/verify"), strings.Index(string(raw), "<html"); i > j {
		t.Error("the HTML part comes before the plain text")
	}
}

func TestPlainMessagesStayPlain(t *testing.T) {
	raw := string(Render("a@b.c", Message{To: []string{"x@y.z"}, Subject: "s", Body: "hello", Plain: true}, time.Unix(0, 0)))
	if !strings.Contains(raw, "Content-Type: text/plain; charset=utf-8") || strings.Contains(raw, "multipart") || strings.Contains(raw, "<html") {
		t.Errorf("a plain message was wrapped:\n%s", raw)
	}
}

func TestUserTextIsEscaped(t *testing.T) {
	c := Content{
		Title: `<script>alert(1)</script>`,
		Blocks: []Block{
			P(`Acme <img src=x onerror=alert(1)> & "Co" http://evil.example/<b>`),
			List(`<i>one</i>`),
			Facts(Fact{`<b>L</b>`, `<u>V</u>`}),
			Code(`<x>`),
			Button(`<go>`, `javascript:alert(1)`),
			Button("Fine", "https://ok.example/path?a=1&b=2"),
		},
		Note: `<n>`,
	}
	html := c.HTML()
	for _, bad := range []string{"<script", "<img", "onerror=alert(1)>", "<b>L", "<u>", "<i>one", "<x>", "<go>", "<n>", "javascript:"} {
		if strings.Contains(html, bad) {
			t.Errorf("unescaped %q in the HTML", bad)
		}
	}
	if !strings.Contains(html, "https://ok.example/path?a=1&amp;b=2") {
		t.Error("a good link is missing")
	}
}

func TestStructuredContentMakesItsOwnText(t *testing.T) {
	c := Content{Title: "Your invoice", Blocks: []Block{
		P("Thanks, Ada."), Facts(Fact{"Invoice", "XB-2026-000001"}, Fact{"Amount", "$10.00"}), Button("View invoice", "https://app.example.com/i/1"),
	}, SignOff: "The Xenibase team"}
	raw := Render("a@b.c", Message{To: []string{"x@y.z"}, Subject: "Your invoice", Content: &c}, time.Unix(0, 0))
	text, html := parts(t, raw)
	for _, want := range []string{"Your invoice", "Invoice: XB-2026-000001", "Amount: $10.00", "View invoice:\nhttps://app.example.com/i/1", "The Xenibase team"} {
		if !strings.Contains(strings.ReplaceAll(text, "\r\n", "\n"), want) {
			t.Errorf("the text lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(html, "XB-2026-000001") || !strings.Contains(html, "View invoice") {
		t.Error("the HTML lacks the invoice")
	}
}

func TestNoLogoWithoutAPublicURL(t *testing.T) {
	SetPublicURL("")
	if h := (Content{Title: "t"}).HTML(); strings.Contains(h, "<img") {
		t.Error("a logo without an address to load it from")
	}
}

func TestDotStuffingSurvivesTheLayout(t *testing.T) {
	raw := string(Render("a@b.c", Message{To: []string{"x@y.z"}, Subject: "s", Body: "one\n.two"}, time.Unix(0, 0)))
	if !strings.Contains(raw, "\r\n..two\r\n") {
		t.Errorf("a line starting with a dot isn't stuffed:\n%s", raw)
	}
}

func TestFromTextFindsListsAndLinks(t *testing.T) {
	c := FromText("[Xenibase] Heads up", "Two things:\n\n- one\n- two\n\nhttps://x.example/a\n\nBye")
	kinds := []string{}
	for _, b := range c.Blocks {
		kinds = append(kinds, b.Kind)
	}
	if strings.Join(kinds, ",") != "p,list,button,p" || c.Title != "Heads up" {
		t.Errorf("blocks %v title %q", kinds, c.Title)
	}
}
