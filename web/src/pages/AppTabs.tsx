import { useEffect, useState, type ReactNode } from "react";
import { type Forecasts } from "../components/DiskRescue";
import { Link, useNavigate } from "react-router-dom";
import { Activity, AlertOctagon, AlertTriangle, BarChart3, Cpu, FileCode2, Flame, FlaskConical, FolderTree, FolderX, GitBranch, Globe, HardDrive, HeartPulse, History, Info, KeyRound, ListChecks, ListTree, Lock, MemoryStick, Pencil, RefreshCw, ScrollText, Settings, SlidersHorizontal, Timer, Trash2, Unplug } from "lucide-react";
import { useStore } from "../store/store";
import { useApp } from "./AppDetail";
import { api, useApi, type ApiError } from "../api/client";
import type { App, AppFlags, Range } from "../mock/types";
import { clock, dateTime, fmtDur, fmtMB, relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, HealthBadge, Input, KV, LoadingState, MetricCard, Modal, SectionTitle, Select, Spinner, Table, Td, Textarea, TimeRangeSelector, Toggle, Tr, AppStatus, IncidentStatusBadge, SeverityBadge, toneDot } from "../components/ui";
import { cn } from "../utils/cn";
import { GitSummary, IncidentActions, StorageBreakdown } from "../components/domain";
import { CommandRunner } from "../components/CommandRunner";
import { LogViewer } from "../components/LogViewer";
import { MetricPanel } from "../components/MetricChart";
import { CADDY_EXAMPLE, FileEditor } from "../components/FileEditor";

interface AppEventRow {
  id: number;
  ts: number;
  kind: string;
  text: string;
}
const evTone: Record<string, "ok" | "warn" | "crit" | "info" | "neutral"> = { status: "warn", added: "ok", discovery: "info" };

