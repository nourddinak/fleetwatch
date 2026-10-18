import { Link, useNavigate } from "react-router-dom";
import { AlertOctagon, Clock, Cog, Cpu, FileCode2, Globe, HardDrive, Layers, MemoryStick, Network, Server as ServerIcon } from "lucide-react";
import type { App } from "../mock/types";
import { useStore } from "../store/store";
import { fmtDur, fmtMB } from "../mock/util";
import { AppStatus, Badge, Table, Td, Tr, appTone, toneDot } from "./ui";
import { MaintenanceBadge, useMaintenance } from "./Maintenance";
import { RowLink } from "./domain";
import { cn } from "../utils/cn";

const mgr = (a: App) => (a.manager === "PM2" ? `PM2 #${a.pm2Id ?? "—"}` : a.manager);

/** An app's tile: its manager (PM2, systemd, a static folder) with its status as a dot. */
export function AppIcon({ a, size = 36 }: { a: App; size?: number }) {
  const Icon = a.manager === "PM2" ? Layers : a.manager === "systemd" ? Cog : FileCode2;
  return (
    <span style={{ width: size, height: size }} className="relative grid shrink-0 place-items-center rounded-xl border border-line bg-surface2 text-fg">
      <Icon size={Math.round(size * 0.44)} />
      <span className={cn("absolute -right-0.5 -top-0.5 h-2.5 w-2.5 rounded-full border-2 border-surface", toneDot[appTone(a.status)], a.status === "crash-loop" && "pulse-dot")} />
    </span>
  );
}

export function AppsTable({ apps, showServer = true }: { apps: App[]; showServer?: boolean }) {
  const { servers, incidents } = useStore();
  const nav = useNavigate();
  const inc = (id: string) => incidents.filter((i) => i.appId === id && i.status === "active").length;
  const srv = (id: string) => servers.find((s) => s.id === id);
  const head = ["Application", ...(showServer ? ["Server"] : []), "Status", "Manager", "Runtime", "CPU", "RAM", "Uptime", "Port", "Domain", "Storage", "Incidents"];

  return (
    <>
      <div className="hidden md:block">
        <Table head={head}>
          {apps.map((a) => (
            <Tr key={a.id} onClick={() => nav(`/applications/${a.id}`)}>
              <Td>
                <div className="flex items-center gap-3">
                  <AppIcon a={a} />
                  <div className="min-w-0">
                    <div className="font-medium">{a.name}</div>
                    <div className="max-w-[260px] truncate font-mono text-[11px] text-muted">{a.path}</div>
                  </div>
                </div>
              </Td>
              {showServer && <Td><RowLink to={`/servers/${a.serverId}`} className="inline-flex items-center gap-1.5 whitespace-nowrap"><ServerIcon size={12} className="text-muted" />{srv(a.serverId)?.name}</RowLink></Td>}
              <Td><span className="flex flex-wrap items-center gap-1"><AppStatus status={a.status} /><MaintenanceFor a={a} /></span></Td>
              <Td><Badge>{mgr(a)}</Badge></Td>
              <Td className="whitespace-nowrap text-muted">{a.runtime || "—"}</Td>
              <Td className="tabular-nums">{a.pid ? `${a.cpu.toFixed(1)}%` : "—"}</Td>
              <Td className="whitespace-nowrap tabular-nums">{a.pid ? fmtMB(a.ram) : "—"}</Td>
              <Td className="whitespace-nowrap text-muted">{a.pid ? fmtDur(a.uptimeSec) : "—"}</Td>
              <Td>{a.ports.length ? <span className="flex flex-wrap gap-1">{a.ports.map((p) => <span key={p.port} className="rounded-md bg-surface2 px-1.5 py-0.5 font-mono text-[11px]">{p.port}</span>)}</span> : <span className="text-muted">—</span>}</Td>
              <Td className="max-w-[190px] text-muted">{a.domain || a.suggestedDomain ? (
                <span className={cn("flex min-w-0 items-center gap-1.5", !a.domain && "italic")} title={a.domain ? undefined : "Detected from the web server (Caddy or Nginx); confirm it on the app page"}><Globe size={12} className="shrink-0" /><span className="truncate">{a.domain ?? a.suggestedDomain}</span></span>
              ) : "—"}</Td>
              <Td className="whitespace-nowrap tabular-nums">{a.folderMB ? fmtMB(a.folderMB) : "—"}</Td>
              <Td>{inc(a.id) ? <Badge tone="crit">{inc(a.id)}</Badge> : <span className="text-muted">0</span>}</Td>
            </Tr>
          ))}
        </Table>
      </div>
      <div className="space-y-3 p-3 md:hidden">
        {apps.map((a) => (
          <Link key={a.id} to={`/applications/${a.id}`} className="block rounded-2xl border border-line p-4">
            <div className="flex items-start gap-3">
              <AppIcon a={a} />
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium">{a.name}</div>
                <div className="truncate text-xs text-muted">{srv(a.serverId)?.name} · {mgr(a)} · {a.runtime}</div>
              </div>
              <AppStatus status={a.status} />
            </div>
            <div className={cn("mt-3 grid grid-cols-3 gap-2 text-xs text-muted")}>
              <span className="flex items-center gap-1.5"><Cpu size={12} /><b className="text-fg">{a.pid ? a.cpu.toFixed(1) + "%" : "—"}</b></span>
              <span className="flex items-center gap-1.5"><MemoryStick size={12} /><b className="text-fg">{a.pid ? fmtMB(a.ram) : "—"}</b></span>
              <span className="flex items-center gap-1.5"><Network size={12} /><b className="font-mono text-fg">{a.ports[0]?.port ?? "—"}</b></span>
              <span className="flex items-center gap-1.5"><Clock size={12} /><b className="text-fg">{a.pid ? fmtDur(a.uptimeSec) : "—"}</b></span>
              <span className="flex items-center gap-1.5"><HardDrive size={12} /><b className="text-fg">{a.folderMB ? fmtMB(a.folderMB) : "—"}</b></span>
              <span className="flex items-center gap-1.5"><AlertOctagon size={12} /><b className={inc(a.id) ? "text-crit" : "text-fg"}>{inc(a.id)}</b></span>
            </div>
            {a.domain && <div className="mt-2 flex items-center gap-1.5 truncate text-xs text-muted"><Globe size={12} />{a.domain}</div>}
          </Link>
        ))}
      </div>
    </>
  );
}

function MaintenanceFor({ a }: { a: App }) {
  return <MaintenanceBadge until={useMaintenance("app", a.id, a.serverId)} />;
}
