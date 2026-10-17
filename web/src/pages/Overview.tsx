import { Link, useNavigate } from "react-router-dom";
import { useState } from "react";
import { AlertOctagon, AlertTriangle, ArrowRight, Bell, Boxes, CheckCircle2, Cpu, Database, Flame, Globe, HardDrive, HeartPulse, LayoutGrid, MemoryStick, Network, PlugZap, Radar, ServerCrash, Server as ServerIcon, ShieldCheck, Unplug, Wrench } from "lucide-react";
import { serverCounts, useStore } from "../store/store";
import { AppStatus, Bar, Card, EmptyState, Gate, MetricCard, PageHeader, SectionTitle, ServerStatus, levelTone, Button } from "../components/ui";
import { IncidentCard, IncidentDrawer, NotificationItem } from "../components/domain";
import { fmtMB, relTime } from "../mock/util";
import type { AppStatusT } from "../mock/types";
import { cn } from "../utils/cn";


const STATUS_COLOR: Record<AppStatusT, string> = { online: "bg-ok", warning: "bg-warn", starting: "bg-info", stopped: "bg-muted", offline: "bg-crit", "crash-loop": "bg-crit/70", unknown: "bg-line" };

function More({ to, label = "View all" }: { to: string; label?: string }) {
  return (
    <Link to={to} className="inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
      {label} <ArrowRight size={12} />
    </Link>
  );
}

