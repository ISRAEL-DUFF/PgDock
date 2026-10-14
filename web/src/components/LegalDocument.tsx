import { Printer } from "lucide-react";
import { useRef } from "react";
import { Markdown, headingsOf } from "./Markdown";
import { Button, cx } from "./ui";

/**
 * A legal document laid out to be read: a contents list for a long one, the
 * text in the app's type, and a button to print it or save it as a PDF.
 * `testId` goes on the document itself.
 */
export function LegalDocument({
  md,
  testId,
  meta,
  maxHeight,
  contents = "auto",
}: {
  md: string;
  testId?: string;
  /** A line above the text: version, date. */
  meta?: string;
  /** A tall document scrolls inside its box at this height (a CSS length). */
  maxHeight?: string;
  /** "auto" lists the headings of a document with four or more. */
  contents?: "auto" | "never";
}) {
  const box = useRef<HTMLDivElement>(null);
  const heads = headingsOf(md);
  const showContents = contents === "auto" && heads.filter((h) => h.level === 2).length >= 4;
  const go = (id: string) => box.current?.querySelector(`#${CSS.escape(id)}`)?.scrollIntoView({ block: "start", behavior: "smooth" });
  const print = () => {
    const el = box.current;
    if (!el) return;
    const done = () => {
      document.body.classList.remove("printing-doc");
      el.classList.remove("print-doc");
      window.removeEventListener("afterprint", done);
    };
    document.body.classList.add("printing-doc");
    el.classList.add("print-doc");
    window.addEventListener("afterprint", done);
    window.print();
  };
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-3 text-[12px] text-muted">
        <span>{meta}</span>
        <Button type="button" onClick={print} aria-label="Print or save as PDF" className="shrink-0">
          <Printer className="mr-1.5 inline h-3.5 w-3.5" aria-hidden />
          Print or save as PDF
        </Button>
      </div>
      <div
        ref={box}
        tabIndex={0}
        data-testid={testId}
        className={cx("rounded-md border border-line bg-surface p-5 sm:p-7", maxHeight && "overflow-y-auto")}
        style={maxHeight ? { maxHeight } : undefined}
      >
        {showContents && (
          <nav aria-label="Contents" className="mb-6 rounded-md bg-surface-2 p-4">
            <div className="mb-2 text-[12px] font-medium uppercase tracking-wide text-muted">Contents</div>
            <ol className="m-0 flex list-none flex-col gap-1 p-0 text-[13.5px]">
              {heads
                .filter((h) => h.level === 2)
                .map((h) => (
                  <li key={h.id}>
                    <button type="button" onClick={() => go(h.id)} className="text-left text-accent-text underline-offset-2 hover:underline">
                      {h.text}
                    </button>
                  </li>
                ))}
            </ol>
          </nav>
        )}
        <Markdown>{md}</Markdown>
      </div>
    </div>
  );
}
