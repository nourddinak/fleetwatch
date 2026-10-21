import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { AlertTriangle, Boxes, CornerDownLeft, Globe, LayoutGrid, Network, RotateCw, ScrollText, Search, Server as ServerIcon, Terminal } from "lucide-react";
import { useStore } from "../store/store";
import { cn } from "../utils/cn";
import { exposure } from "../utils/net";
import { api } from "../api/client";
import type { LogLine } from "./LogViewer";

interface Item {
  id: string;
  group: string;
  title: string;
  sub?: string;
  to: string;
  icon: ReactNode;
}

export const PAGES = [
  { to: "/", label: "Overview" },
  { to: "/servers", label: "Servers" },
  { to: "/applications", label: "Applications" },
  { to: "/docker", label: "Docker" },
  { to: "/databases", label: "Databases" },
  { to: "/logs", label: "Logs" },
  { to: "/cron", label: "Cron jobs" },
  { to: "/backups", label: "Backups" },
  { to: "/incidents", label: "Incidents" },
  { to: "/domains", label: "Domains" },
  { to: "/status-page", label: "Status pages" },
  { to: "/security", label: "Security" },
  { to: "/discoveries", label: "Discoveries" },
  { to: "/metrics", label: "Metrics" },
  { to: "/notifications", label: "Notifications" },
  { to: "/settings", label: "Settings" },
  { to: "/servers/new", label: "Add server" },
];

interface CommandHit {
  id: string;
  appId: string;
  name: string;
  command: string;
}

