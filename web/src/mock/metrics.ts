import type { App, Metric, Point, Range, Server } from "./types";
import { DAY, NOW, clamp, rng } from "./util";

export const RANGES: { id: Range; label: string; points: number; stepMs: number }[] = [
  { id: "1h", label: "1 hour", points: 60, stepMs: 60_000 },
  { id: "6h", label: "6 hours", points: 72, stepMs: 300_000 },
  { id: "24h", label: "24 hours", points: 96, stepMs: 900_000 },
  { id: "7d", label: "7 days", points: 84, stepMs: 7_200_000 },
  { id: "30d", label: "30 days", points: 90, stepMs: 28_800_000 },
];

export interface MetricMeta {
  id: Metric;
  label: string;
  unit: string;
  kind: "area" | "bar";
  max?: number;
}

export function metricMeta(m: Metric, entity: "server" | "app" | "all" = "server"): MetricMeta {
  switch (m) {
    case "cpu": return { id: m, label: "CPU", unit: "%", kind: "area", max: 100 };
    case "ram": return entity === "app" ? { id: m, label: "RAM", unit: " MB", kind: "area" } : { id: m, label: "RAM", unit: "%", kind: "area", max: 100 };
    case "disk": return { id: m, label: "Disk", unit: "%", kind: "area", max: 100 };
    case "netRx": return { id: m, label: "Network receive", unit: " Mbps", kind: "area" };
    case "netTx": return { id: m, label: "Network transmit", unit: " Mbps", kind: "area" };
    case "load": return { id: m, label: "Load average", unit: "", kind: "area" };
    case "latency": return { id: m, label: "Health latency", unit: " ms", kind: "area" };
    case "restarts": return { id: m, label: "Restart count", unit: "", kind: "bar" };
    case "errors": return { id: m, label: "Error count", unit: "", kind: "bar" };
    case "requests": return { id: m, label: "Requests / min", unit: "", kind: "area" };
  }
}

function walk(key: string, range: Range, base: number, opts: { vol?: number; daily?: number; trend?: number; min?: number; max?: number; spikes?: number }): Point[] {
  const cfg = RANGES.find((x) => x.id === range)!;
  const r = rng(key + range);
  const { vol = 0.12, daily = 0.18, trend = 0, min = 0, max = Infinity, spikes = 0.02 } = opts;
  const pts: Point[] = [];
  let v = base;
  for (let i = 0; i < cfg.points; i++) {
    const t = NOW - (cfg.points - 1 - i) * cfg.stepMs;
    const phase = ((t % DAY) / DAY) * Math.PI * 2;
    const seasonal = 1 + daily * Math.sin(phase - 1.2);
    const tr = 1 - trend * (1 - i / (cfg.points - 1));
    const target = base * seasonal * tr;
    v = v * 0.6 + target * 0.4 + (r() - 0.5) * 2 * vol * base;
    if (r() < spikes) v += base * (0.4 + r() * 0.8);
    pts.push({ t, v: clamp(v, min, max) });
  }
  pts[pts.length - 1].v = clamp(base, min, max);
  return pts;
}

function counts(key: string, range: Range, total24h: number, spikeFactor = 1): Point[] {
  const cfg = RANGES.find((x) => x.id === range)!;
  const r = rng(key + range + "bars");
  const perBucket = (total24h * cfg.stepMs) / DAY;
  return Array.from({ length: cfg.points }, (_, i) => {
    const t = NOW - (cfg.points - 1 - i) * cfg.stepMs;
    const recent = i > cfg.points * 0.85 ? spikeFactor : 1;
    const v = Math.max(0, Math.round(perBucket * recent * (0.2 + r() * 1.6) * (r() < 0.12 ? 3 : 1)));
    return { t, v };
  });
}

export function serverSeries(s: Server, m: Metric, range: Range): Point[] {
  const k = s.id + m;
  switch (m) {
    case "cpu": return walk(k, range, s.cpu, { max: 100, vol: 0.1, spikes: s.cpu > 80 ? 0.06 : 0.02 });
    case "ram": return walk(k, range, s.ram, { max: 100, vol: 0.03, daily: 0.05, trend: 0.04 });
    case "disk": return walk(k, range, s.disk, { max: 100, vol: 0.002, daily: 0, trend: s.disk > 85 ? 0.12 : 0.04, spikes: 0 });
    case "netRx": return walk(k, range, Math.max(0.1, s.netRx), { vol: 0.25, daily: 0.4 });
    case "netTx": return walk(k, range, Math.max(0.1, s.netTx), { vol: 0.25, daily: 0.4 });
    case "load": return walk(k, range, Math.max(0.05, s.load[0]), { vol: 0.12, max: s.cpuCores * 3 });
    default: return walk(k, range, 10, {});
  }
}

export function appSeries(a: App, m: Metric, range: Range): Point[] {
  const k = a.id + m;
  switch (m) {
    case "cpu": return walk(k, range, Math.max(0.3, a.cpu), { max: 100, vol: 0.2, spikes: a.cpu > 50 ? 0.08 : 0.03 });
    case "ram": return walk(k, range, Math.max(10, a.ram), { vol: 0.04, daily: 0.05, trend: a.status === "warning" ? 0.3 : 0.06 });
    case "latency": return walk(k, range, a.latency ?? 90, { vol: 0.2, daily: 0.15, spikes: 0.04 });
    case "restarts": return counts(k, range, Math.min(6, a.restarts / 6 + (a.status === "crash-loop" ? 40 : 0.4)), a.status === "crash-loop" ? 3 : 1);
    case "errors": return counts(k, range, a.errors24h, a.id === "app_playpresence" ? 5 : 1);
    case "requests": return walk(k, range, a.logRate * 14 + 30, { vol: 0.25, daily: 0.35 });
    default: return serverSeries({ cpu: 20, ram: 40, disk: 40, netRx: 5, netTx: 5, load: [1, 1, 1], cpuCores: 4, id: a.serverId } as Server, m, range);
  }
}


export const APP_METRICS: Metric[] = ["cpu", "ram", "latency", "restarts", "errors", "requests"];
export const SERVER_METRICS: Metric[] = ["cpu", "ram", "disk", "netRx", "netTx", "load"];
