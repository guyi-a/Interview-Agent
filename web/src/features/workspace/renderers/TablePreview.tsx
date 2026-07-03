import { useMemo } from "react";

const MAX_ROWS = 500;
const MAX_COLS = 50;

export function isTablePath(path: string): boolean {
  const lower = path.toLowerCase();
  return lower.endsWith(".csv") || lower.endsWith(".tsv");
}

function parse(text: string, sep: string) {
  const lines = text.split(/\r?\n/).filter((l) => l.length > 0);
  if (lines.length === 0) {
    return { headers: [] as string[], rows: [] as string[][], tooManyRows: false, tooManyCols: false };
  }
  const rawHeaders = lines[0].split(sep);
  const tooManyCols = rawHeaders.length > MAX_COLS;
  const headers = rawHeaders.slice(0, MAX_COLS);
  const tooManyRows = lines.length - 1 > MAX_ROWS;
  const rows = lines
    .slice(1, MAX_ROWS + 1)
    .map((l) => l.split(sep).slice(0, MAX_COLS));
  return { headers, rows, tooManyRows, tooManyCols };
}

export function TablePreview({
  content,
  path,
}: {
  content: string;
  path: string;
}) {
  const sep = path.toLowerCase().endsWith(".tsv") ? "\t" : ",";
  const { headers, rows, tooManyRows, tooManyCols } = useMemo(
    () => parse(content, sep),
    [content, sep],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-auto scrollbar-subtle">
        <table className="border-collapse font-mono text-[11px]">
          <thead>
            <tr>
              <th className="sticky top-0 left-0 z-20 min-w-[40px] bg-subtle border-r border-b border-rule px-2 py-1.5 text-[10px] font-normal text-muted select-none" />
              {headers.map((h, i) => (
                <th
                  key={i}
                  className="sticky top-0 z-10 min-w-[100px] bg-subtle border-r border-b border-rule px-2 py-1.5 text-left font-medium text-ink"
                >
                  {h || `col_${i + 1}`}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, r) => (
              <tr key={r}>
                <td className="sticky left-0 z-10 bg-subtle/60 border-r border-b border-rule px-2 py-1 text-[10px] text-muted text-center select-none tabular-nums">
                  {r + 1}
                </td>
                {headers.map((_, c) => (
                  <td
                    key={c}
                    className="border-r border-b border-rule/70 px-2 py-1 text-ink whitespace-nowrap"
                    title={row[c] ?? ""}
                  >
                    {row[c] ?? ""}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {(tooManyRows || tooManyCols) && (
        <div className="shrink-0 border-t border-rule px-3 py-1.5 font-mono text-[10px] text-muted">
          {tooManyRows && `已截断到前 ${MAX_ROWS} 行`}
          {tooManyRows && tooManyCols && " · "}
          {tooManyCols && `已截断到前 ${MAX_COLS} 列`}
          {" · "}复杂 CSV（含引号转义/多行 cell）建议下载查看
        </div>
      )}
    </div>
  );
}
