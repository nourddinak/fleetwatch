import { useState } from "react";
import { Link, Outlet, useNavigate, useParams } from "react-router-dom";
import { Activity, AlertOctagon, Bot, Boxes, Cloud, Container, Cpu, EthernetPort, FolderTree, Gpu, HardDrive, HeartPulse, History, KeyRound, LayoutGrid, ListChecks, MemoryStick, Monitor, Network, Plug, Plus, PowerOff, RefreshCw, Rocket, Server as ServerIcon, Settings, Shield, Timer, Trash2, Unplug } from "lucide-react";
import { serverCounts, useStore } from "../store/store";
import { api, toSeries, useApi, type ApiError, type EnrollInfo, type MetricKey, type MetricPoint, type ServerEvent } from "../api/client";
import type { Range, Server } from "../mock/types";
import { fmtDur, fmtMB, relTime, dateTime, clock } from "../mock/util";
import { Badge, Bar, Button, Card, ConfirmDialog, CopyButton, EmptyState, ErrorState, Field, Gate, Input, KV, LinkTabs, LoadingState, MetricCard, Modal, PageHeader, SectionTitle, Select, ServerStatus, Spinner, Textarea, TimeRangeSelector, levelTone } from "../components/ui";
import { AppsTable } from "../components/AppsTable";
import { IncidentCard, IncidentDrawer, PortTable, type PortRow } from "../components/domain";
import { MetricPanel } from "../components/MetricChart";
import { InstallCommand } from "../components/InstallCommand";
import { cn } from "../utils/cn";
import { MaintenanceControl } from "../components/Maintenance";
import { DiskRescue, fullIn, type Forecasts } from "../components/DiskRescue";

function useServer() {
  const { id } = useParams();
  const { servers } = useStore();
  return servers.find((s) => s.id === id);
}

const seen = (s: Server) => (s.lastSeen ? relTime(s.lastSeen) : "never");

export function ServerDetail() {
  const s = useServer();
  const { apps, incidents, openAddApp, serversLoaded } = useStore();
  if (!s)
    return serversLoaded ? (
      <EmptyState title="Server not found" desc="This server may have been removed." action={<Link to="/servers"><Button>Back to servers</Button></Link>} />
    ) : (
      <LoadingState label="Loading server…" />
    );
  const c = serverCounts(s.id, apps, incidents);
  const base = `/servers/${s.id}`;
  const agentState = s.status === "pending" ? "waiting for first connection" : s.status === "disconnected" ? "disconnected" : "connected";
  return (
    <>
      <PageHeader
        back={{ to: "/servers", label: "All servers" }}
        title={
          <span className="flex items-center gap-3">
            <span className="relative grid h-10 w-10 shrink-0 place-items-center rounded-2xl border border-line bg-surface2 text-fg">
              <ServerIcon size={18} />
              <span className={cn("absolute -right-0.5 -top-0.5 h-3 w-3 rounded-full border-2 border-bg",
                { online: "bg-ok", warning: "bg-warn", critical: "bg-crit", disconnected: "bg-crit", pending: "bg-muted" }[s.status], s.status === "disconnected" && "pulse-dot")} />
            </span>
            <span className="min-w-0 break-words">{s.name}</span>
          </span>
        }
        badge={<ServerStatus status={s.status} />}
        sub={
          <span className="flex flex-wrap gap-x-4 gap-y-1">
            {s.hostname && <span className="inline-flex items-center gap-1.5"><ServerIcon size={13} />{s.hostname}</span>}
            {s.ip && <span className="inline-flex items-center gap-1.5 font-mono"><Network size={13} />{s.ip}</span>}
            {s.os && <span className="inline-flex items-center gap-1.5"><Monitor size={13} />{s.os}</span>}
            {s.provider && <span className="inline-flex items-center gap-1.5"><Cloud size={13} />{s.provider}</span>}
            <span className={cn("inline-flex items-center gap-1.5", s.status === "disconnected" && "text-crit")}>{s.status === "disconnected" ? <Unplug size={13} /> : <Bot size={13} />}Agent {agentState}{s.agentVersion && ` · v${s.agentVersion}`}</span>
            {s.status !== "pending" && <span className="inline-flex items-center gap-1.5"><Timer size={13} />Up {s.status === "disconnected" ? "unknown" : fmtDur(s.uptimeSec)}</span>}
          </span>
        }
        actions={
          <>
            {s.status !== "pending" && <MaintenanceControl kind="server" id={s.id} />}
            <Button size="sm" onClick={() => openAddApp(s.id)}><Plus size={14} /> Add application</Button>
          </>
        }
      />
      <LinkTabs
        tabs={[
          { to: base, label: "Overview", end: true, icon: <LayoutGrid size={14} /> },
          { to: `${base}/applications`, label: "Applications", icon: <Boxes size={14} />, badge: <Badge>{c.apps}</Badge> },
          { to: `${base}/metrics`, label: "Metrics", icon: <Activity size={14} /> },
          { to: `${base}/ports`, label: "Ports", icon: <EthernetPort size={14} /> },
          { to: `${base}/firewall`, label: "Firewall", icon: <Shield size={14} /> },
          { to: `${base}/storage`, label: "Storage", icon: <HardDrive size={14} /> },
          { to: `${base}/sites`, label: "Sites", icon: <Rocket size={14} /> },
          ...(s.capabilities.includes("Docker") ? [{ to: `${base}/docker`, label: "Docker", icon: <Container size={14} /> }] : []),
          { to: `${base}/incidents`, label: "Incidents", icon: <AlertOctagon size={14} />, badge: c.incidents ? <Badge tone="crit">{c.incidents}</Badge> : undefined },
          { to: `${base}/agent`, label: "Agent", icon: <Bot size={14} /> },
          { to: `${base}/settings`, label: "Settings", icon: <Settings size={14} /> },
        ]}
      />
      <Gate>
        <Outlet />
      </Gate>
    </>
  );
}

