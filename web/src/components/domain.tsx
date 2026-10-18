import { useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router-dom";
import { AlertTriangle, Bell, BellOff, Boxes, Check, Clock, Cog, Eye, EyeOff, Flame, FolderGit2, GitBranch, Info, Layers, Plus, Radar, Repeat, Server as ServerIcon, ShieldCheck, Tag, Terminal } from "lucide-react";
import type { App, Discovery, Incident, NotificationRec, Server } from "../mock/types";
import { exposure } from "../utils/net";
import { clock, dateTime, fmtMB, relTime } from "../mock/util";
import { cn } from "../utils/cn";
import { useStore } from "../store/store";
import { Badge, Button, Card, CopyButton, Drawer, IncidentStatusBadge, KV, Modal, Select, SeverityBadge, Spinner, Table, Td, Tr } from "./ui";
import type { ApiError } from "../api/client";
import { IncidentComments, RunbookBox } from "./IncidentExtras";

/* ---------------- pickers ---------------- */
export function ServerPicker({ value, onChange, includeAll = true, className }: { value: string; onChange: (v: string) => void; includeAll?: boolean; className?: string }) {
  const { servers } = useStore();
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} className={className} aria-label="Server">
      {includeAll && <option value="all">All servers</option>}
      {servers.map((s) => (
        <option key={s.id} value={s.id}>
          {s.name}
        </option>
      ))}
    </Select>
  );
}
export function ApplicationPicker({ value, onChange, serverId = "all", includeAll = true, className }: { value: string; onChange: (v: string) => void; serverId?: string; includeAll?: boolean; className?: string }) {
  const { apps } = useStore();
  const list = apps.filter((a) => serverId === "all" || a.serverId === serverId);
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} className={className} aria-label="Application">
      {includeAll && <option value="all">All applications</option>}
      {list.map((a) => (
        <option key={a.id} value={a.id}>
          {a.name}
        </option>
      ))}
    </Select>
  );
}

/* ---------------- ports ---------------- */
export interface PortRow {
  app: App | null;
  name: string;
  pid: number | null;
  proto: string;
  bind: string;
  port: number;
}
export function PortTable({ rows }: { rows: PortRow[] }) {
  return (
    <Table head={["Application", "PID", "Protocol", "Bind address", "Port", "Exposure"]}>
      {rows.map((r, i) => {
        const pub = exposure(r.bind) === "Public";
        return (
          <Tr key={i}>
            <Td className="font-medium">{r.app ? <Link to={`/applications/${r.app.id}`} className="hover:underline">{r.app.name}</Link> : <span className="text-muted">{r.name} <span className="text-[10px]">(system)</span></span>}</Td>
            <Td className="font-mono text-xs">{r.pid ?? "—"}</Td>
            <Td>{r.proto}</Td>
            <Td className="font-mono text-xs">{r.bind}</Td>
            <Td className="font-mono font-semibold">{r.port}</Td>
            <Td>
              <Badge tone={pub ? "warn" : "ok"}>{pub ? "Public" : "Local only"}</Badge>
            </Td>
          </Tr>
        );
      })}
    </Table>
  );
}