/** A banner on the app overview: an icon, then the message. */
function Notice({ tone, icon, children }: { tone: "warn" | "crit" | "info" | "neutral"; icon: ReactNode; children: ReactNode }) {
  const cls = { warn: "bg-warn-soft text-warn", crit: "bg-crit-soft text-crit", info: "bg-info-soft text-info", neutral: "bg-surface2 text-fg" }[tone];
  return (
    <div className={cn("flex flex-wrap items-start gap-3 rounded-2xl px-4 py-3 text-sm", cls)}>
      <span className="mt-0.5 shrink-0">{icon}</span>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

export function AppOverview() {
  const a = useApp()!;
  const { servers, incidents, upsertApp, toast } = useStore();
  const s = servers.find((x) => x.id === a.serverId);
  const events = useApi<AppEventRow[]>(`/api/apps/${a.id}/events`);
  const leak = useApi<Forecasts>("/api/forecasts").data?.apps?.[a.id];
  const active = incidents.filter((i) => i.appId === a.id && i.status === "active");
  const live = !!a.pid;
  const useDomain = async () => {
    try {
      upsertApp(await api<App>("PATCH", `/api/apps/${a.id}`, { domain: a.suggestedDomain }));
      toast(`Domain set to ${a.suggestedDomain}`);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const proc = a.manager === "PM2" ? `#${a.pm2Id ?? "—"} · ${a.pm2Name}${a.instances > 1 ? ` · ${a.instances} instances` : ""}` : a.manager === "systemd" ? a.unit : "—";
  return (
    <div className="space-y-6">
      {s?.status === "disconnected" && <Notice tone="warn" icon={<Unplug size={16} />}>The agent on {s.name} is disconnected. Status is unknown and values are the last known state.</Notice>}
      {leak && (
        <Notice tone="warn" icon={<MemoryStick size={16} />}>
          Possible memory leak: memory has grown steadily from {leak.fromMb} MB to {leak.nowMb} MB since the process started {relTime(leak.since)} (about +{leak.pctPerDay}% a day). A restart frees it for a while; a fix in the code ends it.
        </Notice>
      )}
      {a.status === "crash-loop" && <Notice tone="crit" icon={<Flame size={16} />}>Crash loop detected ({a.restarts} restarts, manager state “{a.rawStatus}”). The process exits right after starting.</Notice>}
      {a.manager !== "standalone" && !a.processFound && s?.status !== "disconnected" && (
        <Notice tone="crit" icon={<AlertTriangle size={16} />}>{a.manager} on {s?.name} does not know <span className="font-mono">{a.processKey}</span>. Check the process name in Settings, or start it on the server.</Notice>
      )}
      {a.manager === "standalone" && (
        <Notice tone="neutral" icon={<Info size={16} className="text-muted" />}>
          {a.rawStatus.startsWith("served by Caddy") ? (
            <>Static site, {a.rawStatus}. {a.health.enabled ? "Its health check decides whether it is up." : <><Link to={`/applications/${a.id}/health`} className="font-medium underline underline-offset-2">Turn on a health check</Link> to be alerted when it goes down.</>}</>
          ) : (
            <>No process belongs to this app and Caddy does not serve its folder, so its state is unknown. <Link to={`/applications/${a.id}/health`} className="font-medium underline underline-offset-2">Add a health check</Link> to monitor it.</>
          )}
        </Notice>
      )}
      {a.suggestedDomain && (
        <Notice tone="info" icon={<Globe size={16} />}>
          <div className="flex flex-wrap items-center gap-3">
            <span className="min-w-0 flex-1">Caddy {a.ports.length ? "routes" : "serves this app’s folder at"} <b>{a.suggestedDomain}</b>{a.ports.length ? " to this app’s port" : ""}. Use it as the app’s domain?</span>
            <Button size="sm" variant="secondary" onClick={useDomain}>Use domain</Button>
          </div>
        </Notice>
      )}
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <MetricCard icon={<Cpu size={14} />} label="CPU" value={live ? a.cpu.toFixed(1) : "—"} unit={live ? "%" : ""} progress={live ? Math.min(100, a.cpu) : 0} sub="of one core, incl. child processes" />
        <MetricCard icon={<MemoryStick size={14} />} label="RAM" value={live ? fmtMB(a.ram) : "—"} sub={s ? `of ${s.ramGb} GB host` : undefined} />
        <MetricCard icon={<Timer size={14} />} label="Uptime" value={live && a.uptimeSec ? fmtDur(a.uptimeSec) : "—"} sub={`${a.restarts} restarts`} tone={a.restarts > 10 ? "warn" : undefined} />
        <MetricCard to={`/applications/${a.id}/incidents`} icon={<AlertOctagon size={14} />} label="Active incidents" value={active.length} tone={active.length ? "crit" : "ok"} sub="click to review" />
      </div>
      <div className="grid gap-6 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <SectionTitle icon={<ListTree size={15} />} title="Application details" />
          <dl className="grid grid-cols-2 gap-x-6 sm:grid-cols-3">
            <KV label="Server"><Link className="hover:underline" to={`/servers/${a.serverId}`}>{s?.name ?? "—"}</Link></KV>
            <KV label="Status"><AppStatus status={a.status} /></KV>
            <KV label="Manager state" mono>{a.rawStatus || "—"}</KV>
            <KV label="Manager">{a.manager}</KV>
            <KV label={a.manager === "PM2" ? "PM2 ID / name" : "systemd unit"} mono>{proc}</KV>
            <KV label="Process ID" mono>{a.pid ?? "—"}</KV>
            <div className="col-span-2 sm:col-span-3"><KV label="Working directory" mono>{a.path || "—"}</KV></div>
            {(a.execStart || a.script) && <div className="col-span-2 sm:col-span-3"><KV label="Command" mono>{a.execStart || [a.interpreter, a.script].filter(Boolean).join(" ")}</KV></div>}
            <KV label="Runtime">{a.runtime || "—"}</KV>
            <KV label="Runs as" mono>{a.user || "—"}</KV>
            <KV label="Ports" mono>{a.ports.length ? a.ports.map((p) => `${p.port} (${p.bind === "0.0.0.0" || p.bind === "::" ? "public" : "local"})`).join(", ") : "none"}</KV>
            <KV label="Domain">{a.domain ?? "—"}</KV>
            <KV label="Health"><HealthBadge status={a.healthStatus} latency={a.latency} /></KV>
            <KV label="Git branch" mono>{a.git?.branch ?? "—"}</KV>
            <div className="col-span-2"><KV label="Latest commit">{a.git ? <><span className="font-mono">{a.git.commit}</span> — {a.git.message}</> : "—"}</KV></div>
            <KV label="Added">{relTime(a.discoveredAt)} · {a.source === "manual" ? "manually" : `from ${a.source} discovery`}</KV>
          </dl>
          {a.notes && <p className="mt-3 rounded-2xl bg-surface2 px-4 py-3 text-sm text-muted">{a.notes}</p>}
        </Card>
        <Card>
          <SectionTitle icon={<History size={15} />} title="Recent events" />
          {!events.data ? (
            <Spinner />
          ) : events.data.length === 0 ? (
            <p className="text-sm text-muted">No events yet. Status changes show up here.</p>
          ) : (
            <ol className="relative space-y-4 before:absolute before:bottom-1 before:left-[5px] before:top-1 before:w-px before:bg-line">
              {events.data.slice(0, 10).map((e) => (
                <li key={e.id} className="relative pl-6 text-sm">
                  <span className={cn("absolute left-0 top-1.5 h-[11px] w-[11px] rounded-full border-2 border-surface", toneDot[evTone[e.kind] ?? "neutral"])} />
                  <div className="leading-snug">{e.text}</div>
                  <div className="mt-0.5 flex items-center gap-2 text-xs text-muted"><Badge tone={evTone[e.kind] ?? "neutral"} className="!px-2 !py-0.5">{e.kind}</Badge><span title={dateTime(e.ts)}>{relTime(e.ts)}</span></div>
                </li>
              ))}
            </ol>
          )}
        </Card>
      </div>
    </div>
  );
}

export function AppLogs() {
  const a = useApp()!;
  const { servers } = useStore();
  const srv = servers.find((s) => s.id === a.serverId);
  if (a.manager === "standalone")
    return <EmptyState icon={<ScrollText size={20} />} title="No process to read logs from" desc="Standalone apps have no PM2 process or systemd unit. Set a manager in Settings to collect logs." />;
  if (!a.flags.logs || !a.flags.monitoring)
    return <EmptyState icon={<ScrollText size={20} />} title="Log collection is off for this application" desc="Enable it in Settings; history collected so far stays searchable." action={<Link to={`/applications/${a.id}/settings`}><Button>Open settings</Button></Link>} />;
  const offline = srv?.status === "disconnected";
  const sources = a.manager === "systemd" ? `journalctl -u ${a.unit}` : [a.outLog, a.errLog].filter(Boolean).join(" · ") || "PM2 output files (resolved by the agent)";
  return (
    <LogViewer
      key={a.id}
      appId={a.id}
      canStream={!offline}
      note={
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
          <span>Source: <span className="font-mono">{sources}</span></span>
          {offline && <span className="text-warn">The agent is disconnected; it buffers new lines and sends them when it reconnects.</span>}
        </div>
      }
    />
  );
}

interface AppPoint {
  t: number;
  cpu: number;
  ram: number;
  restarts: number;
}
export function AppMetrics() {
  const a = useApp()!;
  const [range, setRange] = useState<Range>("24h");
  const { data, error, loading, reload } = useApi<AppPoint[]>(`/api/apps/${a.id}/metrics?range=${range}`);
  if (!a.flags.metrics) return <EmptyState title="Metrics are disabled for this application" desc="Enable metrics in Settings to start collecting data." action={<Link to={`/applications/${a.id}/settings`}><Button>Open settings</Button></Link>} />;
  const series = (k: keyof Omit<AppPoint, "t">) => (data ?? []).map((p) => ({ t: p.t, v: p[k] }));
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted">Process-tree CPU and memory sampled by the agent every 30 seconds.</p>
        <TimeRangeSelector value={range} onChange={setRange} />
      </div>
      {error ? (
        <ErrorState message={error} onRetry={reload} />
      ) : loading && !data ? (
        <LoadingState label="Loading metrics…" />
      ) : data && data.length === 0 ? (
        <EmptyState title="No data in this range yet" desc="The first data point appears about a minute after the app is added." />
      ) : (
        <div className="grid gap-4 xl:grid-cols-2">
          <MetricPanel title="CPU" data={series("cpu")} unit="%" sub={`Last ${range} · % of one core`} />
          <MetricPanel title="RAM (RSS)" data={series("ram")} unit=" MB" decimals={0} sub={`Last ${range}`} />
          <MetricPanel title="Restarts" data={series("restarts")} unit="" kind="bar" decimals={0} sub={`Restarts per interval · last ${range}`} />
        </div>
      )}
    </div>
  );
}

export function AppCommands() {
  const a = useApp()!;
  return <CommandRunner key={a.id} app={a} />;
}

export function AppIncidents() {
  const a = useApp()!;
  const { incidents } = useStore();
  const list = incidents.filter((i) => i.appId === a.id).sort((x, y) => y.lastAt - x.lastAt);
  if (!list.length) return <EmptyState title="No incidents for this application" desc="Nothing has been detected. Downtime, crash loops, failing health checks and repeated errors will appear here." />;
  return (
    <div className="space-y-4">
      {list.map((i) => (
        <Card key={i.id}>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3 className="text-lg font-semibold">{i.title}</h3>
              <div className="mt-1.5 flex flex-wrap items-center gap-2">
                <SeverityBadge severity={i.severity} />
                <IncidentStatusBadge status={i.status} />
                <Badge>{i.type}</Badge>
              </div>
            </div>
            <IncidentActions incident={i} />
          </div>
          <dl className="mt-4 grid grid-cols-2 gap-x-6 sm:grid-cols-4">
            <KV label="Occurrences">{i.occurrences.toLocaleString()}</KV>
            <KV label="Started">{clock(i.firstAt, false)} <span className="text-muted">({relTime(i.firstAt)})</span></KV>
            <KV label="Last">{clock(i.lastAt, false)} <span className="text-muted">({relTime(i.lastAt)})</span></KV>
            <KV label="Status"><span className="capitalize">{i.status}</span></KV>
          </dl>
          {i.resolution && <p className="mt-3 text-sm text-muted"><b className="text-fg">Resolution:</b> {i.resolution}</p>}
        </Card>
      ))}
    </div>
  );
}

interface HealthResultRow {
  ts: number;
  ok: boolean;
  status: number;
  latencyMs: number;
  error?: string;
}

function HealthEdit({ a, open, onClose }: { a: App; open: boolean; onClose: () => void }) {
  const { upsertApp, toast } = useStore();
  const port = a.ports.find((p) => p.proto === "TCP")?.port;
  const site = a.domain ?? a.suggestedDomain;
  const [h, setH] = useState({ ...a.health, url: a.health.url || (port ? `http://127.0.0.1:${port}/health` : site ? `https://${site}/` : "") });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const save = async () => {
    setBusy(true);
    setErr("");
    try {
      upsertApp(await api<App>("PUT", `/api/apps/${a.id}/health`, h));
      toast("Health check saved. The agent starts it right away.");
      onClose();
    } catch (e) {
      setErr((e as ApiError).message);
    }
    setBusy(false);
  };
  return (
    <Modal open={open} onClose={onClose} title="Health check" footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={save} disabled={busy}>{busy && <Spinner size={14} />} Save</Button></>}>
      <div className="space-y-4">
        <div className="flex items-center justify-between"><span className="text-sm font-medium">Enabled</span><Toggle checked={h.enabled} onChange={(v) => setH({ ...h, enabled: v })} /></div>
        <Field label="URL" hint="Requested by the agent on the server, so 127.0.0.1 reaches the app directly"><Input value={h.url} onChange={(e) => setH({ ...h, url: e.target.value })} placeholder="http://127.0.0.1:5002/health" className="font-mono" /></Field>
        <div className="grid grid-cols-3 gap-3">
          <Field label="Interval (s)"><Input type="number" min={10} max={3600} value={h.intervalSec} onChange={(e) => setH({ ...h, intervalSec: Number(e.target.value) })} /></Field>
          <Field label="Timeout (ms)"><Input type="number" min={500} max={60000} value={h.timeoutMs} onChange={(e) => setH({ ...h, timeoutMs: Number(e.target.value) })} /></Field>
          <Field label="Expected status"><Input type="number" min={100} max={599} value={h.expectedStatus} onChange={(e) => setH({ ...h, expectedStatus: Number(e.target.value) })} /></Field>
        </div>
        {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>}
      </div>
    </Modal>
  );
}

export function AppHealth() {
  const a = useApp()!;
  const { toast } = useStore();
  const [edit, setEdit] = useState(false);
  const [checking, setChecking] = useState(false);
  const history = useApi<HealthResultRow[]>(`/api/apps/${a.id}/health/history?limit=120&v=${a.healthLastOk}-${a.healthLastFail}`);
  const checkNow = async () => {
    setChecking(true);
    try {
      await api("POST", `/api/apps/${a.id}/health/check`);
      toast("Check requested", "info");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setTimeout(() => setChecking(false), 1500);
  };
  if (!a.health.enabled)
    return (
      <>
        <EmptyState icon={<Activity size={20} />} title="No health check configured" desc="Add an HTTP endpoint the agent requests on a schedule to catch failures before users do." action={<Button onClick={() => setEdit(true)}>Configure health check</Button>} />
        <HealthEdit key={String(edit)} a={a} open={edit} onClose={() => setEdit(false)} />
      </>
    );
  const hist = history.data ?? [];
  const last = hist[0];
  return (
    <div className="space-y-6">
      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <SectionTitle icon={<SlidersHorizontal size={15} />} title="Configuration" action={<div className="flex gap-2"><Button size="sm" variant="secondary" onClick={() => setEdit(true)}><Pencil size={13} /> Edit</Button><Button size="sm" onClick={checkNow} disabled={checking}>{checking ? <Spinner size={13} /> : <FlaskConical size={13} />} Check now</Button></div>} />
          <dl className="grid grid-cols-2 gap-x-6">
            <div className="col-span-2"><KV label="URL" mono>{a.health.url}</KV></div>
            <KV label="Interval">{a.health.intervalSec}s</KV>
            <KV label="Timeout">{a.health.timeoutMs} ms</KV>
            <KV label="Expected status" mono>{a.health.expectedStatus}</KV>
          </dl>
        </Card>
        <Card>
          <SectionTitle icon={<HeartPulse size={15} />} title="Current state" action={<HealthBadge status={a.healthStatus} latency={a.latency} />} />
          <dl className="grid grid-cols-2 gap-x-6">
            <KV label="Latest result">{last ? (last.ok ? `HTTP ${last.status} OK` : last.error || `HTTP ${last.status}`) : "waiting for the first check"}</KV>
            <KV label="Latency">{last ? `${last.latencyMs} ms` : "—"}</KV>
            <KV label="Last success">{a.healthLastOk ? relTime(a.healthLastOk) : "never"}</KV>
            <KV label="Last failure">{a.healthLastFail ? relTime(a.healthLastFail) : "never"}</KV>
            <KV label="Consecutive failures"><span className={a.healthFailures ? "font-semibold text-crit" : ""}>{a.healthFailures}</span></KV>
            <KV label="Checked">{last ? dateTime(last.ts) : "—"}</KV>
          </dl>
        </Card>
      </div>
      <Card>
        <SectionTitle icon={<BarChart3 size={15} />} title="Check history" sub="Newest on the right · bar height is latency · red = failed" />
        {hist.length === 0 ? (
          <p className="text-sm text-muted">No results yet.</p>
        ) : (
          <div className="flex h-12 items-end gap-[3px] overflow-hidden">
            {[...hist].slice(0, 90).reverse().map((h) => (
              <div key={h.ts} title={`${clock(h.ts)} · ${h.ok ? "OK" : "FAILED"} · ${h.latencyMs} ms${h.error ? " · " + h.error : ""}`} className={cn("min-w-[3px] flex-1 rounded-sm", h.ok ? "bg-ok" : "bg-crit")} style={{ height: `${Math.min(100, 20 + (h.latencyMs / Math.max(1, a.health.timeoutMs)) * 160)}%` }} />
            ))}
          </div>
        )}
      </Card>
      <Card pad={false}>
        <div className="p-5 pb-0"><SectionTitle icon={<ListChecks size={15} />} title="Recent results" /></div>
        <Table head={["Time", "Result", "HTTP", "Latency", "Detail"]}>
          {hist.slice(0, 20).map((h) => (
            <Tr key={h.ts}>
              <Td className="font-mono text-xs">{dateTime(h.ts)}</Td>
              <Td><Badge tone={h.ok ? "ok" : "crit"}>{h.ok ? "Passed" : "Failed"}</Badge></Td>
              <Td className="font-mono text-xs">{h.status || "—"}</Td>
              <Td className="tabular-nums">{h.latencyMs} ms</Td>
              <Td className="text-muted">{h.error || "—"}</Td>
            </Tr>
          ))}
        </Table>
      </Card>
      <HealthEdit key={String(edit)} a={a} open={edit} onClose={() => setEdit(false)} />
    </div>
  );
}

interface StorageScan {
  totalBytes: number;
  files: number;
  items: { name: string; bytes: number; dir: boolean }[];
  scannedAt: number;
  durationMs: number;
  truncated: boolean;
  error?: string;
}

export function AppStorage() {
  const a = useApp()!;
  const { toast, subscribeLive } = useStore();
  const { data, reload } = useApi<{ scan: StorageScan | null }>(`/api/apps/${a.id}/storage`);
  const [busy, setBusy] = useState(false);
  useEffect(
    () =>
      subscribeLive((m) => {
        if (m.type === "storage" && m.data.appId === a.id) {
          setBusy(false);
          reload();
        }
      }),
    [a.id, subscribeLive, reload],
  );
  const rescan = async () => {
    setBusy(true);
    try {
      await api("POST", `/api/apps/${a.id}/storage/scan`);
    } catch (e) {
      toast((e as ApiError).message, "err");
      setBusy(false);
    }
  };
  const scan = data?.scan;
  const mb = (b: number) => b / (1 << 20);
  return (
    <div className="grid gap-6 lg:grid-cols-3">
      <Card>
        <SectionTitle icon={<HardDrive size={15} />} title="Folder size" />
        <div className="text-4xl font-semibold tracking-tight tabular-nums">{scan ? fmtMB(mb(scan.totalBytes)) : "—"}</div>
        <p className="mt-1 break-all font-mono text-xs text-muted">{a.path || "no working directory"}</p>
        {scan && (
          <p className="mt-4 text-xs text-muted">
            {scan.files.toLocaleString()} files · scanned {relTime(scan.scannedAt)} in {(scan.durationMs / 1000).toFixed(1)}s{scan.truncated && " · stopped early (very large folder)"}
          </p>
        )}
        <p className="mt-1 text-xs text-muted">Rescanned automatically every 15 minutes (less often for very large folders); scans are throttled to keep disk IO low.</p>
        <Button className="mt-4" variant="secondary" size="sm" onClick={rescan} disabled={busy || !a.path}>
          {busy ? <Spinner size={13} /> : <RefreshCw size={13} />} {busy ? "Scanning…" : "Rescan now"}
        </Button>
      </Card>
      <Card className="lg:col-span-2">
        <SectionTitle icon={<FolderTree size={15} />} title="Breakdown" sub="Largest top-level entries" />
        {!data ? (
          <Spinner />
        ) : !scan ? (
          <p className="text-sm text-muted">{a.path ? "The first scan runs shortly after the app is added." : "Set a working directory in Settings to measure this app."}</p>
        ) : scan.error ? (
          <p className="text-sm text-crit">{scan.error}</p>
        ) : (
          <StorageBreakdown items={scan.items.map((i) => ({ name: i.name + (i.dir ? "/" : ""), mb: mb(i.bytes) }))} total={mb(scan.totalBytes)} />
        )}
      </Card>
    </div>
  );
}

export function AppGit() {
  const a = useApp()!;
  return (
    <Card>
      <SectionTitle icon={<GitBranch size={15} />} title="Git repository" sub="Read-only inspection, refreshed every 2 minutes. Credentials in remote URLs are removed by the agent." />
      <GitSummary app={a} full />
    </Card>
  );
}

const FLAG_ROWS: [keyof AppFlags, string, string][] = [
  ["monitoring", "Monitoring enabled", "Master switch for this application"],
  ["logs", "Log collection", "Stream and store stdout / stderr"],
  ["metrics", "Metrics enabled", "CPU, RAM and restart history"],
  ["incidents", "Incident detection", "Create incidents from rules"],
  ["notifications", "Notifications", "Send alerts to configured providers"],
];

export function AppSettings() {
  const a = useApp()!;
  const { upsertApp, removeApp, toast, servers } = useStore();
  const nav = useNavigate();
  const manager = a.manager === "PM2" ? "pm2" : a.manager === "systemd" ? "systemd" : "standalone";
  const init = { name: a.name, path: a.path, domain: a.domain ?? "", manager, processKey: a.manager === "standalone" ? "" : a.processKey, notes: a.notes, flags: a.flags };
  const [f, setF] = useState(init);
  const [del, setDel] = useState(false);
  const [busy, setBusy] = useState(false);
  const dirty = JSON.stringify(f) !== JSON.stringify(init);
  const set = <K extends keyof typeof f>(k: K, v: (typeof f)[K]) => setF((s) => ({ ...s, [k]: v }));
  const save = async () => {
    setBusy(true);
    try {
      upsertApp(await api<App>("PATCH", `/api/apps/${a.id}`, f));
      toast("Application settings saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const remove = async () => {
    setDel(false);
    try {
      await api("DELETE", `/api/apps/${a.id}`);
      removeApp(a.id);
      toast(`${a.name} removed from monitoring`);
      nav("/applications");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <Card>
        <SectionTitle icon={<Settings size={15} />} title="General" sub={`Hosted on ${servers.find((s) => s.id === a.serverId)?.name ?? "—"}`} />
        <div className="space-y-4">
          <Field label="Display name"><Input value={f.name} maxLength={80} onChange={(e) => set("name", e.target.value)} /></Field>
          <Field label="Working directory"><Input value={f.path} onChange={(e) => set("path", e.target.value)} className="font-mono" placeholder="detected from the process" /></Field>
          <Field label="Domain"><Input value={f.domain} onChange={(e) => set("domain", e.target.value)} placeholder="none" /></Field>
          <Field label="Process manager">
            <Select value={f.manager} onChange={(e) => set("manager", e.target.value)}>
              <option value="pm2">PM2</option>
              <option value="systemd">systemd</option>
              <option value="standalone">Standalone (folder only)</option>
            </Select>
          </Field>
          {f.manager !== "standalone" && (
            <Field label={f.manager === "pm2" ? "PM2 process name" : "systemd unit"}><Input value={f.processKey} onChange={(e) => set("processKey", e.target.value)} className="font-mono" /></Field>
          )}
          <Field label="Notes"><Textarea rows={3} value={f.notes} onChange={(e) => set("notes", e.target.value)} /></Field>
        </div>
      </Card>
      <div className="space-y-6">
        <Card>
          <SectionTitle icon={<Activity size={15} />} title="Monitoring" />
          <div className="divide-y divide-line">
            {FLAG_ROWS.map(([k, l, d]) => (
              <div key={k} className="flex items-center justify-between gap-3 py-3">
                <div><div className="text-sm font-medium">{l}</div><div className="text-xs text-muted">{d}</div></div>
                <Toggle checked={f.flags[k]} onChange={(v) => set("flags", { ...f.flags, [k]: v })} />
              </div>
            ))}
          </div>
          <div className="mt-4 flex gap-2">
            <Button disabled={!dirty || busy} onClick={save}>{busy && <Spinner size={14} />} Save changes</Button>
            <Button variant="ghost" disabled={!dirty} onClick={() => setF(init)}>Reset</Button>
          </div>
        </Card>
        <Card className="border-crit/30">
          <SectionTitle icon={<Trash2 size={15} />} title="Danger zone" />
          <p className="mb-4 text-sm text-muted">Removing the monitoring entry only stops Fleetwatch from tracking it. The process, files and logs on the server are not touched.</p>
          <Button variant="danger" onClick={() => setDel(true)}><Trash2 size={14} /> Delete monitoring entry</Button>
        </Card>
      </div>
      <ConfirmDialog open={del} danger title={`Delete “${a.name}” from monitoring?`} confirmLabel="Delete entry" message="Its metrics history and events are removed from the dashboard. If the process still runs, it reappears under Discoveries." onCancel={() => setDel(false)} onConfirm={remove} />
    </div>
  );
}

/** The app's .env files on its server: owner only, unlocked with the password. */
export function AppEnv() {
  const a = useApp()!;
  const { me } = useStore();
  if (me && me.role !== "owner") return <EmptyState icon={<Lock size={20} />} title="Only the owner can open .env files" desc="They hold the app's secrets." />;
  if (!a.path) return <EmptyState icon={<FolderX size={20} />} title="No folder on the server" desc="This app has no working directory, so it has no .env file." />;
  return (
    <FileEditor base={`/api/apps/${a.id}/env`} dir={a.path} known={[".env"]} newName={{ pattern: /^\.env(\.[A-Za-z0-9_-]{1,40})?$/, placeholder: ".env.production", hint: "Names look like .env or .env.production" }}
      icon={<KeyRound size={18} />} title="Environment files"
      lockedTitle="This app's .env files hold its secrets" lockedText="Enter your password to open them." placeholder="KEY=value"
      saveHint="Saving keeps the previous version as {name}.bak. Restart the app so it reads the change." savedToast="{name} saved. Restart the app so it reads the change."
      actions={a.manager !== "standalone" && <Link to={`/applications/${a.id}/commands?run=builtin:restart`}><Button size="sm" variant="secondary"><RefreshCw size={13} /> Restart app</Button></Link>} />
  );
}

/** Web server files kept in the app's folder: its Caddyfile or nginx.conf. */
export function AppWeb() {
  const a = useApp()!;
  const { me } = useStore();
  if (me && me.role !== "owner") return <EmptyState icon={<Lock size={20} />} title="Only the owner can open these files" />;
  if (!a.path) return <EmptyState icon={<FolderX size={20} />} title="No folder on the server" desc="This app has no working directory, so it has no web server files." />;
  return (
    <FileEditor base={`/api/apps/${a.id}/env`} dir={a.path} known={["Caddyfile", "nginx.conf"]} icon={<FileCode2 size={18} />} title="Web server files of this app"
      lockedTitle="The app's own Caddyfile and nginx.conf" lockedText="Enter your password to open the web server files in the app's folder." placeholder={CADDY_EXAMPLE}
      saveHint="Saving keeps the previous version as {name}.bak. The web server uses it only if the server's own configuration includes it."
      savedToast="{name} saved" />
  );
}
