import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDownToLine, ArrowUpToLine, Check, ClipboardCopy, Clock, Copy, Pause, Play, Radio, X } from "lucide-react";
import { api, type ApiError } from "../api/client";
import { clock, copyText, dateTime } from "../mock/util";
import { cn } from "../utils/cn";
import { useStore } from "../store/store";
import { Badge, Button, Card, EmptyState, Input, SearchInput, Select, Spinner, type Tone } from "./ui";

export interface LogLine {
  id: number;
  ts: number;
  serverId: string;
  appId: string;
  source: string;
  stream: string;
  severity: string;
  message: string;
}

export const SEVERITIES = ["debug", "info", "warning", "error", "critical", "unknown"] as const;
const STREAMS = ["stdout", "stderr", "journal"] as const;
const SOURCES = ["pm2", "systemd", "agent"] as const;
const RANGES = [
  { id: "live", label: "Live tail", ms: 0 },
  { id: "1h", label: "Last hour", ms: 3_600_000 },
  { id: "6h", label: "Last 6 hours", ms: 6 * 3_600_000 },
  { id: "24h", label: "Last 24 hours", ms: 24 * 3_600_000 },
  { id: "3d", label: "Last 3 days", ms: 3 * 86_400_000 },
  { id: "7d", label: "Last 7 days", ms: 7 * 86_400_000 },
  { id: "custom", label: "Custom range", ms: 0 },
];
const MAX_LINES = 5000; // kept in the browser; older lines are re-fetched on demand
const PAGE = 300;

export const severityTone: Record<string, Tone> = { debug: "neutral", info: "info", warning: "warn", error: "crit", critical: "crit", unknown: "neutral" };
export const LevelBadge = ({ level }: { level: string }) => (
  <Badge tone={severityTone[level] ?? "neutral"} className="w-[64px] justify-center !px-1.5 !py-0.5 font-mono !text-[10px] uppercase">
    {level === "warning" ? "warn" : level === "critical" ? "crit" : level}
  </Badge>
);

export const fmtLine = (l: LogLine) => `${new Date(l.ts).toISOString()} ${l.severity.toUpperCase()} [${l.source}/${l.stream}] ${l.message}`;

