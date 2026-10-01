import { useId, useMemo, useState } from "react";

export type ChartSeries = { name: string; color: string; points: { ts: string; value: number }[] };

const W = 600;
const H = 160;
const PAD = { l: 52, r: 8, t: 8, b: 20 };

/** Rounds max up to 1, 2, or 5 times a power of ten. */
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

function timeLabel(t: number, spanMs: number): string {
  const d = new Date(t);
  if (spanMs > 2 * 86400_000) return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/** A small time-series chart over [from, to] (spec §8.7). */
export function LineChart({
  series,
  from,
  to,
  format,
  label,
}: {
  series: ChartSeries[];
  from: number;
  to: number;
  format: (v: number) => string;
  label: string;
}) {
  const clip = useId();
  const [hover, setHover] = useState<number | null>(null);
  const max = useMemo(() => niceMax(Math.max(0, ...series.flatMap((s) => s.points.map((p) => p.value)))), [series]);
  const span = Math.max(1, to - from);
  const x = (t: number) => PAD.l + ((t - from) / span) * (W - PAD.l - PAD.r);
  const y = (v: number) => PAD.t + (1 - v / max) * (H - PAD.t - PAD.b);
  const empty = series.every((s) => s.points.length === 0);

  // The hovered time snaps to the nearest point of the first series.
  const near = (t: number) => {
    let best: { ts: number; i: number } | null = null;
    const pts = series[0]?.points ?? [];
    pts.forEach((p, i) => {
      const ts = Date.parse(p.ts);
      if (!best || Math.abs(ts - t) < Math.abs(best.ts - t)) best = { ts, i };
    });
    return best as { ts: number; i: number } | null;
  };
  const hv = hover == null ? null : near(hover);

  return (
    <figure className="relative">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="h-40 w-full"
        role="img"
        aria-label={label}
        onMouseMove={(e) => {
          const r = e.currentTarget.getBoundingClientRect();
          const px = ((e.clientX - r.left) / r.width) * W;
          setHover(from + ((px - PAD.l) / (W - PAD.l - PAD.r)) * span);
        }}
        onMouseLeave={() => setHover(null)}
      >
        <defs>
          <clipPath id={clip}>
            <rect x={PAD.l} y={PAD.t} width={W - PAD.l - PAD.r} height={H - PAD.t - PAD.b} />
          </clipPath>
        </defs>
        {[0, 0.5, 1].map((f) => (
          <g key={f}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(max * f)} y2={y(max * f)} stroke="var(--border)" strokeDasharray={f === 0 ? undefined : "3 3"} />
            <text x={PAD.l - 6} y={y(max * f) + 3} textAnchor="end" fontSize="10" fill="var(--muted)">
              {format(max * f)}
            </text>
          </g>
        ))}
        {[from, from + span / 2, to].map((t, i) => (
          <text key={i} x={x(t)} y={H - 5} textAnchor={i === 0 ? "start" : i === 2 ? "end" : "middle"} fontSize="10" fill="var(--muted)">
            {timeLabel(t, span)}
          </text>
        ))}
        <g clipPath={`url(#${clip})`}>
          {series.map((s) => {
            const pts = s.points.map((p) => [x(Date.parse(p.ts)), y(p.value)] as const);
            if (pts.length === 0) return null;
            const d = pts.map(([px, py], i) => `${i ? "L" : "M"}${px.toFixed(1)},${py.toFixed(1)}`).join("");
            return (
              <g key={s.name}>
                {series.length === 1 && pts.length > 1 && (
                  <path d={`${d}L${pts[pts.length - 1][0]},${y(0)}L${pts[0][0]},${y(0)}Z`} fill={s.color} opacity="0.12" />
                )}
                <path d={d} fill="none" stroke={s.color} strokeWidth="1.75" strokeLinejoin="round" />
                {pts.length < 3 && pts.map(([px, py], i) => <circle key={i} cx={px} cy={py} r="2.5" fill={s.color} />)}
              </g>
            );
          })}
          {hv && <line x1={x(hv.ts)} x2={x(hv.ts)} y1={PAD.t} y2={H - PAD.b} stroke="var(--muted)" strokeWidth="1" />}
        </g>
        {empty && (
          <text x={W / 2} y={H / 2} textAnchor="middle" fontSize="12" fill="var(--muted)">
            No data yet
          </text>
        )}
      </svg>
      {hv && (
        <figcaption className="pointer-events-none absolute top-1 right-2 rounded border border-line bg-surface px-2 py-1 text-xs shadow-sm">
          <div className="text-muted">{new Date(hv.ts).toLocaleString()}</div>
          {series.map((s) => {
            const p = s.points.find((q) => Date.parse(q.ts) === hv.ts);
            return p ? (
              <div key={s.name}>
                <span style={{ color: s.color }}>●</span> {s.name}: {format(p.value)}
              </div>
            ) : null;
          })}
        </figcaption>
      )}
    </figure>
  );
}