export function ServerOverview() {
  const s = useServer()!;
  const { apps, incidents } = useStore();
  const events = useApi<ServerEvent[]>(`/api/servers/${s.id}/events`);
  const fc = useApi<Forecasts>("/api/forecasts").data?.servers?.[s.id];
  const c = serverCounts(s.id, apps, incidents);
  const dc = s.status === "disconnected";
  if (s.status === "pending")
    return (
      <EmptyState icon={<Plug size={20} />} title="Waiting for the agent" desc="This server appears here once its agent connects. Get a fresh install command from the Agent tab." action={<Link to={`/servers/${s.id}/agent`}><Button>Open agent setup</Button></Link>} />
    );
  return (
    <div className="space-y-6">
      {dc && (
        <div className="flex items-start gap-3 rounded-2xl bg-crit-soft px-4 py-3 text-sm text-crit">
          <Unplug size={16} className="mt-0.5 shrink-0" />
          <span><b>Agent disconnected.</b> No heartbeat since {seen(s)}. Values below are the last known state.</span>
        </div>
      )}
      <div className={cn("grid grid-cols-2 gap-3 md:grid-cols-4", dc && "opacity-60")}>
        <MetricCard label="CPU" value={s.cpu.toFixed(1)} unit="%" progress={s.cpu} icon={<Cpu size={14} />} tone={levelTone(s.cpu) === "ok" ? undefined : levelTone(s.cpu)} sub={`${s.cpuCores} cores · ${s.arch}`} />
        <MetricCard label="RAM" value={s.ram.toFixed(1)} unit="%" progress={s.ram} icon={<MemoryStick size={14} />} sub={`${((s.ramGb * s.ram) / 100).toFixed(1)} / ${s.ramGb} GB · swap ${s.swap.toFixed(0)}% of ${s.swapGb} GB`} />
        <MetricCard label="Disk" value={s.disk.toFixed(1)} unit="%" progress={s.disk} icon={<HardDrive size={14} />} tone={levelTone(s.disk, 80, 90) === "ok" ? undefined : levelTone(s.disk, 80, 90)} sub={fc ? `${Math.round((s.diskGb * s.disk) / 100)} / ${s.diskGb} GB · full in ~${fullIn(fc.fullDays)}` : `${Math.round((s.diskGb * s.disk) / 100)} / ${s.diskGb} GB`} />
        <MetricCard label="Load average" value={s.load[0].toFixed(2)} sub={`5m ${s.load[1]} · 15m ${s.load[2]}`} icon={<Activity size={14} />} />
        <MetricCard label="Network" value={`↓${s.netRx.toFixed(1)}`} unit="Mbps" sub={`↑ ${s.netTx.toFixed(1)} Mbps`} icon={<Network size={14} />} />
        <MetricCard label="Uptime" value={dc ? "—" : fmtDur(s.uptimeSec)} icon={<Timer size={14} />} sub={dc ? "unknown while disconnected" : `booted ${dateTime(Date.now() - s.uptimeSec * 1000)}`} />
        <MetricCard to={`/servers/${s.id}/agent`} label="Agent health" value={dc ? "Offline" : `${s.latencyMs}ms`} tone={dc ? "crit" : s.latencyMs > 200 ? "warn" : "ok"} icon={<Plug size={14} />} sub={`heartbeat ${seen(s)}`} />
        <MetricCard to={`/servers/${s.id}/applications`} label="Applications" value={c.apps} icon={<Boxes size={14} />} sub={`${c.incidents} incidents · ${c.warnings} warnings`} tone={c.incidents ? "warn" : undefined} />
        {(s.gpus ?? []).map((g, i) => (
          <MetricCard key={i} to={`/servers/${s.id}/metrics`} label={(s.gpus!.length > 1 ? `GPU ${i} · ` : "GPU · ") + g.name} value={g.util.toFixed(0)} unit="%" progress={g.util} icon={<Gpu size={14} />}
            tone={levelTone(g.memTotalGb ? (g.memUsedGb / g.memTotalGb) * 100 : 0, 85, 95) === "ok" ? undefined : "warn"}
            sub={`${g.memUsedGb} / ${g.memTotalGb} GB memory · ${g.tempC.toFixed(0)}°C${g.powerW ? ` · ${g.powerW.toFixed(0)} W` : ""}`} />
        ))}
      </div>
      <Card>
        <SectionTitle icon={<History size={15} />} title="Recent server events" sub="Agent connections, reboots and changes" />
        {events.error ? (
          <p className="text-sm text-crit">{events.error}</p>
        ) : !events.data ? (
          <Spinner />
        ) : events.data.length === 0 ? (
          <p className="text-sm text-muted">No events yet.</p>
        ) : (
          <ol className="relative space-y-3 before:absolute before:bottom-1 before:left-[5px] before:top-1 before:w-px before:bg-line">
            {events.data.map((e) => (
              <li key={e.id} className="relative flex items-start gap-3 pl-6 text-sm">
                <span className={cn("absolute left-0 top-1.5 h-[11px] w-[11px] rounded-full border-2 border-surface", e.kind === "reboot" ? "bg-warn" : e.text.includes("disconnected") ? "bg-crit" : e.kind === "agent" ? "bg-ok" : "bg-muted")} />
                <span className="min-w-0 flex-1">{e.text}</span>
                <span className="shrink-0 text-xs text-muted" title={dateTime(e.ts)}>{relTime(e.ts)}</span>
              </li>
            ))}
          </ol>
        )}
      </Card>
    </div>
  );
}