export function Highlight({ text, q }: { text: string; q: string }) {
  const words = q.trim().split(/\s+/).filter(Boolean);
  if (!words.length) return <>{text}</>;
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})`, "i");
  return (
    <>
      {text.split(re).map((p, i) => (
        <Fragment key={i}>{i % 2 === 1 ? <mark className="rounded bg-warn-soft px-0.5 text-warn">{p}</mark> : p}</Fragment>
      ))}
    </>
  );
}

function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button onClick={onClick} className={cn("rounded-full border px-3 py-1 text-[11px] font-medium transition", active ? "border-transparent bg-accent text-accent-fg" : "border-line text-muted hover:text-fg")}>
      {children}
    </button>
  );
}

function toggle<T>(set: Set<T>, v: T): Set<T> {
  const n = new Set(set);
  n.has(v) ? n.delete(v) : n.add(v);
  return n;
}

const toLocalInput = (ms: number) => new Date(ms - new Date().getTimezoneOffset() * 60_000).toISOString().slice(0, 16);

/** Logs for one app (appId), one server (serverId) or everything. */
export function LogViewer({ appId, serverId, showApp, canStream = true, note }: { appId?: string; serverId?: string; showApp?: boolean; canStream?: boolean; note?: ReactNode }) {
  const { toast, getApp, getServer } = useStore();
  const [params] = useSearchParams();
  const [q, setQ] = useState(params.get("q") ?? "");
  const [query, setQuery] = useState(q); // debounced
  const [levels, setLevels] = useState<Set<string>>(new Set(SEVERITIES));
  const [streams, setStreams] = useState<Set<string>>(new Set(STREAMS));
  const [sources, setSources] = useState<Set<string>>(new Set(SOURCES));
  const [range, setRange] = useState("live");
  const [customFrom, setCustomFrom] = useState(toLocalInput(Date.now() - 3_600_000));
  const [customTo, setCustomTo] = useState(toLocalInput(Date.now()));
  const [lines, setLines] = useState<LogLine[]>([]);
  const [nextBefore, setNextBefore] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [paused, setPaused] = useState(false);
  const [held, setHeld] = useState<LogLine[]>([]);
  const [dropped, setDropped] = useState(0);
  const [wsState, setWsState] = useState<"off" | "connecting" | "open">("off");
  const [autoScroll, setAutoScroll] = useState(true);
  const [atBottom, setAtBottom] = useState(true);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [copied, setCopied] = useState<number | null>(null);
  const [showTs, setShowTs] = useState(true);
  const box = useRef<HTMLDivElement>(null);
  const prependedRef = useRef(0);

  useEffect(() => {
    const t = setTimeout(() => setQuery(q), 350);
    return () => clearTimeout(t);
  }, [q]);

  const isLive = range === "live";
  const filterKey = [query, [...levels].join(), [...streams].join(), [...sources].join(), range, customFrom, customTo, appId, serverId].join("|");

  const buildQuery = useCallback(
    (before?: number) => {
      const p = new URLSearchParams({ limit: String(PAGE) });
      if (appId) p.set("app", appId);
      if (serverId) p.set("server", serverId);
      if (query.trim()) p.set("q", query.trim());
      if (levels.size < SEVERITIES.length) p.set("severity", [...levels].join(","));
      if (streams.size < STREAMS.length) p.set("stream", [...streams].join(","));
      if (sources.size < SOURCES.length) p.set("source", [...sources].join(","));
      const preset = RANGES.find((r) => r.id === range);
      if (range === "custom") {
        p.set("from", String(new Date(customFrom).getTime()));
        p.set("to", String(new Date(customTo).getTime()));
      } else if (preset && preset.ms) p.set("from", String(Date.now() - preset.ms));
      if (before) p.set("before", String(before));
      return `/api/logs?${p}`;
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [filterKey],
  );

  // History: newest page for the current filters.
  useEffect(() => {
    let live = true;
    setLoading(true);
    setError(null);
    setSelected(new Set());
    api<{ lines: LogLine[]; nextBefore: number }>("GET", buildQuery())
      .then((r) => {
        if (!live) return;
        setLines([...r.lines].reverse());
        setNextBefore(r.nextBefore);
        setAutoScroll(true);
      })
      .catch((e: ApiError) => live && setError(e.message))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [buildQuery]);

  const matches = useCallback(
    (l: LogLine) => {
      const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
      const msg = l.message.toLowerCase();
      return levels.has(l.severity) && streams.has(l.stream) && sources.has(l.source) && words.every((w) => msg.includes(w));
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [filterKey],
  );

  // Live tail over WebSocket; reconnects while the tab stays in live mode.
  const pausedRef = useRef(paused);
  pausedRef.current = paused;
  useEffect(() => {
    if (!isLive || !canStream) {
      setWsState("off");
      return;
    }
    let ws: WebSocket | null = null;
    let stopped = false;
    let timer = 0;
    const p = new URLSearchParams();
    if (appId) p.set("app", appId);
    if (serverId) p.set("server", serverId);
    const connect = () => {
      setWsState("connecting");
      ws = new WebSocket(`${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/ws/logs?${p}`);
      ws.onopen = () => setWsState("open");
      ws.onmessage = (e) => {
        const m = JSON.parse(e.data) as { type: string; data: any };
        if (m.type === "dropped") return setDropped((n) => n + (m.data as number));
        const fresh = (m.data as LogLine[]).filter(matches);
        if (!fresh.length) return;
        if (pausedRef.current) setHeld((h) => [...h, ...fresh].slice(-MAX_LINES));
        else setLines((l) => [...l, ...fresh].slice(-MAX_LINES));
      };
      ws.onclose = () => {
        setWsState("connecting");
        if (!stopped) timer = window.setTimeout(connect, 3000);
      };
    };
    connect();
    return () => {
      stopped = true;
      clearTimeout(timer);
      ws?.close();
    };
  }, [isLive, canStream, appId, serverId, matches]);

  const virt = useVirtualizer({ count: lines.length, getScrollElement: () => box.current, estimateSize: () => 21, overscan: 30 });

  useLayoutEffect(() => {
    if (prependedRef.current) {
      virt.scrollToIndex(prependedRef.current, { align: "start" });
      prependedRef.current = 0;
    } else if (autoScroll && lines.length) {
      virt.scrollToIndex(lines.length - 1, { align: "end" });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lines.length]);

  const onScroll = () => {
    const el = box.current;
    if (!el) return;
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    setAtBottom(bottom);
    setAutoScroll(bottom);
  };
  const jump = () => {
    setAutoScroll(true);
    if (lines.length) virt.scrollToIndex(lines.length - 1, { align: "end" });
  };
  const loadOlder = async () => {
    if (!nextBefore) return;
    setLoadingOlder(true);
    try {
      const r = await api<{ lines: LogLine[]; nextBefore: number }>("GET", buildQuery(nextBefore));
      prependedRef.current = r.lines.length;
      setAutoScroll(false);
      setLines((l) => [...[...r.lines].reverse(), ...l]);
      setNextBefore(r.nextBefore);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setLoadingOlder(false);
  };
  const togglePause = () => {
    if (paused) {
      setLines((l) => [...l, ...held].slice(-MAX_LINES));
      setHeld([]);
    }
    setPaused(!paused);
  };
  const copyLine = (l: LogLine) => {
    copyText(fmtLine(l));
    setCopied(l.id);
    setTimeout(() => setCopied(null), 1200);
  };
  const copySelected = () => {
    copyText(lines.filter((l) => selected.has(l.id)).map(fmtLine).join("\n"));
    toast(`Copied ${selected.size} line${selected.size === 1 ? "" : "s"}`);
  };
  const reset = () => {
    setQ("");
    setLevels(new Set(SEVERITIES));
    setStreams(new Set(STREAMS));
    setSources(new Set(SOURCES));
    setRange("live");
  };
  const today = useMemo(() => new Date().toDateString(), []);

  return (
    <Card pad={false}>
      <div className="space-y-3 border-b border-line p-4">
        <div className="flex flex-wrap items-center gap-2">
          <span className={cn("inline-flex items-center gap-2 rounded-full px-4 py-2 text-xs font-semibold", isLive && canStream && wsState === "open" ? "bg-crit-soft text-crit" : "border border-line text-muted")}>
            <Radio size={14} className={isLive && wsState === "open" && !paused ? "pulse-dot" : ""} />
            {!isLive ? "History" : !canStream ? "Live unavailable" : wsState !== "open" ? "Connecting…" : paused ? "LIVE · paused" : "LIVE"}
          </span>
          <Button size="sm" variant="secondary" disabled={!isLive || !canStream} onClick={togglePause}>
            {paused ? <Play size={13} /> : <Pause size={13} />} {paused ? `Resume${held.length ? ` (+${held.length})` : ""}` : "Pause"}
          </Button>
          <Button size="sm" variant={autoScroll ? "primary" : "secondary"} onClick={() => (autoScroll ? setAutoScroll(false) : jump())}>
            <ArrowDownToLine size={13} /> Auto-scroll {autoScroll ? "on" : "off"}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setShowTs(!showTs)}><Clock size={13} /> {showTs ? "Hide" : "Show"} timestamps</Button>
          <div className="ml-auto flex items-center gap-2 text-xs text-muted">
            <span className="tabular-nums">{lines.length.toLocaleString()} lines loaded</span>
            {selected.size > 0 && (
              <>
                <Button size="sm" variant="secondary" onClick={copySelected}><ClipboardCopy size={13} /> Copy {selected.size} selected</Button>
                <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())}>Clear</Button>
              </>
            )}
          </div>
        </div>
        {note}
        {dropped > 0 && (
          <div className="flex items-center justify-between rounded-2xl bg-warn-soft px-4 py-2 text-xs text-warn">
            <span>{dropped.toLocaleString()} live lines were skipped because this browser fell behind. They are stored; reload to see them.</span>
            <button onClick={() => setDropped(0)} aria-label="Dismiss"><X size={13} /></button>
          </div>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <SearchInput value={q} onChange={setQ} placeholder="Search words or prefixes, e.g. ECONNREFUSED 5432" className="w-full sm:w-80" />
          <div className="flex flex-wrap gap-1.5">
            {SEVERITIES.map((l) => <Chip key={l} active={levels.has(l)} onClick={() => setLevels(toggle(levels, l))}>{l}</Chip>)}
          </div>
          <span className="hidden h-5 w-px bg-line sm:block" />
          <div className="flex flex-wrap gap-1.5">
            {STREAMS.map((s) => <Chip key={s} active={streams.has(s)} onClick={() => setStreams(toggle(streams, s))}>{s}</Chip>)}
          </div>
          <span className="hidden h-5 w-px bg-line sm:block" />
          <div className="flex flex-wrap gap-1.5">
            {SOURCES.map((s) => <Chip key={s} active={sources.has(s)} onClick={() => setSources(toggle(sources, s))}>{s}</Chip>)}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={range} onChange={(e) => { setRange(e.target.value); setPaused(false); setHeld([]); }} className="!w-auto !py-1.5 text-xs">
            {RANGES.map((r) => <option key={r.id} value={r.id}>{r.label}</option>)}
          </Select>
          {range === "custom" && (
            <>
              <Input type="datetime-local" value={customFrom} onChange={(e) => setCustomFrom(e.target.value)} className="!w-auto !py-1.5 text-xs" aria-label="From" />
              <span className="text-xs text-muted">to</span>
              <Input type="datetime-local" value={customTo} onChange={(e) => setCustomTo(e.target.value)} className="!w-auto !py-1.5 text-xs" aria-label="To" />
            </>
          )}
        </div>
      </div>

      <div className="relative">
        <div ref={box} onScroll={onScroll} className="h-[560px] overflow-auto bg-surface2/50 font-mono text-[12px] leading-5">
          {loading ? (
            <div className="flex h-full items-center justify-center text-muted"><Spinner /></div>
          ) : error ? (
            <div className="p-6"><EmptyState title="Could not load logs" desc={error} /></div>
          ) : lines.length === 0 ? (
            <div className="p-6">
              <EmptyState title="No log lines match" desc={isLive ? "New lines appear here as the application writes them." : "Adjust the filters or time range."} action={<Button variant="secondary" size="sm" onClick={reset}><X size={13} /> Reset filters</Button>} />
            </div>
          ) : (
            <>
              {nextBefore > 0 && (
                <div className="flex justify-center py-2">
                  <Button size="sm" variant="ghost" onClick={loadOlder} disabled={loadingOlder}>{loadingOlder ? <Spinner size={13} /> : <ArrowUpToLine size={13} />} Load older lines</Button>
                </div>
              )}
              <div style={{ height: virt.getTotalSize(), position: "relative" }}>
                {virt.getVirtualItems().map((row) => {
                  const l = lines[row.index];
                  const bad = l.severity === "error" || l.severity === "critical";
                  const appName = showApp ? (l.appId ? getApp(l.appId)?.name ?? "deleted app" : getServer(l.serverId)?.name ?? "agent") : null;
                  return (
                    <div key={l.id} data-index={row.index} ref={virt.measureElement} style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${row.start}px)` }}
                      className={cn("group flex items-start gap-2 px-3 py-0.5 hover:bg-surface", selected.has(l.id) && "bg-info-soft/60", bad && "bg-crit-soft/25")}>
                      <input type="checkbox" checked={selected.has(l.id)} onChange={() => setSelected(toggle(selected, l.id))} className="mt-1 h-3 w-3 shrink-0 accent-current" aria-label="Select line" />
                      {showTs && <span className="shrink-0 text-muted" title={dateTime(l.ts)}>{new Date(l.ts).toDateString() !== today && `${new Date(l.ts).toLocaleDateString(undefined, { month: "short", day: "numeric" })} `}{clock(l.ts)}</span>}
                      <LevelBadge level={l.severity} />
                      {appName && <span className="hidden max-w-[140px] shrink-0 truncate text-muted md:inline" title={appName}>{appName}</span>}
                      <span className="hidden shrink-0 text-muted sm:inline">{l.source}/{l.stream}</span>
                      <span className={cn("min-w-0 flex-1 whitespace-pre-wrap break-all", bad && "text-crit", l.severity === "warning" && "text-warn", l.severity === "debug" && "text-muted")}>
                        <Highlight text={l.message} q={query} />
                      </span>
                      <button onClick={() => copyLine(l)} className="shrink-0 text-muted opacity-0 transition hover:text-fg group-hover:opacity-100" title="Copy line">
                        {copied === l.id ? <Check size={13} className="text-ok" /> : <Copy size={13} />}
                      </button>
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>
        {!atBottom && lines.length > 0 && (
          <button onClick={jump} className="absolute bottom-4 right-6 inline-flex items-center gap-1.5 rounded-full bg-accent px-4 py-2 text-xs font-medium text-accent-fg shadow-lg">
            <ArrowDownToLine size={13} /> Jump to newest
          </button>
        )}
      </div>
    </Card>
  );
}