export function SearchPalette({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { servers, apps, incidents } = useStore();
  const nav = useNavigate();
  const [q, setQ] = useState("");
  const [idx, setIdx] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const [logHits, setLogHits] = useState<LogLine[]>([]);
  const [cmdHits, setCmdHits] = useState<CommandHit[]>([]);

  // Logs (full-text index) and custom commands are searched on the server, debounced.
  useEffect(() => {
    const term = q.trim();
    if (term.length < 2) {
      setCmdHits([]);
      return setLogHits([]);
    }
    let live = true;
    const t = setTimeout(() => {
      api<CommandHit[]>("GET", `/api/search/commands?q=${encodeURIComponent(term)}`)
        .then((r) => live && setCmdHits(r))
        .catch(() => live && setCmdHits([]));
      if (term.length < 3) return setLogHits([]);
      api<{ lines: LogLine[] }>("GET", `/api/logs?limit=5&q=${encodeURIComponent(term)}`)
        .then((r) => live && setLogHits(r.lines))
        .catch(() => live && setLogHits([]));
    }, 300);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [q]);

  useEffect(() => {
    if (open) {
      setQ("");
      setIdx(0);
      setTimeout(() => input.current?.focus(), 30);
    }
  }, [open]);

  const items = useMemo<Item[]>(() => {
    const s = q.trim().toLowerCase();
    const out: Item[] = [];
    const ic = (n: ReactNode) => n;
    if (!s) {
      PAGES.forEach((p) => out.push({ id: "p" + p.to, group: "Go to", title: p.label, to: p.to, icon: ic(<LayoutGrid size={15} />) }));
      return out;
    }
    const matchApps = apps.filter((a) => [a.name, a.slug, a.domain ?? "", a.path, a.pm2Name, a.unit, a.runtime].join(" ").toLowerCase().includes(s) || a.ports.some((p) => String(p.port) === s));
    const sv = (id: string) => servers.find((x) => x.id === id);
    matchApps.slice(0, 5).forEach((a) => out.push({ id: "a" + a.id, group: "Applications", title: `Open ${a.name}`, sub: `${sv(a.serverId)?.name} · ${a.manager} · ${a.status}`, to: `/applications/${a.id}`, icon: <Boxes size={15} /> }));
    matchApps.slice(0, 2).forEach((a) => {
      out.push({ id: "al" + a.id, group: "Quick actions", title: `View ${a.name} logs`, to: `/applications/${a.id}/logs`, icon: <ScrollText size={15} /> });
      out.push({ id: "as" + a.id, group: "Quick actions", title: `Open ${a.name} server`, sub: sv(a.serverId)?.name, to: `/servers/${a.serverId}`, icon: <ServerIcon size={15} /> });
      out.push({ id: "ai" + a.id, group: "Quick actions", title: `View ${a.name} incidents`, to: `/applications/${a.id}/incidents`, icon: <AlertTriangle size={15} /> });
      if (a.manager !== "standalone") out.push({ id: "ar" + a.id, group: "Quick actions", title: `Restart ${a.name}`, sub: "asks for confirmation", to: `/applications/${a.id}/commands?run=builtin:restart`, icon: <RotateCw size={15} /> });
    });
    servers
      .filter((x) => [x.name, x.hostname, x.ip, x.provider, x.os, x.id, ...x.privateIps].join(" ").toLowerCase().includes(s))
      .slice(0, 4)
      .forEach((x) => out.push({ id: "s" + x.id, group: "Servers", title: x.name, sub: [x.hostname, x.ip, x.provider].filter(Boolean).join(" · "), to: `/servers/${x.id}`, icon: <ServerIcon size={15} /> }));
    apps.filter((a) => a.domain && a.domain.toLowerCase().includes(s) && !matchApps.includes(a)).slice(0, 4).forEach((a) => out.push({ id: "d" + a.id, group: "Domains", title: a.domain!, sub: a.name, to: `/applications/${a.id}`, icon: <Globe size={15} /> }));
    if (/^\d{2,5}$/.test(s)) {
      apps.forEach((a) =>
        a.ports
          .filter((p) => String(p.port).includes(s))
          .forEach((p) => out.push({ id: `p${a.id}${p.port}`, group: "Ports", title: `Port ${p.port} · ${a.name}`, sub: `${p.bind} · ${exposure(p.bind)} · ${sv(a.serverId)?.name}`, to: `/servers/${a.serverId}/ports`, icon: <Network size={15} /> })),
      );
    }
    incidents
      .filter((i) => [i.title, i.type, i.status].join(" ").toLowerCase().includes(s))
      .slice(0, 4)
      .forEach((i) => out.push({ id: "i" + i.id, group: "Incidents", title: i.title, sub: `${i.type} · ${i.severity} · ${i.status}`, to: i.appId ? `/applications/${i.appId}/incidents` : `/servers/${i.serverId}/incidents`, icon: <AlertTriangle size={15} /> }));
    cmdHits.forEach((c) => out.push({ id: "c" + c.id, group: "Commands", title: `${c.name} · ${apps.find((a) => a.id === c.appId)?.name ?? ""}`, sub: c.command, to: `/applications/${c.appId}/commands`, icon: <Terminal size={15} /> }));
    logHits.forEach((l) =>
      out.push({
        id: "l" + l.id,
        group: "Logs",
        title: l.message,
        sub: `${apps.find((a) => a.id === l.appId)?.name ?? servers.find((x) => x.id === l.serverId)?.name ?? "—"} · ${l.severity} · ${new Date(l.ts).toLocaleString()}`,
        to: l.appId ? `/applications/${l.appId}/logs?q=${encodeURIComponent(q.trim())}` : `/logs?q=${encodeURIComponent(q.trim())}`,
        icon: <ScrollText size={15} />,
      }),
    );
    PAGES.filter((p) => p.label.toLowerCase().includes(s)).forEach((p) => out.push({ id: "pg" + p.to, group: "Go to", title: p.label, to: p.to, icon: <LayoutGrid size={15} /> }));
    return out;
  }, [q, apps, servers, incidents, logHits, cmdHits]);

  useEffect(() => setIdx(0), [q]);

  if (!open) return null;
  const go = (it: Item) => {
    nav(it.to);
    onClose();
  };
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setIdx((i) => Math.min(items.length - 1, i + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setIdx((i) => Math.max(0, i - 1));
    } else if (e.key === "Enter" && items[idx]) go(items[idx]);
    else if (e.key === "Escape") onClose();
  };

  let lastGroup = "";
  return (
    <div className="fixed inset-0 z-[80] flex items-start justify-center bg-black/50 p-4 pt-[10vh] backdrop-blur-sm" onMouseDown={onClose}>
      <div onMouseDown={(e) => e.stopPropagation()} className="fade-in w-full max-w-2xl overflow-hidden rounded-3xl border border-line bg-surface">
        <div className="flex items-center gap-3 border-b border-line px-5">
          <Search size={18} className="text-muted" />
          <input ref={input} value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={onKey} placeholder="Search servers, apps, domains, ports, incidents, logs…" className="h-14 flex-1 bg-transparent text-sm outline-none placeholder:text-muted" />
          <kbd className="rounded-md border border-line px-1.5 py-0.5 text-[10px] text-muted">ESC</kbd>
        </div>
        <div className="max-h-[56vh] overflow-y-auto p-2">
          {items.length === 0 && (
            <div className="px-4 py-10 text-center text-sm text-muted">
              No results for “{q}”. Search by app or server name, domain, IP or port number.
            </div>
          )}
          {items.map((it, i) => {
            const head = it.group !== lastGroup;
            lastGroup = it.group;
            return (
              <div key={it.id}>
                {head && <div className="px-3 pb-1 pt-3 text-[10px] font-semibold uppercase tracking-wider text-muted">{it.group}</div>}
                <button onMouseEnter={() => setIdx(i)} onClick={() => go(it)} className={cn("flex w-full items-center gap-3 rounded-2xl px-3 py-2.5 text-left transition", i === idx ? "bg-surface2" : "")}>
                  <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-surface2 text-muted">{it.icon}</span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium">{it.title}</span>
                    {it.sub && <span className="block truncate text-xs text-muted">{it.sub}</span>}
                  </span>
                  {i === idx && <CornerDownLeft size={14} className="text-muted" />}
                </button>
              </div>
            );
          })}
        </div>
        <div className="flex items-center gap-4 border-t border-line px-5 py-2.5 text-[11px] text-muted">
          <span>↑↓ navigate</span>
          <span>↵ open</span>
          <span className="ml-auto">{items.length} results</span>
        </div>
      </div>
    </div>
  );
}