export function Overview() {
  const { servers, apps, incidents, discoveries, notifications, tickCount } = useStore();
  const nav = useNavigate();
  const [drawer, setDrawer] = useState<string | null>(null);
  void tickCount;
  const empty = false;

  const connected = servers.filter((s) => s.status !== "disconnected" && s.status !== "pending");
  const offline = servers.filter((s) => s.status === "disconnected").length;
  const providers = new Set(servers.map((s) => s.provider).filter(Boolean)).size;
  const diskPct = (u: number, t: number) => (t ? (u / t) * 100 : 0);
  const avg = (k: "cpu" | "ram") => (connected.reduce((s, x) => s + x[k], 0) / Math.max(1, connected.length)).toFixed(1);
  const diskUsed = servers.reduce((s, x) => s + (x.diskGb * x.disk) / 100, 0);
  const diskTotal = servers.reduce((s, x) => s + x.diskGb, 0);
  const active = incidents.filter((i) => i.status === "active");
  const critical = active.filter((i) => i.severity === "critical");
  const appsOnline = apps.filter((a) => a.status === "online").length;
  // Down when they should be up; apps stopped on purpose are not an alert.
  const appsOff = apps.filter((a) => a.status === "offline" || a.status === "crash-loop").length;
  const appsStopped = apps.filter((a) => a.status === "stopped").length;
  const appsWarn = apps.filter((a) => a.status === "warning" || a.healthStatus === "failed").length;

  const needServers = empty ? [] : servers.filter((s) => s.status !== "online");
  const needApps = empty ? [] : apps.filter((a) => a.status !== "online" || a.healthStatus === "failed");
  const recent = empty ? [] : incidents.filter((i) => i.status !== "resolved").sort((a, b) => b.lastAt - a.lastAt).slice(0, 5);
  const recov = empty ? [] : incidents.filter((i) => i.status === "resolved").sort((a, b) => (b.resolvedAt ?? 0) - (a.resolvedAt ?? 0)).slice(0, 4);
  const dist = (Object.keys(STATUS_COLOR) as AppStatusT[]).map((s) => ({ s, n: apps.filter((a) => a.status === s).length })).filter((x) => x.n > 0);
  const pm2 = apps.filter((a) => a.manager === "PM2").length;
  const publicPorts = apps.reduce((n, a) => n + a.ports.filter((p) => p.bind === "0.0.0.0").length, 0);
  const domains = apps.filter((a) => a.domain).length;

  return (
    <>
      <PageHeader
        title="Overview"
        sub={`Fleet health across ${servers.length} servers · updated live`}
        actions={
          <>
            <Button variant="secondary" size="sm" onClick={() => nav("/servers/new")}>
              <ServerIcon size={14} /> Add server
            </Button>
            <Button size="sm" onClick={() => nav("/discoveries")}>
              <Radar size={14} /> {discoveries.length} discoveries
            </Button>
          </>
        }
      />
      <Gate>
        <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
          <MetricCard to="/servers" icon={<ServerIcon size={14} />} label="Total servers" value={servers.length} sub={`${providers} providers`} />
          <MetricCard to="/servers" icon={<PlugZap size={14} />} label="Servers online" value={connected.length} tone="ok" sub={`${servers.filter((s) => s.status === "online").length} fully healthy`} />
          <MetricCard to="/servers" icon={<Unplug size={14} />} label="Servers offline" value={offline} tone={offline ? "crit" : undefined} sub="agent disconnected" />
          <MetricCard to="/applications" icon={<Boxes size={14} />} label="Applications" value={apps.length} sub={`${pm2} PM2 · ${apps.length - pm2} systemd`} />
          <MetricCard to="/applications?status=online" icon={<CheckCircle2 size={14} />} label="Apps online" value={appsOnline} tone="ok" sub={`${Math.round((appsOnline / Math.max(1, apps.length)) * 100)}% of fleet`} />
          <MetricCard to="/applications?status=down" icon={<ServerCrash size={14} />} label="Apps offline" value={appsOff} tone={appsOff ? "crit" : undefined} sub={appsStopped ? `crashed or offline · ${appsStopped} stopped on purpose` : "crashed or offline"} />
          <MetricCard to="/applications?status=warning" icon={<AlertTriangle size={14} />} label="Apps with warnings" value={appsWarn} tone={appsWarn ? "warn" : undefined} sub="warning or failing health" />
          <MetricCard to="/incidents" icon={<AlertOctagon size={14} />} label="Active incidents" value={active.length} tone={active.length ? "warn" : "ok"} sub={`${incidents.filter((i) => i.status === "acknowledged").length} acknowledged`} />
          <MetricCard to="/incidents" icon={<Flame size={14} />} label="Critical incidents" value={critical.length} tone={critical.length ? "crit" : "ok"} sub="need attention" />
          <MetricCard label="Average CPU" value={avg("cpu")} unit="%" icon={<Cpu size={14} />} progress={Number(avg("cpu"))} sub="connected servers" />
          <MetricCard label="Average RAM" value={avg("ram")} unit="%" icon={<MemoryStick size={14} />} progress={Number(avg("ram"))} sub="connected servers" />
          <MetricCard to="/metrics" label="Total disk usage" value={Math.round(diskPct(diskUsed, diskTotal))} unit="%" icon={<HardDrive size={14} />} progress={diskPct(diskUsed, diskTotal)} sub={`${Math.round(diskUsed)} / ${diskTotal} GB`} />
        </div>

        <div className="mt-6 grid gap-6 lg:grid-cols-3">
          <Card className="lg:col-span-1">
            <SectionTitle icon={<LayoutGrid size={15} />} title="Infrastructure summary" sub="What the agents currently report" />
            <dl className="divide-y divide-line text-sm">
              {([
                [ServerIcon, "Servers", `${servers.length} across ${providers} providers`],
                [Boxes, "Applications", `${apps.length} monitored (${pm2} PM2, ${apps.length - pm2} systemd)`],
                [Globe, "Domains served", `${domains} public domains`],
                [Network, "Listening ports", `${apps.reduce((n, a) => n + a.ports.length, 0)} app ports · ${publicPorts} publicly bound`],
                [Database, "Project storage", fmtMB(apps.reduce((n, a) => n + a.folderMB, 0))],
                [ShieldCheck, "Agents", `${connected.length} connected · ${offline} disconnected`],
                [Radar, "Discoveries", `${discoveries.length} awaiting review`],
              ] as const).map(([Icon, k, v]) => (
                <div key={k} className="flex items-center justify-between gap-4 py-2.5">
                  <dt className="flex items-center gap-2 text-muted"><Icon size={14} />{k}</dt>
                  <dd className="text-right font-medium">{v}</dd>
                </div>
              ))}
            </dl>
          </Card>

          <Card className="lg:col-span-2">
            <SectionTitle icon={<Cpu size={15} />} title="Resource overview" sub="CPU · RAM · Disk per server" action={<More to="/metrics" label="Metrics explorer" />} />
            <div className="space-y-4">
              {servers.map((s) => (
                <Link key={s.id} to={`/servers/${s.id}`} className={cn("block rounded-2xl p-2 transition hover:bg-surface2", s.status === "disconnected" && "opacity-60")}>
                  <div className="mb-2 flex items-center justify-between gap-2 text-sm">
                    <span className="flex min-w-0 items-center gap-2 font-medium"><ServerIcon size={14} className="shrink-0 text-muted" /><span className="truncate">{s.name}</span></span>
                    <ServerStatus status={s.status} />
                  </div>
                  <div className="grid grid-cols-3 gap-4 text-[11px] text-muted">
                    {([["CPU", s.cpu, Cpu], ["RAM", s.ram, MemoryStick], ["Disk", s.disk, HardDrive]] as const).map(([l, v, Icon]) => (
                      <div key={l}>
                        <div className="mb-1 flex justify-between">
                          <span className="flex items-center gap-1"><Icon size={11} />{l}</span>
                          <span className={cn("tabular-nums", levelTone(v) !== "ok" && (levelTone(v) === "crit" ? "text-crit" : "text-warn"))}>{v.toFixed(0)}%</span>
                        </div>
                        <Bar value={v} />
                      </div>
                    ))}
                  </div>
                </Link>
              ))}
            </div>
          </Card>

          <Card>
            <SectionTitle icon={<Wrench size={15} />} title="Servers requiring attention" action={<More to="/servers" />} />
            {needServers.length === 0 ? (
              <EmptyState icon={<CheckCircle2 size={20} />} title="All servers healthy" desc="No server needs attention right now." />
            ) : (
              <ul className="space-y-1">
                {needServers.map((s) => {
                  const c = serverCounts(s.id, apps, incidents);
                  const why = s.status === "disconnected" ? "Agent heartbeat lost" : s.disk > 85 ? `Disk ${s.disk.toFixed(0)}% full` : s.cpu > 85 ? `CPU ${s.cpu.toFixed(0)}%` : `${c.incidents} active incident${c.incidents === 1 ? "" : "s"}`;
                  return (
                    <li key={s.id}>
                      <Link to={`/servers/${s.id}`} className="flex items-center gap-3 rounded-2xl px-3 py-2.5 hover:bg-surface2">
                        <span className={cn("grid h-8 w-8 shrink-0 place-items-center rounded-lg", s.status === "disconnected" || s.status === "critical" ? "bg-crit-soft text-crit" : "bg-warn-soft text-warn")}><ServerIcon size={14} /></span>
                        <div className="min-w-0 flex-1">
                          <div className="text-sm font-medium">{s.name}</div>
                          <div className="truncate text-xs text-muted">{why}</div>
                        </div>
                        <ServerStatus status={s.status} />
                      </Link>
                    </li>
                  );
                })}
              </ul>
            )}
          </Card>

          <Card>
            <SectionTitle icon={<HeartPulse size={15} />} title="Applications requiring attention" action={<More to="/applications?status=attention" />} />
            {needApps.length === 0 ? (
              <EmptyState icon={<CheckCircle2 size={20} />} title="Everything is running" />
            ) : (
              <ul className="max-h-[360px] space-y-1 overflow-y-auto">
                {needApps.slice(0, 9).map((a) => (
                  <li key={a.id}>
                    <Link to={`/applications/${a.id}`} className="flex items-center gap-3 rounded-2xl px-3 py-2.5 hover:bg-surface2">
                      <span className="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-surface2 text-muted"><Boxes size={14} /></span>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">{a.name}</div>
                        <div className="truncate text-xs text-muted">{servers.find((s) => s.id === a.serverId)?.name} · {a.manager}</div>
                      </div>
                      <AppStatus status={a.status} />
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card>
            <SectionTitle icon={<Boxes size={15} />} title="Application status" sub={`${apps.length} applications`} />
            <div className="mb-4 flex h-4 overflow-hidden rounded-full bg-neutral-soft">
              {dist.map((d) => (
                <button key={d.s} onClick={() => nav(`/applications?status=${d.s}`)} title={`${d.s}: ${d.n}`} className={cn("h-full transition hover:opacity-70", STATUS_COLOR[d.s])} style={{ width: `${(d.n / apps.length) * 100}%` }} />
              ))}
            </div>
            <ul className="space-y-1.5 text-sm">
              {dist.map((d) => (
                <li key={d.s}>
                  <Link to={`/applications?status=${d.s}`} className="flex items-center gap-3 rounded-xl px-2 py-1.5 hover:bg-surface2">
                    <span className={cn("h-2.5 w-2.5 rounded-full", STATUS_COLOR[d.s])} />
                    <span className="flex-1 capitalize">{d.s.replace("-", " ")}</span>
                    <span className="font-semibold tabular-nums">{d.n}</span>
                  </Link>
                </li>
              ))}
            </ul>
          </Card>

          <Card className="lg:col-span-2">
            <SectionTitle icon={<AlertOctagon size={15} />} title="Recent incidents" action={<More to="/incidents" />} />
            {recent.length === 0 ? <EmptyState icon={<CheckCircle2 size={20} />} title="No open incidents" /> : <div className="space-y-2">{recent.map((i) => <IncidentCard key={i.id} incident={i} onOpen={() => setDrawer(i.id)} />)}</div>}
          </Card>

          <Card>
            <SectionTitle icon={<HeartPulse size={15} />} title="Recent recoveries" action={<More to="/incidents" />} />
            {recov.length === 0 ? (
              <EmptyState title="No recoveries yet" />
            ) : (
              <ul className="space-y-3">
                {recov.map((i) => (
                  <li key={i.id} onClick={() => setDrawer(i.id)} className="flex cursor-pointer items-start gap-3 rounded-2xl p-2 hover:bg-surface2">
                    <span className="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-ok-soft text-ok"><CheckCircle2 size={14} /></span>
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium">{i.title}</div>
                      <div className="text-xs text-muted">Resolved {relTime(i.resolvedAt ?? i.lastAt)} · {servers.find((s) => s.id === i.serverId)?.name}</div>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card>
            <SectionTitle icon={<Radar size={15} />} title="Recent discoveries" action={<More to="/discoveries" />} />
            {discoveries.length === 0 || empty ? (
              <EmptyState title="Nothing new discovered" icon={<Radar size={20} />} />
            ) : (
              <ul className="space-y-1">
                {discoveries.slice(0, 4).map((d) => (
                  <li key={d.id}>
                    <Link to="/discoveries" className="flex items-center gap-3 rounded-2xl px-3 py-2.5 hover:bg-surface2">
                      <span className="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-info-soft text-info"><Radar size={14} /></span>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-medium">{d.name}</div>
                        <div className="truncate text-xs text-muted">{d.headline}</div>
                      </div>
                      <span className="shrink-0 text-xs text-muted">{relTime(d.discoveredAt)}</span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </Card>

          <Card className="lg:col-span-2">
            <SectionTitle icon={<Bell size={15} />} title="Recent notifications" action={<More to="/notifications" />} />
            {empty ? <EmptyState title="No notifications" /> : <div>{notifications.slice(0, 6).map((n) => <NotificationItem key={n.id} n={n} onClick={() => nav("/notifications")} />)}</div>}
          </Card>
        </div>
        <div className="mt-6 flex items-center gap-2 text-xs text-muted">
          <Boxes size={13} /> Click any tile, row or bar to open the corresponding page.
        </div>
        <IncidentDrawer incidentId={drawer} onClose={() => setDrawer(null)} />
      </Gate>
    </>
  );
}
