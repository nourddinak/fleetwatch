import { useMemo, useState } from "react";
import { MaintenanceBadge } from "../components/Maintenance";
import { Link, useNavigate } from "react-router-dom";
import { Activity, AlertOctagon, AlertTriangle, ArrowDown, ArrowUp, Bot, Boxes, Clock, Cloud, Cpu, HardDrive, Hourglass, MemoryStick, Monitor, Network, Plus, PlugZap, Server as ServerIcon, Timer, Unplug } from "lucide-react";
import { serverCounts, useStore } from "../store/store";
import { Bar, Button, Card, EmptyState, FilterBar, Gate, LoadingState, PageHeader, SearchInput, Select, ServerStatus, levelTone } from "../components/ui";
import { fmtDur, relTime } from "../mock/util";
import { cn } from "../utils/cn";
import type { Server } from "../mock/types";

const ORDER = { critical: 0, disconnected: 1, warning: 2, pending: 3, online: 4 };

export function Servers() {
  const { servers, serversLoaded, apps, incidents, maintenance } = useStore();
  const nav = useNavigate();
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("all");
  const [provider, setProvider] = useState("all");
  const [sort, setSort] = useState("status");

  const providers = useMemo(() => [...new Set(servers.map((s) => s.provider).filter(Boolean))].sort(), [servers]);
  const list = useMemo(() => {
    const s = q.trim().toLowerCase();
    const l = servers.filter((x) => (status === "all" || x.status === status) && (provider === "all" || x.provider === provider) && (!s || [x.name, x.hostname, x.ip, x.provider, x.os, ...x.privateIps].join(" ").toLowerCase().includes(s)));
    const cnt = (x: Server) => serverCounts(x.id, apps, incidents);
    return [...l].sort((a, b) => {
      switch (sort) {
        case "name": return a.name.localeCompare(b.name);
        case "cpu": return b.cpu - a.cpu;
        case "ram": return b.ram - a.ram;
        case "disk": return b.disk - a.disk;
        case "apps": return cnt(b).apps - cnt(a).apps;
        default: return ORDER[a.status] - ORDER[b.status];
      }
    });
  }, [servers, apps, incidents, q, status, provider, sort]);

  return (
    <>
      <PageHeader title="Servers" sub={`${servers.length} VPS ${servers.length === 1 ? "instance" : "instances"} monitored by Go agents`} actions={<Button onClick={() => nav("/servers/new")}><Plus size={15} /> Add server</Button>} />
      <Gate>
        <FilterBar>
          <SearchInput value={q} onChange={setQ} placeholder="Search name, host, IP…" className="lg:w-72" />
          <Select value={status} onChange={(e) => setStatus(e.target.value)} className="lg:!w-auto">
            <option value="all">All statuses</option>
            <option value="online">Online</option>
            <option value="warning">Warning</option>
            <option value="critical">Critical</option>
            <option value="disconnected">Disconnected</option>
            <option value="pending">Waiting for agent</option>
          </Select>
          <Select value={provider} onChange={(e) => setProvider(e.target.value)} className="lg:!w-auto">
            <option value="all">All providers</option>
            {providers.map((p) => <option key={p}>{p}</option>)}
          </Select>
          <Select value={sort} onChange={(e) => setSort(e.target.value)} className="lg:!w-auto">
            <option value="status">Sort: status (worst first)</option>
            <option value="name">Sort: name</option>
            <option value="cpu">Sort: CPU</option>
            <option value="ram">Sort: RAM</option>
            <option value="disk">Sort: disk</option>
            <option value="apps">Sort: applications</option>
          </Select>
          <span className="text-xs text-muted lg:ml-auto">{list.length} of {servers.length} servers</span>
        </FilterBar>
        {!serversLoaded ? (
          <LoadingState label="Loading servers…" />
        ) : servers.length === 0 ? (
          <EmptyState icon={<ServerIcon size={20} />} title="No servers yet" desc="Pair a VPS by installing the agent with a one-time token." action={<Button onClick={() => nav("/servers/new")}>Add your first server</Button>} />
        ) : list.length === 0 ? (
          <EmptyState icon={<ServerIcon size={20} />} title="No servers match your filters" action={<Button onClick={() => (setQ(""), setStatus("all"), setProvider("all"))}>Clear filters</Button>} />
        ) : (
          <>
          <div className="mb-4 flex flex-wrap gap-2 text-xs">
            {([
              ["online", "online", PlugZap, servers.filter((x) => x.status === "online").length],
              ["warning", "need attention", AlertTriangle, servers.filter((x) => x.status === "warning" || x.status === "critical").length],
              ["disconnected", "offline", Unplug, servers.filter((x) => x.status === "disconnected").length],
              ["pending", "waiting for agent", Hourglass, servers.filter((x) => x.status === "pending").length],
            ] as const).map(([k, word, Icon, n]) => n > 0 && (
              <button key={k} onClick={() => setStatus(status === k ? "all" : k)}
                className={cn("inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5 transition", status === k ? "border-fg/40 bg-surface2 text-fg" : "border-line bg-surface text-muted hover:text-fg")}>
                <Icon size={13} /> {n} {word}
              </button>
            ))}
          </div>
          <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
            {list.map((s) => {
              if (s.status === "pending")
                return (
                  <Link key={s.id} to={`/servers/${s.id}/agent`}>
                    <Card className="h-full border-dashed transition hover:border-fg/30">
                      <div className="flex items-start gap-3">
                        <span className="grid h-11 w-11 shrink-0 place-items-center rounded-2xl border border-dashed border-line text-muted"><Hourglass size={18} /></span>
                        <h3 className="min-w-0 flex-1 truncate pt-2 text-[15px] font-semibold tracking-tight">{s.name}</h3>
                        <ServerStatus status={s.status} />
                      <MaintenanceBadge until={s.status === "pending" ? 0 : maintenance.servers[s.id] > Date.now() ? maintenance.servers[s.id] : 0} />
                      </div>
                      <p className="mt-3 text-sm text-muted">The agent has not connected yet. Open the server to get a new install command.</p>
                    </Card>
                  </Link>
                );
              const c = serverCounts(s.id, apps, incidents);
              const dc = s.status === "disconnected";
              return (
                <Link key={s.id} to={`/servers/${s.id}`}>
                  <Card className={cn("flex h-full flex-col gap-4 transition hover:border-fg/30", dc && "border-crit/30")}>
                    <div className="flex items-start gap-3">
                      <span className="relative grid h-11 w-11 shrink-0 place-items-center rounded-2xl border border-line bg-surface2 text-fg">
                        <ServerIcon size={18} />
                        <span className={cn("absolute -right-0.5 -top-0.5 h-3 w-3 rounded-full border-2 border-surface",
                          { online: "bg-ok", warning: "bg-warn", critical: "bg-crit", disconnected: "bg-crit", pending: "bg-muted" }[s.status], s.status === "disconnected" && "pulse-dot")} />
                      </span>
                      <div className="min-w-0 flex-1">
                        <h3 className="truncate text-[15px] font-semibold tracking-tight">{s.name}</h3>
                        <p className="truncate text-xs text-muted">{s.hostname}</p>
                      </div>
                      <div className="flex shrink-0 flex-col items-end gap-1">
                        <ServerStatus status={s.status} />
                        <MaintenanceBadge until={maintenance.servers[s.id] > Date.now() ? maintenance.servers[s.id] : 0} />
                      </div>
                    </div>
                    <div className="flex flex-wrap gap-1.5 text-[11px] text-muted">
                      <span className="inline-flex items-center gap-1.5 rounded-full border border-line px-2.5 py-1 font-mono"><Network size={11} />{s.ip}</span>
                      {s.provider && <span className="inline-flex items-center gap-1.5 rounded-full border border-line px-2.5 py-1"><Cloud size={11} />{s.provider}</span>}
                      {s.os && <span className="inline-flex min-w-0 items-center gap-1.5 rounded-full border border-line px-2.5 py-1"><Monitor size={11} /><span className="truncate">{s.os}</span></span>}
                    </div>
                    <div className={cn("grid grid-cols-3 gap-3", dc && "opacity-50")}>
                      {([["CPU", s.cpu, Cpu, `${s.cpuCores} cores`], ["RAM", s.ram, MemoryStick, `${s.ramGb} GB`], ["Disk", s.disk, HardDrive, `${s.diskGb} GB`]] as const).map(([l, v, Icon, size]) => (
                        <div key={l} className="rounded-2xl bg-surface2/60 p-3">
                          <div className="mb-1 flex items-center gap-1.5 text-[11px] text-muted"><Icon size={12} />{l}</div>
                          <div className={cn("text-lg font-semibold tabular-nums leading-tight", levelTone(v) === "crit" && "text-crit", levelTone(v) === "warn" && "text-warn")}>{v.toFixed(0)}<span className="text-xs font-normal text-muted">%</span></div>
                          <Bar value={v} className="mt-2" />
                          <div className="mt-1.5 truncate text-[10px] text-muted">{size}</div>
                        </div>
                      ))}
                    </div>
                    <div className="grid grid-cols-2 gap-x-3 gap-y-2 text-xs text-muted">
                      <span className="flex min-w-0 items-center gap-1.5"><Activity size={13} className="shrink-0" />Load <b className="truncate font-semibold tabular-nums text-fg">{s.load.map((x) => x.toFixed(2)).join(" ")}</b></span>
                      <span className="flex items-center gap-1.5"><Timer size={13} />Up <b className="font-semibold text-fg">{dc ? "—" : fmtDur(s.uptimeSec)}</b></span>
                      <span className="flex items-center gap-1.5"><ArrowDown size={13} /><b className="font-semibold tabular-nums text-fg">{s.netRx}</b><ArrowUp size={13} className="ml-1" /><b className="font-semibold tabular-nums text-fg">{s.netTx}</b> Mbps</span>
                      <span className="flex items-center gap-1.5"><Bot size={13} />Agent <b className="font-semibold text-fg">v{s.agentVersion}</b></span>
                    </div>
                    <div className="mt-auto flex flex-wrap items-center gap-x-4 gap-y-1.5 border-t border-line pt-3 text-[11px] text-muted">
                      <span className="flex items-center gap-1.5"><Boxes size={12} />{c.apps} apps</span>
                      <span className={cn("flex items-center gap-1.5", c.incidents > 0 && "font-medium text-crit")}><AlertOctagon size={12} />{c.incidents} incidents</span>
                      <span className={cn("flex items-center gap-1.5", c.warnings > 0 && "font-medium text-warn")}><AlertTriangle size={12} />{c.warnings} warnings</span>
                      <span className="ml-auto flex items-center gap-1.5"><Clock size={12} />{dc ? "last seen " : "seen "}{relTime(s.lastSeen)}</span>
                    </div>
                  </Card>
                </Link>
              );
            })}
          </div>
          </>
        )}
      </Gate>
    </>
  );
}