export function ServerApplications() {
  const s = useServer()!;
  const { apps, openAddApp } = useStore();
  const list = apps.filter((a) => a.serverId === s.id);
  return (
    <Card pad={false}>
      <div className="p-5 pb-2"><SectionTitle icon={<Boxes size={15} />} title={`Applications on ${s.name}`} sub={`${list.length} discovered or manually added`} action={<Button size="sm" variant="secondary" onClick={() => openAddApp(s.id)}><Plus size={13} /> Add</Button>} /></div>
      {list.length ? <AppsTable apps={list} showServer={false} /> : <div className="p-5"><EmptyState title="No applications on this server" desc="Discovered PM2, systemd and static sites appear under Discoveries. You can also add one manually." /></div>}
    </Card>
  );
}

const SERVER_PANELS: { m: MetricKey; title: string; unit: string; max?: number; thr?: number }[] = [
  { m: "cpu", title: "CPU usage", unit: "%", max: 100, thr: 90 },
  { m: "ram", title: "RAM usage", unit: "%", max: 100, thr: 90 },
  { m: "disk", title: "Disk usage", unit: "%", max: 100, thr: 85 },
  { m: "swap", title: "Swap usage", unit: "%", max: 100 },
  { m: "netRx", title: "Network receive", unit: " Mbps" },
  { m: "netTx", title: "Network transmit", unit: " Mbps" },
  { m: "load", title: "Load average (1m)", unit: "" },
];
const GPU_PANELS: typeof SERVER_PANELS = [
  { m: "gpu", title: "GPU usage", unit: "%", max: 100 },
  { m: "gpuMem", title: "GPU memory", unit: "%", max: 100, thr: 95 },
];
export function ServerMetrics() {
  const s = useServer()!;
  const [range, setRange] = useState<Range>("24h");
  const { data, error, loading, reload } = useApi<MetricPoint[]>(`/api/metrics?server=${s.id}&range=${range}`);
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted">Stored as 1-minute averages, rolled up to 5-minute and hourly buckets for longer ranges.</p>
        <TimeRangeSelector value={range} onChange={setRange} />
      </div>
      {s.status === "disconnected" && <div className="rounded-2xl bg-warn-soft px-4 py-3 text-sm text-warn">Metrics stop at the last heartbeat ({seen(s)}) until the agent reconnects.</div>}
      {error ? (
        <ErrorState message={error} onRetry={reload} />
      ) : loading && !data ? (
        <LoadingState label="Loading metrics…" />
      ) : data && data.length === 0 ? (
        <EmptyState title="No data in this range yet" desc="The first minute of data appears shortly after the agent connects." />
      ) : (
        <div className="grid gap-4 xl:grid-cols-2">
          {[...SERVER_PANELS, ...(s.gpus?.length ? GPU_PANELS : [])].map((p) => (
            <MetricPanel key={p.m} title={p.title} data={toSeries(data, p.m)} unit={p.unit} max={p.max} threshold={p.thr} sub={`Last ${range}`} />
          ))}
        </div>
      )}
    </div>
  );
}

