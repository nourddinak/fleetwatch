import { useMemo, useState } from "react";
import { MaintenanceBadge } from "../components/Maintenance";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Server as ServerIcon } from "lucide-react";
import { serverCounts, useStore } from "../store/store";
import { Badge, Bar, Button, Card, EmptyState, FilterBar, Gate, LoadingState, PageHeader, SearchInput, Select, ServerStatus, levelTone } from "../components/ui";
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
          <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
            {list.map((s) => {
              if (s.status === "pending")
                return (
                  <Link key={s.id} to={`/servers/${s.id}/agent`}>
                    <Card className="h-full border-dashed transition hover:border-fg/30">
                      <div className="flex items-start justify-between gap-3">
                        <h3 className="truncate text-lg font-semibold tracking-tight">{s.name}</h3>
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
                  <Card className={cn("h-full transition hover:border-fg/30", dc && "border-crit/30")}>
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0">
                        <h3 className="truncate text-lg font-semibold tracking-tight">{s.name}</h3>
                        <p className="truncate text-xs text-muted">{s.hostname}</p>
                      </div>
                      <ServerStatus status={s.status} />
                    </div>
                    <div className="mt-3 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted">
                      <span className="font-mono">{s.ip}</span>
                      {s.provider && <span>{s.provider}</span>}
                      {s.os && <span>{s.os}</span>}
                    </div>
                    <div className={cn("mt-5 grid grid-cols-3 gap-4", dc && "opacity-50")}>
                      {([["CPU", s.cpu], ["RAM", s.ram], ["Disk", s.disk]] as const).map(([l, v]) => (
                        <div key={l}>
                          <div className="mb-1.5 flex items-baseline justify-between">
                            <span className="text-[11px] text-muted">{l}</span>
                            <span className={cn("text-sm font-semibold tabular-nums", levelTone(v) === "crit" && "text-crit", levelTone(v) === "warn" && "text-warn")}>{v.toFixed(0)}%</span>
                          </div>
                          <Bar value={v} />
                        </div>
                      ))}
                    </div>
                    <div className="mt-5 grid grid-cols-2 gap-y-1.5 text-xs text-muted">
                      <span>Load <b className="font-semibold text-fg">{s.load.join(" · ")}</b></span>
                      <span>Uptime <b className="font-semibold text-fg">{dc ? "—" : fmtDur(s.uptimeSec)}</b></span>
                      <span>Network ↓ <b className="font-semibold text-fg">{s.netRx}</b> ↑ <b className="font-semibold text-fg">{s.netTx}</b> Mbps</span>
                      <span>Agent <b className="font-semibold text-fg">v{s.agentVersion}</b></span>
                    </div>
                    <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-4">
                      <Badge>{c.apps} apps</Badge>
                      <Badge tone={c.incidents ? "crit" : "neutral"}>{c.incidents} incidents</Badge>
                      <Badge tone={c.warnings ? "warn" : "neutral"}>{c.warnings} warnings</Badge>
                      <span className="ml-auto text-[11px] text-muted">{dc ? "last seen " : "seen "}{relTime(s.lastSeen)}</span>
                    </div>
                  </Card>
                </Link>
              );
            })}
          </div>
        )}
      </Gate>
    </>
  );
}