/* ---------------- storage ---------------- */
const PALETTE = ["#6366f1", "#f59e0b", "#10b981", "#ec4899", "#06b6d4", "#8b5cf6", "#94a3b8"];
export function StorageBreakdown({ items, total }: { items: { name: string; mb: number }[]; total?: number }) {
  const sum = total ?? items.reduce((s, i) => s + i.mb, 0);
  return (
    <div>
      <div className="mb-4 flex h-3 w-full overflow-hidden rounded-full bg-neutral-soft">
        {items.map((it, i) => (
          <div key={it.name} style={{ width: `${(it.mb / sum) * 100}%`, background: PALETTE[i % PALETTE.length] }} title={`${it.name} — ${fmtMB(it.mb)}`} />
        ))}
      </div>
      <ul className="divide-y divide-line">
        {items.map((it, i) => (
          <li key={it.name} className="flex items-center gap-3 py-2.5 text-sm">
            <span className="h-2.5 w-2.5 rounded-full" style={{ background: PALETTE[i % PALETTE.length] }} />
            <span className="flex-1 font-mono text-[13px]">{it.name}</span>
            <span className="tabular-nums text-muted">{((it.mb / sum) * 100).toFixed(1)}%</span>
            <span className="w-20 text-right font-medium tabular-nums">{fmtMB(it.mb)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/* ---------------- git ---------------- */
export function GitSummary({ app, full }: { app: App; full?: boolean }) {
  const g = app.git;
  if (!g) return <div className="rounded-2xl bg-surface2 p-4 text-sm text-muted">No Git repository detected in {app.path}.</div>;
  const changes = g.changes ?? [];
  return (
    <div>
      <div className="grid grid-cols-2 gap-x-6 sm:grid-cols-3">
        <KV label="Repository" mono>{g.repo}</KV>
        <KV label="Branch"><span className="inline-flex items-center gap-1"><GitBranch size={13} /> {g.branch}</span></KV>
        <KV label="Commit" mono>{g.commit}</KV>
        <div className="col-span-2 sm:col-span-3"><KV label="Message">{g.message}</KV></div>
        {full && (
          <>
            <KV label="Remote" mono>{g.remote}</KV>
            <KV label="Author">{g.author}</KV>
            <KV label="Committed">{dateTime(g.date)} ({relTime(g.date)})</KV>
          </>
        )}
        <KV label="Working tree"><Badge tone={g.dirty ? "warn" : "ok"} dot>{g.dirty ? "Dirty" : "Clean"}</Badge></KV>
        <KV label="Ahead / Behind">↑{g.ahead} · ↓{g.behind}</KV>
      </div>
      {full && changes.length > 0 && (
        <div className="mt-3 rounded-2xl border border-line">
          {changes.map((c) => (
            <div key={c.file} className="flex items-center gap-3 border-b border-line px-4 py-2.5 text-sm last:border-0">
              <Badge tone={c.state === "Modified" ? "warn" : c.state === "Untracked" ? "info" : "crit"} className="w-[84px] justify-center">{c.state}</Badge>
              <span className="font-mono text-[13px]">{c.file}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/* ---------------- incidents ---------------- */
const sevTile = { critical: "bg-crit-soft text-crit", warning: "bg-warn-soft text-warn", info: "bg-info-soft text-info" } as const;
const sevIcon = { critical: Flame, warning: AlertTriangle, info: Info } as const;
export function IncidentCard({ incident, onOpen, compact }: { incident: Incident; onOpen?: () => void; compact?: boolean }) {
  const { getApp, getServer } = useStore();
  const app = incident.appId ? getApp(incident.appId) : null;
  const srv = getServer(incident.serverId);
  return (
    <div onClick={onOpen} className={cn("relative flex items-start gap-3 overflow-hidden rounded-2xl border border-line bg-surface p-4 transition", onOpen && "cursor-pointer hover:border-fg/30", incident.status === "resolved" && "opacity-80")}>
      {(() => {
        const Icon = incident.status === "resolved" ? Check : sevIcon[incident.severity];
        return <span className={cn("grid h-10 w-10 shrink-0 place-items-center rounded-xl", incident.status === "resolved" ? "bg-ok-soft text-ok" : sevTile[incident.severity])}><Icon size={17} /></span>;
      })()}
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{incident.title}</span>
          <SeverityBadge severity={incident.severity} />
          <IncidentStatusBadge status={incident.status} />
        </div>
        <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
          <span className="inline-flex items-center gap-1.5"><Tag size={12} />{incident.type}</span>
          <span className="inline-flex items-center gap-1.5">{app ? <Boxes size={12} /> : <ServerIcon size={12} />}{app ? app.name : "Server-level"} · {srv?.name}</span>
          {!compact && <span className="inline-flex items-center gap-1.5"><Repeat size={12} />{incident.occurrences.toLocaleString()} occurrence{incident.occurrences === 1 ? "" : "s"}</span>}
          <span className="inline-flex items-center gap-1.5"><Clock size={12} />{incident.status === "resolved" && incident.resolvedAt ? `resolved ${relTime(incident.resolvedAt)}` : `started ${clock(incident.firstAt, false)} · last ${clock(incident.lastAt, false)}`}</span>
        </div>
      </div>
    </div>
  );
}

export function IncidentActions({ incident }: { incident: Incident }) {
  const { actIncident, toast } = useStore();
  const [busy, setBusy] = useState("");
  const act = async (action: string, msg: string) => {
    setBusy(action);
    try {
      await actIncident(incident.id, action);
      toast(msg);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const B = ({ action, msg, variant, children }: { action: string; msg: string; variant?: "secondary" | "ghost"; children: ReactNode }) => (
    <Button size="sm" variant={variant} disabled={!!busy} onClick={() => act(action, msg)}>{busy === action ? <Spinner size={13} /> : null}{children}</Button>
  );
  const open = incident.status !== "resolved";
  return (
    <div className="flex flex-wrap gap-2">
      {incident.status === "active" && <B action="acknowledge" msg="Incident acknowledged" variant="secondary"><Check size={13} /> Acknowledge</B>}
      {open && <B action="resolve" msg="Incident marked resolved"><ShieldCheck size={13} /> Mark resolved</B>}
      {open && incident.status !== "muted" && <B action="mute" msg="Notifications muted for 24h" variant="ghost"><BellOff size={13} /> Mute 24h</B>}
      {!open && <B action="reopen" msg="Incident re-opened" variant="secondary">Re-open</B>}
    </div>
  );
}

export function IncidentDrawer({ incidentId, onClose }: { incidentId: string | null; onClose: () => void }) {
  const { incidents, getApp, getServer } = useStore();
  const nav = useNavigate();
  const inc = incidents.find((i) => i.id === incidentId);
  const app = inc?.appId ? getApp(inc.appId) : null;
  const srv = inc ? getServer(inc.serverId) : null;
  return (
    <Drawer open={!!inc} onClose={onClose} title={inc?.title ?? ""}>
      {inc && (
        <div className="space-y-6">
          <div className="flex flex-wrap gap-2">
            <SeverityBadge severity={inc.severity} />
            <IncidentStatusBadge status={inc.status} />
            <Badge>{inc.type}</Badge>
            <Badge className="font-mono">{inc.id}</Badge>
          </div>
          <IncidentActions incident={inc} />
          <RunbookBox typeKey={inc.typeKey} />
          <dl className="grid grid-cols-2 gap-x-6 rounded-2xl bg-surface2 px-4">
            <KV label="Server">{srv ? <Link className="underline-offset-2 hover:underline" to={`/servers/${inc.serverId}`} onClick={onClose}>{srv.name}</Link> : "— (outside check)"}</KV>
            <KV label="Application">{app ? <Link className="underline-offset-2 hover:underline" to={`/applications/${app.id}`} onClick={onClose}>{app.name}</Link> : "— (server-level)"}</KV>
            <KV label="First detected">{dateTime(inc.firstAt)}</KV>
            <KV label="Last detected">{dateTime(inc.lastAt)}</KV>
            <KV label="Occurrences">{inc.occurrences.toLocaleString()}</KV>
            <KV label="Resolution">{inc.status === "resolved" ? `${inc.resolution || "—"}${inc.resolvedBy ? ` (${inc.resolvedBy === "auto" ? "automatic" : "by admin"})` : ""}` : inc.mutedUntil && inc.status === "muted" ? `Muted until ${dateTime(inc.mutedUntil)}` : "Not resolved yet"}</KV>
          </dl>
          <div>
            <h4 className="mb-3 text-sm font-semibold">Timeline</h4>
            <ol className="relative space-y-4 border-l border-line pl-5">
              {inc.timeline.map((t, i) => (
                <li key={i} className="relative">
                  <span className="absolute -left-[26px] top-1 h-2.5 w-2.5 rounded-full border-2 border-surface bg-fg" />
                  <div className="text-sm">{t.text}</div>
                  <div className="text-xs text-muted">{dateTime(t.ts)} · {relTime(t.ts)}</div>
                </li>
              ))}
            </ol>
          </div>
          <div>
            <h4 className="mb-3 text-sm font-semibold">Related logs</h4>
            <div className="space-y-1 rounded-2xl border border-line bg-surface2 p-3 font-mono text-[12px]">
              {inc.logSamples.length === 0 && <div className="text-muted">No samples recorded.</div>}
              {inc.logSamples.map((l, i) => (
                <div key={i} className="break-all">{l}</div>
              ))}
            </div>
          </div>
          <IncidentComments incidentId={inc.id} />
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" size="sm" onClick={() => { nav(`/incidents/${inc.id}/postmortem`); onClose(); }}>Postmortem</Button>
          </div>
          {app && (
            <div className="flex flex-wrap gap-2">
              <Button variant="secondary" size="sm" onClick={() => { nav(`/applications/${app.id}/logs`); onClose(); }}>View application logs</Button>
              <Button variant="secondary" size="sm" onClick={() => { nav(`/applications/${app.id}/incidents`); onClose(); }}>Application incidents</Button>
            </div>
          )}
        </div>
      )}
    </Drawer>
  );
}

/* ---------------- notifications ---------------- */
export const providerLabel: Record<string, string> = { ntfy: "ntfy", telegram: "Telegram", discord: "Discord", webhook: "Webhook", push: "Push" };
export function NotificationItem({ n, onClick }: { n: NotificationRec; onClick?: () => void }) {
  const { getApp, getServer } = useStore();
  const failed = n.result === "failed";
  return (
    <div onClick={onClick} className={cn("flex items-start gap-3 rounded-2xl px-3 py-3 transition", onClick && "cursor-pointer hover:bg-surface2")}>
      <div className={cn("mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-xl", failed ? "bg-crit-soft text-crit" : "bg-surface2 text-muted")}>
        <Bell size={14} />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-medium">{n.event}</span>
          {!n.read && <span className="h-1.5 w-1.5 rounded-full bg-crit" />}
          <Badge tone="info">{providerLabel[n.provider] ?? n.provider}</Badge>
          <Badge tone={failed ? "crit" : "ok"}>{failed ? "Failed" : "Delivered"}</Badge>
        </div>
        <div className="truncate text-xs text-muted">{n.title} · {n.appId ? getApp(n.appId)?.name : n.serverId ? getServer(n.serverId)?.name : "test"}</div>
      </div>
      <span className="shrink-0 text-xs text-muted">{relTime(n.ts)}</span>
    </div>
  );
}

/* ---------------- discovery ---------------- */
export function DiscoveryCard({ d, onAdd, onIgnore, onInspect, busy }: { d: Discovery; onAdd: () => void; onIgnore: () => void; onInspect: () => void; busy?: boolean }) {
  const { getServer } = useStore();
  const kindLabel = d.kind === "pm2" ? "PM2" : d.kind === "systemd" ? "systemd" : "Folder";
  return (
    <Card className="flex flex-col gap-4">
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <span className="grid h-11 w-11 shrink-0 place-items-center rounded-2xl border border-line bg-surface2 text-fg">
            {d.kind === "pm2" ? <Layers size={18} /> : d.kind === "systemd" ? <Cog size={18} /> : <FolderGit2 size={18} />}
          </span>
          <div className="min-w-0">
            <h3 className="truncate text-[15px] font-semibold tracking-tight">{d.name}</h3>
            <p className="mt-0.5 flex items-center gap-1.5 text-xs text-muted"><Radar size={11} className="shrink-0" /><span className="truncate">{d.headline} · {relTime(d.discoveredAt)}</span></p>
          </div>
        </div>
        <Badge tone="info" dot pulse>New</Badge>
      </div>
      <dl className="grid grid-cols-2 gap-x-4">
        <KV label="Server">{getServer(d.serverId)?.name ?? "—"}</KV>
        <KV label="Type">{kindLabel}</KV>
        {d.kind !== "folder" && <KV label={d.kind === "pm2" ? "PM2 name" : "Unit"} mono>{d.unit}</KV>}
        <KV label="Runtime">{d.runtime || "—"}</KV>
        <div className="col-span-2"><KV label="Path" mono>{d.path || "—"}</KV></div>
        <KV label="Port">{d.ports.length ? <span className="inline-flex flex-wrap items-center gap-1">{d.ports.map((p) => p.port).join(", ")}{d.public && <Badge tone="warn">public</Badge>}</span> : "—"}</KV>
        <KV label="Git">{d.git ? <Badge tone="ok">Detected</Badge> : <Badge>Not detected</Badge>}</KV>
        {d.suggestedDomain && <div className="col-span-2"><KV label="Domain (from the web server)">{d.suggestedDomain}</KV></div>}
      </dl>
      <div className="mt-auto flex flex-wrap gap-2">
        <Button size="sm" onClick={onAdd} disabled={busy}><Plus size={13} /> Add application</Button>
        <Button size="sm" variant="secondary" onClick={onInspect}><Eye size={13} /> Inspect</Button>
        <Button size="sm" variant="ghost" onClick={onIgnore} disabled={busy}><EyeOff size={13} /> Ignore</Button>
      </div>
    </Card>
  );
}

export function InspectModal({ d, onClose, onAdd }: { d: Discovery | null; onClose: () => void; onAdd: () => void }) {
  const { getServer } = useStore();
  return (
    <Modal open={!!d} onClose={onClose} title={`Inspect — ${d?.name}`} wide footer={<><Button variant="secondary" onClick={onClose}>Close</Button>{d?.state !== "added" && <Button onClick={onAdd}><Plus size={14} /> Add application</Button>}</>}>
      {d && (
        <div className="space-y-4">
          <dl className="grid grid-cols-2 gap-x-4 sm:grid-cols-3">
            <KV label="Server">{getServer(d.serverId)?.name ?? "—"}</KV>
            <KV label={d.kind === "folder" ? "Folder owner" : "Process user"} mono>{d.user || "—"}</KV>
            <KV label="PID" mono>{d.pid ?? "—"}</KV>
            <KV label="Manager state" mono>{d.status || "—"}</KV>
            <KV label="Runtime">{d.runtime || "—"}</KV>
            <KV label="Last seen">{relTime(d.lastSeen)}</KV>
          </dl>
          {d.reason && <p className="rounded-xl bg-surface2 px-3 py-2 text-xs text-muted">Detected because: {d.reason}</p>}
          {d.cmdline && (
            <div>
              <div className="mb-1 text-[11px] uppercase tracking-wide text-muted">Command line</div>
              <code className="block break-all rounded-xl bg-surface2 px-3 py-2 font-mono text-xs">{d.cmdline}</code>
            </div>
          )}
          <div>
            <div className="mb-1 text-[11px] uppercase tracking-wide text-muted">Listening ports</div>
            {d.ports.length === 0 ? (
              <p className="text-sm text-muted">None detected.</p>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {d.ports.map((p) => (
                  <span key={p.proto + p.bind + p.port} className="rounded-full bg-surface2 px-2.5 py-1 font-mono text-xs">{p.proto} {p.bind}:{p.port} · {exposure(p.bind)}</span>
                ))}
              </div>
            )}
          </div>
          {d.gitRemote && <KV label="Git remote" mono>{d.gitRemote}</KV>}
          {d.suggestedDomain && <KV label="Domain from the web server">{d.suggestedDomain}</KV>}
          {d.files.length > 0 && (
            <div>
              <div className="mb-1 text-[11px] uppercase tracking-wide text-muted">Folder contents · {d.path}</div>
              <div className="flex flex-wrap gap-1.5">
                {d.files.map((f) => (
                  <span key={f} className={cn("rounded-full px-2.5 py-1 font-mono text-xs", d.markers.includes(f.replace(/\/$/, "")) ? "bg-ok-soft text-ok" : "bg-surface2")}>{f}</span>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </Modal>
  );
}

/* ---------------- app / server rows ---------------- */
export function RowLink({ to, children, className }: { to: string; children: ReactNode; className?: string }) {
  return <Link to={to} className={cn("hover:underline", className)} onClick={(e) => e.stopPropagation()}>{children}</Link>;
}

export function ServerChip({ server }: { server?: Server }) {
  if (!server) return <span className="text-muted">—</span>;
  return (
    <RowLink to={`/servers/${server.id}`} className="inline-flex items-center gap-1.5 text-sm">
      <ServerIcon size={12} className="text-muted" /> {server.name}
    </RowLink>
  );
}

export function CommandHint() {
  return <Terminal size={14} />;
}

export { CopyButton };