interface PortApiRow {
  proto: string;
  bind: string;
  port: number;
  pid: number;
  process: string;
  appId?: string;
  appName?: string;
}
export function ServerPorts() {
  const s = useServer()!;
  const { apps } = useStore();
  const { data, error, reload } = useApi<PortApiRow[]>(`/api/servers/${s.id}/ports`);
  if (error) return <ErrorState message={error} onRetry={reload} />;
  if (!data) return <LoadingState label="Loading ports…" />;
  const rows: PortRow[] = data.map((r) => ({ app: apps.find((a) => a.id === r.appId) ?? null, name: r.appName ?? (r.process || "unknown process"), pid: r.pid || null, proto: r.proto, bind: r.bind, port: r.port }));
  const pub = rows.filter((r) => r.bind === "0.0.0.0" || r.bind === "::").length;
  const unexpected = rows.filter((r) => (r.bind === "0.0.0.0" || r.bind === "::") && r.app).length;
  return (
    <Card pad={false}>
      <div className="p-5 pb-2">
        <SectionTitle icon={<EthernetPort size={15} />} title="Listening ports" sub={`${rows.length} sockets from the latest agent scan · ${pub} publicly exposed`} action={<Button size="sm" variant="ghost" onClick={reload}>Refresh</Button>} />
        {unexpected > 0 && <p className="text-sm text-warn">{unexpected} application socket{unexpected === 1 ? " listens" : "s listen"} on all interfaces. If the app sits behind Caddy, bind it to 127.0.0.1 instead. Fleetwatch never changes bind or firewall settings.</p>}
      </div>
      {rows.length ? <PortTable rows={rows} /> : <div className="p-5"><EmptyState title="No listening sockets reported" desc="Ports appear after the agent's first scan." /></div>}
    </Card>
  );
}

