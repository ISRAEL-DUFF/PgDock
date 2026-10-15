import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Markdown, headingsOf, slug } from "./Markdown";

const html = (md: string) => renderToStaticMarkup(<Markdown>{md}</Markdown>);

describe("Markdown", () => {
  it("renders the elements legal documents use", () => {
    const out = html("# Title\n\n## Part one\n\nSome **bold** text.\n\n- a\n- b\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n");
    expect(out).toContain("<h1");
    expect(out).toContain('id="part-one"');
    expect(out).toContain("<strong");
    expect(out).toContain("<ul");
    expect(out).toContain("<table");
    expect(out).toContain("<th");
    // The source's markers aren't left in the page.
    expect(out).not.toContain("| ---");
    expect(out).not.toContain("## ");
  });

  it("never renders raw HTML from a document", () => {
    const out = html('Hello <script>alert(1)</script> <img src=x onerror="alert(1)"> <b>x</b>');
    expect(out).not.toContain("<script");
    expect(out).not.toContain("<img");
    expect(out).not.toContain("<b>");
  });

  it("drops links that aren't web, mail or phone addresses", () => {
    const out = html("[bad](javascript:alert(1)) [data](data:text/html;base64,AAAA) [ok](https://example.com) [mail](mailto:a@b.co)");
    expect(out).not.toContain("javascript:");
    expect(out).not.toContain("data:text");
    expect(out).toContain('href="https://example.com"');
    expect(out).toContain('rel="noopener noreferrer"');
    expect(out).toContain('href="mailto:a@b.co"');
  });
});

describe("headings", () => {
  it("lists the second and third level headings", () => {
    const hs = headingsOf("# Doc\n\n## One\n\ntext\n\n### Sub\n\n```\n## not a heading\n```\n\n## Two *bold*\n");
    expect(hs.map((h) => [h.level, h.text, h.id])).toEqual([
      [2, "One", "one"],
      [3, "Sub", "sub"],
      [2, "Two bold", "two-bold"],
    ]);
  });
  it("makes ids from words, in any script", () => {
    expect(slug("Service credits, 2026!")).toBe("service-credits-2026");
    expect(slug("!!!")).toBe("section");
  });
});
