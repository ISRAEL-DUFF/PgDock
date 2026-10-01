/** RFC 4180 CSV of a result grid; NULL is an empty field. */
export function toCSV(columns: string[], rows: (string | null | undefined)[][]): string {
  const field = (v: string | null | undefined) => {
    if (v == null) return "";
    // Guard spreadsheet formula injection on values that start a formula
    // (numbers like -2 stay as they are).
    const formula = /^[=+\-@\t\r]/.test(v) && !/^[+-]?\d+(\.\d+)?([eE][+-]?\d+)?$/.test(v);
    const s = formula ? `'${v}` : v;
    return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  return [columns, ...rows].map((r) => r.map(field).join(",")).join("\r\n") + "\r\n";
}

/** Starts a browser download of text. */
export function download(filename: string, text: string, type = "text/csv;charset=utf-8") {
  const url = URL.createObjectURL(new Blob([text], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
