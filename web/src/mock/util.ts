export const NOW = Date.now();
export const MIN = 60_000;
export const HOUR = 3_600_000;
export const DAY = 86_400_000;
export const ago = (minutes: number) => NOW - minutes * MIN;

export function hashStr(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

export function rng(seed: string) {
  let a = hashStr(seed);
  return () => {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function pick<T>(r: () => number, arr: T[]): T {
  return arr[Math.floor(r() * arr.length)];
}

export function fmtMB(mb: number): string {
  if (mb >= 1024 * 1024) return (mb / 1024 / 1024).toFixed(1) + " TB";
  if (mb >= 1024) return (mb / 1024).toFixed(mb >= 10240 ? 0 : 1) + " GB";
  if (mb >= 10) return Math.round(mb) + " MB";
  if (mb >= 1) return mb.toFixed(1) + " MB";
  if (mb * 1024 >= 1) return Math.round(mb * 1024) + " KB";
  return mb > 0 ? "<1 KB" : "0 B";
}

export function fmtDur(sec: number): string {
  if (sec <= 0) return "—";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${Math.floor(sec % 60)}s`;
  return `${Math.floor(sec)}s`;
}

export function relTime(ts: number): string {
  const diff = Date.now() - ts;
  const s = Math.floor(diff / 1000);
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

const p2 = (n: number) => String(n).padStart(2, "0");
export function clock(ts: number, seconds = true): string {
  const d = new Date(ts);
  return `${p2(d.getHours())}:${p2(d.getMinutes())}${seconds ? ":" + p2(d.getSeconds()) : ""}`;
}
export function dateTime(ts: number): string {
  const d = new Date(ts);
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} ${clock(ts)}`;
}
export function dayLabel(offset: number): string {
  if (offset === 0) return "Today";
  if (offset === 1) return "Yesterday";
  const d = new Date(Date.now() - offset * DAY);
  return d.toLocaleDateString(undefined, { weekday: "short", day: "numeric" });
}

export const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));
export const round1 = (v: number) => Math.round(v * 10) / 10;
export const uid = (p = "id") => `${p}_${Math.random().toString(36).slice(2, 9)}`;

export function copyText(text: string) {
  try {
    if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text);
  } catch {
    /* ignore */
  }
  return Promise.resolve();
}
