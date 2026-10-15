import { Link, useNavigate } from "react-router-dom";
import type { App } from "../mock/types";
import { useStore } from "../store/store";
import { fmtDur, fmtMB } from "../mock/util";
import { AppStatus, Badge, Table, Td, Tr } from "./ui";
import { RowLink } from "./domain";
import { cn } from "../utils/cn";

const mgr = (a: App) => (a.manager === "PM2" ? `PM2 #${a.pm2Id ?? "—"}` : a.manager);

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
                <div className="font-medium">{a.name}</div>
                <div className="font-mono text-[11px] text-muted">{a.path}</div>
              </Td>
              {showServer && <Td><RowLink to={`/servers/${a.serverId}`} className="whitespace-nowrap">{srv(a.serverId)?.name}</RowLink></Td>}
              <Td><AppStatus status={a.status} /></Td>
              <Td><Badge>{mgr(a)}</Badge></Td>
              <Td className="whitespace-nowrap text-muted">{a.runtime || "—"}</Td>
              <Td className="tabular-nums">{a.pid ? `${a.cpu.toFixed(1)}%` : "—"}</Td>
              <Td className="whitespace-nowrap tabular-nums">{a.pid ? fmtMB(a.ram) : "—"}</Td>
              <Td className="whitespace-nowrap text-muted">{a.pid ? fmtDur(a.uptimeSec) : "—"}</Td>
              <Td className="font-mono text-xs">{a.ports.length ? a.ports.map((p) => p.port).join(", ") : "—"}</Td>
              <Td className="max-w-[170px] truncate text-muted">{a.domain ?? (a.suggestedDomain ? <span className="italic" title="Detected from Caddy; confirm it on the app page">{a.suggestedDomain}</span> : "—")}</Td>
              <Td className="whitespace-nowrap tabular-nums">{a.folderMB ? fmtMB(a.folderMB) : "—"}</Td>
              <Td>{inc(a.id) ? <Badge tone="crit">{inc(a.id)}</Badge> : <span className="text-muted">0</span>}</Td>
            </Tr>
          ))}
        </Table>
      </div>
      <div className="space-y-3 p-3 md:hidden">
        {apps.map((a) => (
          <Link key={a.id} to={`/applications/${a.id}`} className="block rounded-2xl border border-line p-4">
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="truncate font-medium">{a.name}</div>
                <div className="truncate text-xs text-muted">{srv(a.serverId)?.name} · {mgr(a)} · {a.runtime}</div>
              </div>
              <AppStatus status={a.status} />
            </div>
            <div className={cn("mt-3 grid grid-cols-3 gap-2 text-xs text-muted")}>
              <span>CPU <b className="text-fg">{a.pid ? a.cpu.toFixed(1) + "%" : "—"}</b></span>
              <span>RAM <b className="text-fg">{a.pid ? fmtMB(a.ram) : "—"}</b></span>
              <span>Port <b className="text-fg">{a.ports[0]?.port ?? "—"}</b></span>
              <span>Up <b className="text-fg">{a.pid ? fmtDur(a.uptimeSec) : "—"}</b></span>
              <span>Disk <b className="text-fg">{a.folderMB ? fmtMB(a.folderMB) : "—"}</b></span>
              <span>Inc <b className={inc(a.id) ? "text-crit" : "text-fg"}>{inc(a.id)}</b></span>
            </div>
            {a.domain && <div className="mt-2 truncate text-xs text-muted">{a.domain}</div>}
          </Link>
        ))}
      </div>
    </>
  );
}
