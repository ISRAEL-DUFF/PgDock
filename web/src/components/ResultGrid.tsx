import { cx } from "./ui";

type Col = { name: string; type: string };

/** A read-only grid of query rows; NULL is shown as such. */
export function ResultGrid({ columns, rows, testId, offset = 0 }: { columns: Col[]; rows: (string | null | undefined)[][]; testId?: string; offset?: number }) {
  if (columns.length === 0) return null;
  return (
    <div className="max-h-[32rem] overflow-auto rounded-md border border-line" data-testid={testId}>
      <table className="min-w-full border-collapse text-xs">
        <thead className="sticky top-0 z-10 bg-surface-2">
          <tr>
            <th className="border-b border-line px-2 py-1.5 text-right font-normal text-muted">#</th>
            {columns.map((c, i) => (
              <th key={i} className="border-b border-l border-line px-2 py-1.5 text-left font-semibold whitespace-nowrap">
                {c.name}
                <span className="ml-1.5 font-normal text-muted">{c.type}</span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i} className="odd:bg-surface even:bg-surface-2/40">
              <td className="px-2 py-1 text-right text-muted tabular-nums">{offset + i + 1}</td>
              {r.map((v, j) => (
                <td
                  key={j}
                  title={v ?? "NULL"}
                  className={cx("max-w-[28rem] truncate border-l border-line px-2 py-1 font-mono whitespace-pre", v == null && "text-muted italic")}
                >
                  {v ?? "NULL"}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && <p className="px-3 py-2 text-xs text-muted">No rows.</p>}
    </div>
  );
}