export function ServerStorage() {
  const s = useServer()!;
  const { apps } = useStore();
  const list = apps.filter((a) => a.serverId === s.id).sort((a, b) => b.folderMB - a.folderMB);
  const usedMB = ((s.diskGb * s.disk) / 100) * 1024;
  const appsMB = list.reduce((n, a) => n + a.folderMB, 0);
  const max = list[0]?.folderMB ?? 1;
  const fc = useApi<Forecasts>("/api/forecasts").data?.servers?.[s.id];
  return (
    <div className="grid gap-6 lg:grid-cols-5">
      <DiskRescue serverId={s.id} disk={s.disk} />
      <Card className="lg:col-span-2">
        <SectionTitle icon={<HardDrive size={15} />} title="Disk usage" sub="Root filesystem /" />
        <div className="text-4xl font-semibold tracking-tight tabular-nums">{s.disk.toFixed(1)}<span className="text-lg text-muted">%</span></div>
        <Bar value={s.disk} className="mt-3 !h-3" />
        <div className="mt-2 flex justify-between text-xs text-muted"><span>{fmtMB(usedMB)} used</span><span>{s.diskGb} GB total</span></div>
        {s.disk > 85 && <div className="mt-4 rounded-2xl bg-crit-soft px-4 py-3 text-sm text-crit">Disk is above the 85% threshold. See below what can be freed, or attach a volume.</div>}
        {fc && <div className="mt-4 rounded-2xl bg-warn-soft px-4 py-3 text-sm text-warn">At the pace of the last days (+{fc.perDay.toFixed(1)} points a day) this disk is full in about {fullIn(fc.fullDays)}.</div>}
        <dl className="mt-4 divide-y divide-line text-sm">
          <div className="flex justify-between py-2"><dt className="text-muted">Monitored app folders</dt><dd className="font-medium">{fmtMB(appsMB)}</dd></div>
          <div className="flex justify-between py-2"><dt className="text-muted">Everything else</dt><dd className="font-medium">{fmtMB(Math.max(0, usedMB - appsMB))}</dd></div>
          <div className="flex justify-between py-2"><dt className="text-muted">Free</dt><dd className="font-medium">{fmtMB(Math.max(0, s.diskGb * 1024 - usedMB))}</dd></div>
        </dl>
      </Card>
      <Card className="lg:col-span-3">
        <SectionTitle icon={<FolderTree size={15} />} title="Application folder ranking" />
        {list.length === 0 ? (
          <p className="text-sm text-muted">Folder sizes are measured once applications are discovered.</p>
        ) : (
          <ul className="space-y-4">
            {list.map((a) => (
              <li key={a.id}>
                <div className="mb-1.5 flex justify-between gap-3 text-sm">
                  <Link to={`/applications/${a.id}/storage`} className="min-w-0 truncate font-medium hover:underline">{a.name}</Link>
                  <span className="shrink-0 tabular-nums text-muted">{fmtMB(a.folderMB)}</span>
                </div>
                <Bar value={(a.folderMB / max) * 100} tone="accent" />
                <div className="mt-1 font-mono text-[11px] text-muted">{a.path}</div>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

export function ServerIncidents() {
  const s = useServer()!;
  const { incidents } = useStore();
  const [open, setOpen] = useState<string | null>(null);
  const list = incidents.filter((i) => i.serverId === s.id).sort((a, b) => b.lastAt - a.lastAt);
  return (
    <div className="space-y-3">
      {list.length === 0 ? <EmptyState title="No incidents on this server" desc="Healthy state — nothing has been detected." /> : list.map((i) => <IncidentCard key={i.id} incident={i} onOpen={() => setOpen(i.id)} />)}
      <IncidentDrawer incidentId={open} onClose={() => setOpen(null)} />
    </div>
  );
}

export function ServerAgent() {
  const s = useServer()!;
  const { toast } = useStore();
  const [dlg, setDlg] = useState<"restart" | "repair" | "revoke" | "update" | null>(null);
  const [busy, setBusy] = useState(false);
  const [enroll, setEnroll] = useState<EnrollInfo | null>(null);
  const dc = s.status === "disconnected";
  const pending = s.status === "pending";
  const outdated = !!s.agentVersion && s.agentVersion !== s.latestAgentVersion;

  const act = async (fn: () => Promise<void>) => {
    setDlg(null);
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const restart = () => act(async () => { await api("POST", `/api/servers/${s.id}/restart-agent`); toast("Restart requested. The agent reconnects in a few seconds.", "info"); });
  const updateAgent = () => act(async () => { await api("POST", `/api/servers/${s.id}/update-agent`); toast(`Update to v${s.latestAgentVersion} sent. The agent restarts on the new version in a few seconds.`, "info"); });
  const repair = () => act(async () => setEnroll(await api<EnrollInfo>("POST", `/api/servers/${s.id}/enroll-token`)));
  const revoke = () => act(async () => { await api("POST", `/api/servers/${s.id}/revoke`); toast(`Agent credential for ${s.name} revoked`); });

  return (
    <div className="grid gap-6 lg:grid-cols-3">
      <Card className="lg:col-span-2">
        <SectionTitle icon={<Bot size={15} />} title="Agent status" action={<Badge tone={pending ? "neutral" : dc ? "crit" : "ok"} dot pulse={!dc && !pending}>{pending ? "Not paired yet" : dc ? "Disconnected" : "Connected"}</Badge>} />
        {pending ? (
          <div className="space-y-4">
            <p className="text-sm text-muted">Install the agent on the VPS to start monitoring. Each install command has a single-use token valid for one hour.</p>
            <Button onClick={repair} disabled={busy}>{busy ? <Spinner size={14} /> : <KeyRound size={14} />} Get install command</Button>
          </div>
        ) : (
          <>
            <dl className="grid grid-cols-2 gap-x-6 sm:grid-cols-3">
              <KV label="Agent version"><span>v{s.agentVersion}</span> {outdated && <Badge tone="warn" className="ml-1">update v{s.latestAgentVersion}</Badge>}</KV>
              <KV label="Connected since">{dc || !s.connectedSince ? "—" : dateTime(s.connectedSince)}</KV>
              <KV label="Last heartbeat">{seen(s)}</KV>
              <KV label="Latency">{dc ? "—" : `${s.latencyMs} ms`}</KV>
              <KV label="Transport">{dc ? "—" : s.transport === "wss" ? "WSS (TLS)" : "WS (unencrypted, local only)"}</KV>
              <KV label="Host">{s.hostname}</KV>
              <KV label="Kernel" mono>{s.kernel}</KV>
              <KV label="Public IP" mono>{s.ip || "—"}</KV>
              <KV label="Interface IPs" mono>{s.privateIps.join(", ") || "—"}</KV>
            </dl>
            {outdated && (
              <div className="mt-4 flex flex-wrap items-center justify-between gap-3 rounded-2xl bg-warn-soft px-4 py-3 text-sm">
                <span>Agent v{s.latestAgentVersion} is available (this server runs v{s.agentVersion}).</span>
                <Button size="sm" disabled={busy || dc} onClick={() => setDlg("update")}><RefreshCw size={13} /> Update agent</Button>
              </div>
            )}
            <div className="mt-5 flex flex-wrap gap-2 border-t border-line pt-5">
              <Button size="sm" variant="secondary" disabled={busy || dc} onClick={() => setDlg("restart")}>{busy ? <Spinner size={13} /> : <RefreshCw size={13} />} Restart agent</Button>
              <Button size="sm" variant="secondary" disabled={busy} onClick={() => setDlg("repair")}><KeyRound size={13} /> New install command</Button>
              <Button size="sm" variant="danger" disabled={busy} onClick={() => setDlg("revoke")}><PowerOff size={13} /> Revoke agent</Button>
            </div>
          </>
        )}
      </Card>
      <div className="space-y-6">
        <Card>
          <SectionTitle icon={<ListChecks size={15} />} title="Capabilities" sub="Reported by the agent" />
          <div className="flex flex-wrap gap-1.5">
            {s.capabilities.length ? s.capabilities.map((c) => <Badge key={c} tone="ok">{c}</Badge>) : <span className="text-sm text-muted">Reported when the agent connects.</span>}
          </div>
        </Card>
        <Card>
          <SectionTitle icon={<HeartPulse size={15} />} title="Recent heartbeats" sub="WebSocket ping round-trips" />
          {s.pings.length === 0 ? (
            <p className="text-sm text-muted">{dc || pending ? "No live connection." : "First ping within 15 seconds."}</p>
          ) : (
            <ul className="space-y-2 text-sm">
              {[...s.pings].reverse().map((p) => (
                <li key={p.ts} className="flex items-center justify-between">
                  <span className="font-mono text-xs">{clock(p.ts)}</span>
                  <Badge tone={p.ms > 200 ? "warn" : "ok"}>{p.ms} ms</Badge>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      <ConfirmDialog open={dlg === "restart"} title="Restart agent?" message="The agent process exits and systemd starts it again. Metrics may have a gap of a few seconds." confirmLabel="Restart agent" onConfirm={restart} onCancel={() => setDlg(null)} />
      <ConfirmDialog open={dlg === "update"} title={`Update the agent to v${s.latestAgentVersion}?`} confirmLabel="Update agent" message="The agent downloads the new binary from this dashboard, checks its SHA-256, replaces itself and restarts. Monitoring pauses for a few seconds. If the check fails, nothing is replaced and the error appears in the server's events." onConfirm={updateAgent} onCancel={() => setDlg(null)} />
      <ConfirmDialog open={dlg === "repair"} title="Create a new install command?" confirmLabel="Create" message="Running it on the VPS installs the current agent and re-pairs it. The current agent credential stops working as soon as the new one enrolls." onCancel={() => setDlg(null)} onConfirm={repair} />
      <ConfirmDialog open={dlg === "revoke"} title={`Revoke the agent of ${s.name}?`} danger confirmLabel="Revoke" message="The agent is disconnected and its credential stops working. History is kept. To monitor this server again, run a new install command on it." onCancel={() => setDlg(null)} onConfirm={revoke} />
      <Modal open={!!enroll} onClose={() => setEnroll(null)} title="Install command" wide footer={<Button onClick={() => setEnroll(null)}>Done</Button>}>
        {enroll && <InstallCommand info={enroll} />}
      </Modal>
    </div>
  );
}

export function ServerSettings() {
  const s = useServer()!;
  const { toast, upsertServer, removeServer } = useStore();
  const nav = useNavigate();
  const initial = { name: s.name, provider: s.provider, notes: s.notes, notifyOverride: s.notifyOverride };
  const [f, setF] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [del, setDel] = useState(false);
  const dirty = JSON.stringify(f) !== JSON.stringify(initial);
  const save = async () => {
    setBusy(true);
    try {
      upsertServer(await api<Server>("PATCH", `/api/servers/${s.id}`, f));
      toast("Server settings saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const remove = async () => {
    setDel(false);
    try {
      await api("DELETE", `/api/servers/${s.id}`);
      removeServer(s.id);
      toast(`${s.name} deleted`);
      nav("/servers");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  return (
    <div className="max-w-2xl space-y-6">
      <Card>
        <SectionTitle icon={<Settings size={15} />} title="Server settings" />
        <div className="space-y-5">
          <Field label="Server display name"><Input value={f.name} maxLength={64} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
          <Field label="Provider label" hint="Free text, used for grouping and filters"><Input value={f.provider} maxLength={64} placeholder="e.g. Hetzner" onChange={(e) => setF({ ...f, provider: e.target.value })} /></Field>
          <Field label="Notes"><Textarea rows={3} value={f.notes} onChange={(e) => setF({ ...f, notes: e.target.value })} /></Field>
          <Field label="Notification override" hint="Overrides global notification rules for this server only.">
            <Select value={f.notifyOverride} onChange={(e) => setF({ ...f, notifyOverride: e.target.value as typeof f.notifyOverride })}>
              <option value="default">Use global rules</option>
              <option value="all">Notify on everything</option>
              <option value="critical-only">Critical incidents only</option>
              <option value="muted">Muted</option>
            </Select>
          </Field>
          <div className="flex gap-2">
            <Button disabled={!dirty || busy} onClick={save}>{busy && <Spinner size={14} />} Save changes</Button>
            <Button variant="ghost" disabled={!dirty} onClick={() => setF(initial)}>Reset</Button>
          </div>
        </div>
      </Card>
      <Card>
        <SectionTitle icon={<PowerOff size={15} />} title="Uninstall agent" sub="Run this on the VPS. It stops ops-agent and removes its binary, service, config and credential. Your apps are not touched." />
        <div className="flex items-center gap-2 rounded-2xl bg-surface2 px-4 py-3">
          <code className="flex-1 break-all font-mono text-xs">{`curl -fsSL ${location.origin}/uninstall.sh | sudo bash`}</code>
          <CopyButton text={`curl -fsSL ${location.origin}/uninstall.sh | sudo bash`} />
        </div>
        <p className="mt-3 text-xs text-muted">Then delete the server below to remove its history from the dashboard.</p>
      </Card>
      <Card>
        <SectionTitle icon={<Trash2 size={15} />} title="Delete server" sub="Revokes the agent and deletes all stored metrics and events for this server." />
        <Button variant="danger" onClick={() => setDel(true)}><Trash2 size={14} /> Delete {s.name}</Button>
        <p className="mt-3 text-xs text-muted">The agent stays installed on the VPS but can no longer connect. Uninstall it there first (see above).</p>
      </Card>
      <ConfirmDialog open={del} danger title={`Delete ${s.name}?`} confirmLabel="Delete server" message="This cannot be undone. All metrics history and events for this server are deleted." onCancel={() => setDel(false)} onConfirm={remove} />
    </div>
  );
}
