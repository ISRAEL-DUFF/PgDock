import { ChevronLeft, ChevronRight } from "lucide-react";
import { useEffect, useState } from "react";
import type { RowCount } from "../../api/client";
import { Button, Segmented, Select } from "../ui";

export const PAGE_SIZES = [100, 500, 1000] as const;
export type Mode = "data" | "definition";

/** Page navigation, page size, the record count, and Data / Definition. */
export function Footer({
  page,
  pageSize,
  count,
  hasNext,
  mode,
  onPage,
  onPageSize,
  onMode,
}: {
  page: number;
  pageSize: number;
  count: RowCount | undefined;
  hasNext: boolean;
  mode: Mode;
  onPage: (p: number) => void;
  onPageSize: (n: number) => void;
  onMode: (m: Mode) => void;
}) {
  const pages = count?.count != null && !count.estimated ? Math.max(1, Math.ceil(count.count / pageSize)) : null;
  const [input, setInput] = useState(String(page + 1));
  useEffect(() => setInput(String(page + 1)), [page]);
  const go = () => {
    const n = Math.floor(Number(input));
    if (Number.isFinite(n) && n >= 1 && (pages == null || n <= pages)) onPage(n - 1);
    else setInput(String(page + 1));
  };
  return (
    <div className="flex h-10 shrink-0 items-center gap-3 border-t border-line bg-surface px-3 text-[12px] text-fg-light" data-testid="table-footer">
      {mode === "data" && (
        <>
          <div className="flex items-center gap-1">
            <Button size="tiny" variant="ghost" aria-label="Previous page" disabled={page === 0} onClick={() => onPage(page - 1)} icon={<ChevronLeft className="h-3.5 w-3.5" />} />
            <span>Page</span>
            <input
              aria-label="Page"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onBlur={go}
              onKeyDown={(e) => e.key === "Enter" && go()}
              className="h-6 w-10 rounded border border-line-strong bg-surface-2 text-center text-[12px] text-fg"
              data-testid="page-input"
            />
            <span data-testid="page-number">{pages != null ? `of ${pages.toLocaleString()}` : ""}</span>
            <Button size="tiny" variant="ghost" aria-label="Next page" disabled={!hasNext} onClick={() => onPage(page + 1)} icon={<ChevronRight className="h-3.5 w-3.5" />} />
          </div>
          <Select aria-label="Rows per page" value={pageSize} onChange={(e) => onPageSize(Number(e.target.value))} className="h-6 text-[12px]">
            {PAGE_SIZES.map((n) => (
              <option key={n} value={n}>
                {n} rows
              </option>
            ))}
          </Select>
          <span data-testid="record-count">
            {count?.count == null ? "" : `${count.estimated ? "~" : ""}${count.count.toLocaleString()} ${count.count === 1 ? "record" : "records"}`}
          </span>
        </>
      )}
      <span className="flex-1" />
      <Segmented
        value={mode}
        onChange={onMode}
        options={[
          { value: "data", label: "Data" },
          { value: "definition", label: "Definition" },
        ]}
      />
    </div>
  );
}
