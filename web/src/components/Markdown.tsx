import { isValidElement, useMemo, useState, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { cx } from "./ui";

/**
 * Legal and policy text, as people read it: headings, lists, tables and
 * links in the app's type and theme, instead of the markdown source.
 * Raw HTML in a document is never rendered (react-markdown ignores it),
 * and link addresses other than http, https, mailto and tel are dropped, so
 * a published document can't script the page.
 */

/** A heading's id: lower-case words joined by dashes. */
export function slug(text: string): string {
  return (
    text
      .toLowerCase()
      .replace(/[^\p{L}\p{N}\s-]/gu, "")
      .trim()
      .replace(/\s+/g, "-") || "section"
  );
}

function textOf(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (isValidElement<{ children?: ReactNode }>(node)) return textOf(node.props.children);
  return "";
}

export type Heading = { level: number; text: string; id: string };

/** The headings of a document, for a contents list (levels 2 and 3). Two
 * headings with the same words share an id; the list links to the first. */
export function headingsOf(md: string): Heading[] {
  const out: Heading[] = [];
  let fence = false;
  for (const line of md.split("\n")) {
    if (/^\s*(```|~~~)/.test(line)) fence = !fence;
    const m = !fence && /^(#{2,3})\s+(.+?)\s*#*\s*$/.exec(line);
    if (!m) continue;
    const text = m[2].replace(/[*_`]/g, "");
    out.push({ level: m[1].length, text, id: slug(text) });
  }
  return out;
}

const safeHref = (url: string) => /^(https?:|mailto:|tel:|#|\/)/i.test(url.trim()) ? url : "";

function components(): Components {
  const heading =
    (Tag: "h1" | "h2" | "h3" | "h4", cls: string) =>
    ({ children }: { children?: ReactNode }) => {
      const id = slug(textOf(children));
      return (
        <Tag id={id} className={cx("scroll-mt-20 text-fg", cls)}>
          {children}
        </Tag>
      );
    };
  return {
    h1: heading("h1", "mb-3 mt-0 text-[22px] font-semibold leading-tight"),
    h2: heading("h2", "mb-2 mt-8 border-b border-line pb-1.5 text-[17px] font-semibold"),
    h3: heading("h3", "mb-1.5 mt-6 text-[15px] font-semibold"),
    h4: heading("h4", "mb-1 mt-4 text-[14px] font-semibold"),
    p: ({ children }) => <p className="my-3 text-[14px] leading-7 text-fg-light">{children}</p>,
    ul: ({ children }) => <ul className="my-3 list-disc space-y-1.5 pl-6 text-[14px] leading-7 text-fg-light marker:text-muted">{children}</ul>,
    ol: ({ children }) => <ol className="my-3 list-decimal space-y-1.5 pl-6 text-[14px] leading-7 text-fg-light marker:text-muted">{children}</ol>,
    li: ({ children }) => <li className="pl-1">{children}</li>,
    strong: ({ children }) => <strong className="font-semibold text-fg">{children}</strong>,
    em: ({ children }) => <em className="italic">{children}</em>,
    hr: () => <hr className="my-6 border-line" />,
    blockquote: ({ children }) => (
      <blockquote className="my-4 border-l-2 border-accent bg-surface-2 px-4 py-1 text-[14px] text-fg-light">{children}</blockquote>
    ),
    a: ({ href, children }) => {
      const h = safeHref(href ?? "");
      const external = /^https?:/i.test(h);
      return h ? (
        <a
          href={h}
          className="text-accent-text underline underline-offset-2"
          {...(external ? { target: "_blank", rel: "noopener noreferrer" } : {})}
        >
          {children}
        </a>
      ) : (
        <span>{children}</span>
      );
    },
    code: ({ children }) => <code className="rounded bg-surface-3 px-1 py-0.5 font-mono text-[12.5px]">{children}</code>,
    pre: ({ children }) => <pre className="my-4 overflow-x-auto rounded-md bg-code-bg p-3 font-mono text-[12.5px]">{children}</pre>,
    table: ({ children }) => (
      <div className="my-4 overflow-x-auto rounded-md border border-line">
        <table className="w-full border-collapse text-left text-[13.5px]">{children}</table>
      </div>
    ),
    thead: ({ children }) => <thead className="bg-surface-2 text-[12px] uppercase tracking-wide text-muted">{children}</thead>,
    tr: ({ children }) => <tr className="border-b border-line last:border-b-0">{children}</tr>,
    th: ({ children }) => <th className="px-3 py-2 font-medium">{children}</th>,
    td: ({ children }) => <td className="px-3 py-2.5 align-top text-fg-light">{children}</td>,
  };
}

/** Markdown, rendered. */
export function Markdown({ children, className }: { children: string; className?: string }) {
  const comps = useMemo(components, []);
  return (
    <div className={cx("markdown max-w-none", className)}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={comps} urlTransform={safeHref}>
        {children}
      </ReactMarkdown>
    </div>
  );
}


/** A Markdown text box with a Preview tab, so an author sees the document as
 * readers will before publishing it. */
export function MarkdownField({
  id,
  value,
  onChange,
  className,
}: {
  id?: string;
  value: string;
  onChange: (v: string) => void;
  className?: string;
}) {
  const [mode, setMode] = useState<"write" | "preview">("write");
  const tab = (m: "write" | "preview", label: string) => (
    <button
      type="button"
      role="tab"
      aria-selected={mode === m}
      onClick={() => setMode(m)}
      className={cx(
        "rounded px-2 py-1 text-[12px]",
        mode === m ? "bg-surface-3 text-fg" : "text-muted hover:text-fg",
      )}
    >
      {label}
    </button>
  );
  return (
    <div className="flex flex-col gap-1">
      <div role="tablist" className="flex gap-1">
        {tab("write", "Write")}
        {tab("preview", "Preview")}
      </div>
      {mode === "write" ? (
        <textarea id={id} className={className} value={value} onChange={(e) => onChange(e.target.value)} />
      ) : (
        <div className="max-h-96 overflow-y-auto rounded-md border border-line bg-surface p-4" data-testid="markdown-preview">
          <Markdown>{value.trim() ? value : "_Nothing to preview yet._"}</Markdown>
        </div>
      )}
    </div>
  );
}
