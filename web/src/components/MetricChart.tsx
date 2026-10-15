import { useMemo, useRef, useState } from "react";
import type { Point } from "../mock/types";
import { cn } from "../utils/cn";
import { clock, dateTime } from "../mock/util";
import { Card } from "./ui";

interface Props {
  data: Point[];
  unit?: string;
  kind?: "area" | "bar";
  height?: number;
  max?: number;
  threshold?: number;
  color?: string;
  decimals?: number;
}

const fmt = (v: number, d: number) => (Math.abs(v) >= 1000 ? Math.round(v).toLocaleString() : v.toFixed(d));

export function MetricChart({ data, unit = "", kind = "area", height = 180, max, threshold, color = "var(--chart1)", decimals = 1 }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<number | null>(null);
  const { lo, hi, path, area, bars, thY } = useMemo(() => {
    const vals = data.map((d) => d.v);
    const hi0 = Math.max(max ?? 0, ...vals, threshold ?? 0);
    const hi = max ?? (hi0 === 0 ? 1 : hi0 * 1.12);
    const lo = 0;
    const W = 1000;
    const H = 100;
    const x = (i: number) => (data.length <= 1 ? 0 : (i / (data.length - 1)) * W);
    const y = (v: number) => H - ((v - lo) / (hi - lo || 1)) * H;
    const pts = data.map((d, i) => [x(i), y(d.v)]);
    const path = pts.map((p, i) => `${i === 0 ? "M" : "L"}${p[0].toFixed(1)},${p[1].toFixed(1)}`).join(" ");
    const area = pts.length ? `${path} L${W},${H} L0,${H} Z` : "";
    const bw = (W / data.length) * 0.7;
    const bars = data.map((d, i) => ({ x: (i / data.length) * W + (W / data.length) * 0.15, y: y(d.v), w: bw, h: H - y(d.v) }));
    return { lo, hi, path, area, bars, thY: threshold !== undefined ? y(threshold) : null };
  }, [data, max, threshold]);

  const onMove = (e: React.MouseEvent) => {
    const rect = ref.current?.getBoundingClientRect();
    if (!rect || !data.length) return;
    const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
    setHover(Math.round(frac * (data.length - 1)));
  };

  const ticks = [hi, hi * 0.5, lo];
  const span = data.length > 1 ? data[data.length - 1].t - data[0].t : 0;
  const labelFmt = (t: number) => (span > 36 * 3600_000 ? new Date(t).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : clock(t, false));
  const hp = hover !== null ? data[hover] : null;
  const leftPct = hover !== null ? (hover / Math.max(1, data.length - 1)) * 100 : 0;

  return (
    <div className="select-none">
      <div className="flex gap-2">
        <div className="flex flex-col justify-between py-0.5 text-right text-[10px] tabular-nums text-muted" style={{ height }}>
          {ticks.map((t, i) => (
            <span key={i}>{fmt(t, hi > 20 ? 0 : 1)}</span>
          ))}
        </div>
        <div ref={ref} className="relative flex-1" style={{ height }} onMouseMove={onMove} onMouseLeave={() => setHover(null)} onTouchMove={(e) => onMove(e.touches[0] as unknown as React.MouseEvent)}>
          <svg viewBox="0 0 1000 100" preserveAspectRatio="none" className="h-full w-full overflow-visible">
            {[0, 50, 100].map((g) => (
              <line key={g} x1="0" x2="1000" y1={g} y2={g} stroke="var(--line)" strokeWidth="1" vectorEffect="non-scaling-stroke" strokeDasharray={g === 100 ? "" : "3 4"} />
            ))}
            {kind === "area" ? (
              <>
                <path d={area} fill={color} opacity="0.12" />
                <path d={path} fill="none" stroke={color} strokeWidth="2" vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
              </>
            ) : (
              bars.map((b, i) => <rect key={i} x={b.x} y={b.y} width={b.w} height={Math.max(b.h, 0)} fill={color} opacity={hover === i ? 1 : 0.75} rx="1" />)
            )}
            {thY !== null && <line x1="0" x2="1000" y1={thY} y2={thY} stroke="var(--crit)" strokeWidth="1" strokeDasharray="5 4" vectorEffect="non-scaling-stroke" />}
          </svg>
          {hp && (
            <>
              <div className="pointer-events-none absolute top-0 h-full w-px bg-fg/30" style={{ left: `${leftPct}%` }} />
              <div className={cn("pointer-events-none absolute top-1 z-10 whitespace-nowrap rounded-xl border border-line bg-surface px-2.5 py-1.5 text-[11px] shadow-lg", leftPct > 70 ? "-translate-x-full" : "translate-x-2")} style={{ left: `${leftPct}%` }}>
                <div className="font-semibold tabular-nums">
                  {fmt(hp.v, decimals)}
                  {unit}
                </div>
                <div className="text-muted">{dateTime(hp.t)}</div>
              </div>
            </>
          )}
        </div>
      </div>
      <div className="ml-8 mt-1.5 flex justify-between text-[10px] text-muted">
        {[0, 0.25, 0.5, 0.75, 1].map((f) => (
          <span key={f}>{data.length ? labelFmt(data[Math.round(f * (data.length - 1))].t) : ""}</span>
        ))}
      </div>
    </div>
  );
}

export function Stats({ data, unit, decimals = 1, sum }: { data: Point[]; unit: string; decimals?: number; sum?: boolean }) {
  if (!data.length) return null;
  const vals = data.map((d) => d.v);
  const avg = vals.reduce((a, b) => a + b, 0) / vals.length;
  const items: [string, number][] = sum ? [["Total", vals.reduce((a, b) => a + b, 0)], ["Peak bucket", Math.max(...vals)], ["Latest", vals[vals.length - 1]]] : [["Min", Math.min(...vals)], ["Avg", avg], ["Max", Math.max(...vals)], ["Now", vals[vals.length - 1]]];
  return (
    <div className="flex flex-wrap gap-x-5 gap-y-1 text-xs">
      {items.map(([k, v]) => (
        <span key={k} className="text-muted">
          {k} <span className="font-semibold tabular-nums text-fg">{fmt(v, decimals)}{unit}</span>
        </span>
      ))}
    </div>
  );
}

export function MetricPanel({ title, data, unit = "", kind = "area", max, threshold, height = 170, decimals = 1, sub }: { title: string; data: Point[]; unit?: string; kind?: "area" | "bar"; max?: number; threshold?: number; height?: number; decimals?: number; sub?: string }) {
  return (
    <Card>
      <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h4 className="text-sm font-semibold">{title}</h4>
          {sub && <p className="text-[11px] text-muted">{sub}</p>}
        </div>
        <Stats data={data} unit={unit} decimals={decimals} sum={kind === "bar"} />
      </div>
      <MetricChart data={data} unit={unit} kind={kind} max={max} threshold={threshold} height={height} decimals={decimals} />
    </Card>
  );
}

export function Sparkline({ data, className, color = "var(--chart1)" }: { data: number[]; className?: string; color?: string }) {
  const hi = Math.max(...data, 1);
  const lo = Math.min(...data, 0);
  const pts = data.map((v, i) => `${(i / Math.max(1, data.length - 1)) * 100},${30 - ((v - lo) / (hi - lo || 1)) * 28}`).join(" ");
  return (
    <svg viewBox="0 0 100 30" preserveAspectRatio="none" className={cn("h-8 w-full", className)}>
      <polyline points={pts} fill="none" stroke={color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}
