import { Fragment, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { AlertTriangle, Ban, Bug, Clock, Download, Flame, Globe, Info, ListOrdered, Package, PackageCheck, Plus, RefreshCw, Server as ServerIcon, ShieldCheck, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, Input, LoadingState, Modal, SectionTitle, Select, Spinner, Table, Td, Textarea, ToggleRow, Tr, type Tone } from "../components/ui";
import { cn } from "../utils/cn";

const useOwner = () => {
  const { me } = useStore();
  return { owner: !me || me.role === "owner", operator: !me || me.role !== "viewer" };
};
const fail = (toast: (m: string, k?: "ok" | "err" | "info") => void) => (e: unknown) => toast((e as ApiError).message, "err");

/* ---------------- Blocked IPs ---------------- */

interface Block {
  ip: string;
  reason: string;
  source: "auto" | "manual";
  by?: string;
  createdAt: number;
  expiresAt: number;
  servers?: string[];
}
interface BlockSettings {
  enabled: boolean;
  threshold: number;
  windowMin: number;
  banHours: number;
  allow: string[];
}

export function BlockedIPs() {
  const { toast, subscribeLive, getServer } = useStore();
  const { owner } = useOwner();
  const data = useApi<{ enabled: boolean; blocks: Block[]; servers: Record<string, { at: number; applied: number; error?: string }> }>("/api/blocks");
  const { setData, reload } = data;
  const [add, setAdd] = useState<{ ip: string; hours: number; reason: string } | null>(null);
  const [del, setDel] = useState<Block | null>(null);
  useEffect(() => subscribeLive((m) => {
    if (m.type === "blocks") setData((d) => (d ? { ...d, blocks: m.data } : d));
    if (m.type === "blocks.status") setData((d) => (d ? { ...d, servers: { ...d.servers, [m.data.serverId]: m.data.status } } : d));
  }), [subscribeLive, setData]);
  if (data.error) return <ErrorState message={data.error} onRetry={reload} />;
  if (!data.data) return <LoadingState />;
  const errors = Object.entries(data.data.servers).filter(([, st]) => st.error);
  const save = async () => {
    if (!add) return;
    try {
      await api("POST", "/api/blocks", add);
      toast(`${add.ip} blocked on every server`);
      setAdd(null);
      reload();
    } catch (e) {
      fail(toast)(e);
    }
  };
  const unblock = async (b: Block) => {
    setDel(null);
    try {
      await api("DELETE", `/api/blocks/${b.ip}`);
      toast(`${b.ip} unblocked`);
      reload();
    } catch (e) {
      fail(toast)(e);
    }
  };
  return (
    <div className="space-y-4">
      {owner && <BlockingSettings onSaved={reload} />}
      {!owner && <p className="text-sm text-muted">{data.data.enabled ? "Addresses that keep guessing SSH logins are blocked on every server." : "Automatic blocking is off."}</p>}
      {errors.length > 0 && (
        <div className="rounded-2xl bg-warn-soft px-4 py-3 text-sm text-warn">
          {errors.map(([id, st]) => <div key={id}>{getServer(id)?.name ?? id}: {st.error}</div>)}
        </div>
      )}
      <Card pad={false}>
        <div className="p-5 pb-2">
          <SectionTitle icon={<Ban size={15} />} title={`Blocked addresses (${data.data.blocks.length})`} sub={data.data.enabled ? `Dropped by the firewall of every server${Object.keys(data.data.servers).length ? ` (${Object.keys(data.data.servers).length} applied the list)` : ""}.` : "Turn blocking on to enforce this list."}
            action={owner && <Button size="sm" onClick={() => setAdd({ ip: "", hours: 0, reason: "" })}><Plus size={14} /> Block</Button>} />
        </div>
        {data.data.blocks.length === 0 ? (
          <div className="p-5 pt-0"><EmptyState icon={<Ban size={20} />} title="Nothing blocked" desc="Addresses appear here when they guess wrong too often, or when you block them." /></div>
        ) : (
          <Table head={["Address", "Why", "Since", "Until", ""]}>
            {data.data.blocks.map((b) => (
              <Tr key={b.ip}>
                <Td className="font-mono text-xs"><span className="inline-flex items-center gap-1.5"><Ban size={12} className="text-crit" />{b.ip}</span></Td>
                <Td>
                  <div className="text-sm">{b.reason}</div>
                  <div className="text-[11px] text-muted">{b.source === "auto" ? `automatic${b.servers?.length ? ` · seen on ${b.servers.map((id) => getServer(id)?.name ?? id).join(", ")}` : ""}` : `by ${b.by ?? "hand"}`}</div>
                </Td>
                <Td className="text-xs text-muted">{relTime(b.createdAt)}</Td>
                <Td className="text-xs text-muted">{b.expiresAt ? new Date(b.expiresAt).toLocaleString() : "until removed"}</Td>
                <Td className="text-right">{owner && <Button size="sm" variant="ghost" onClick={() => setDel(b)}>Unblock</Button>}</Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
      {add && (
        <Modal open onClose={() => setAdd(null)} title="Block an address everywhere" footer={<><Button variant="ghost" onClick={() => setAdd(null)}>Cancel</Button><Button disabled={!add.ip} onClick={save}>Block</Button></>}>
          <div className="space-y-4">
            <Field label="IP address or range"><Input value={add.ip} onChange={(e) => setAdd({ ...add, ip: e.target.value })} placeholder="203.0.113.7 or 203.0.113.0/24" /></Field>
            <Field label="For"><Select value={add.hours} onChange={(e) => setAdd({ ...add, hours: Number(e.target.value) })}>
              {[[1, "1 hour"], [24, "1 day"], [168, "1 week"], [720, "30 days"], [0, "until removed"]].map(([v, l]) => <option key={v} value={v}>{l}</option>)}
            </Select></Field>
            <Field label="Note (optional)"><Input value={add.reason} onChange={(e) => setAdd({ ...add, reason: e.target.value })} /></Field>
            <p className="text-xs text-muted">Your servers' own addresses, private addresses and addresses someone signed in from are never blocked.</p>
          </div>
        </Modal>
      )}
      <ConfirmDialog open={!!del} title={`Unblock ${del?.ip}?`} message="It can reach your servers again." confirmLabel="Unblock" onConfirm={() => del && unblock(del)} onCancel={() => setDel(null)} />
    </div>
  );
}

function BlockingSettings({ onSaved }: { onSaved: () => void }) {
  const { toast } = useStore();
  const s = useApi<BlockSettings>("/api/settings/blocking");
  const [f, setF] = useState<(BlockSettings & { allowText: string }) | null>(null);
  useEffect(() => {
    if (s.data) setF({ ...s.data, allowText: s.data.allow.join("\n") });
  }, [s.data]);
  if (!f || !s.data) return <Card><LoadingState /></Card>;
  const save = async (next = f) => {
    try {
      const { allowText, ...rest } = next;
      s.setData(await api<BlockSettings>("PUT", "/api/settings/blocking", { ...rest, allow: allowText.split(/\s+/).filter(Boolean) }));
      toast(next.enabled ? "Blocking is on" : "Saved");
      onSaved();
    } catch (e) {
      fail(toast)(e);
    }
  };
  const dirty = JSON.stringify({ ...f, allowText: undefined, allow: f.allowText.split(/\s+/).filter(Boolean) }) !== JSON.stringify({ ...s.data, allowText: undefined });
  return (
    <Card>
      <ToggleRow label="Block repeat SSH guessers on every server" desc="When one address fails to log in too often across your servers, every server's firewall drops it. Works with iptables (agent v0.9.38+)."
        checked={f.enabled} onChange={(v) => { const n = { ...f, enabled: v }; setF(n); save(n); }} />
      <div className="mt-3 grid gap-3 sm:grid-cols-3">
        <Field label="Failed logins"><Input type="number" min={3} value={f.threshold} onChange={(e) => setF({ ...f, threshold: Number(e.target.value) })} /></Field>
        <Field label="Within (minutes)"><Input type="number" min={1} value={f.windowMin} onChange={(e) => setF({ ...f, windowMin: Number(e.target.value) })} /></Field>
        <Field label="Block for (hours)" hint="0 = until removed"><Input type="number" min={0} value={f.banHours} onChange={(e) => setF({ ...f, banHours: Number(e.target.value) })} /></Field>
      </div>
      <Field label="Never block" hint="One address or range per line. Your servers and addresses that signed in are already safe." className="mt-3">
        <Textarea rows={2} value={f.allowText} onChange={(e) => setF({ ...f, allowText: e.target.value })} placeholder="203.0.113.10&#10;198.51.100.0/24" />
      </Field>
      {dirty && <div className="mt-3"><Button size="sm" onClick={() => save()}>Save</Button></div>}
    </Card>
  );
}

/* ---------------- Package updates ---------------- */

interface Pkg {
  name: string;
  current?: string;
  new: string;
  security: boolean;
}
interface UpdatesState {
  report: { at: number; manager: string; packages: Pkg[]; rebootRequired: boolean; autoSecurity: boolean; listsAt?: number; error?: string } | null;
  scanning: boolean;
  installing?: "security" | "all";
  last?: { ok: boolean; output?: string; error?: string };
  lastBy?: string;
}
interface Auto {
  enabled: boolean;
  securityOnly: boolean;
  days: number[];
  hour: number;
  timeZone: string;
}
const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function Updates() {
  const { servers, subscribeLive, toast } = useStore();
  const { owner, operator } = useOwner();
  const data = useApi<{ servers: Record<string, UpdatesState>; auto: Auto }>("/api/updates");
  const { setData } = data;
  const [confirm, setConfirm] = useState<{ id: string; security: boolean } | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => subscribeLive((m) => {
    if (m.type === "updates") setData((d) => (d ? { ...d, servers: { ...d.servers, [m.data.serverId]: m.data.state } } : d));
  }), [subscribeLive, setData]);
  const asked = data.data !== null;
  useEffect(() => {
    if (!asked || !operator) return;
    for (const s of servers) {
      if (s.status !== "disconnected" && s.enrolled && !data.data?.servers[s.id]?.report) api("POST", `/api/servers/${s.id}/updates/scan`, { refresh: false }).catch(() => {});
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [asked]);
  if (data.error) return <ErrorState message={data.error} onRetry={data.reload} />;
  if (!data.data) return <LoadingState />;
  const scan = (id: string) => api("POST", `/api/servers/${id}/updates/scan`, { refresh: true }).catch(fail(toast));
  const install = async () => {
    if (!confirm) return;
    setConfirm(null);
    try {
      await api("POST", `/api/servers/${confirm.id}/updates/install`, { securityOnly: confirm.security });
      toast("Installing updates: this can take several minutes");
    } catch (e) {
      fail(toast)(e);
    }
  };
  const list = servers.filter((s) => s.enrolled);
  return (
    <div className="space-y-4">
      {owner && <AutoUpdates auto={data.data.auto} onSaved={(a) => setData({ ...data.data!, auto: a })} />}
      {list.length === 0 && <EmptyState icon={<PackageCheck size={20} />} title="No servers yet" />}
      {list.map((s) => {
        const st = data.data!.servers[s.id];
        const rep = st?.report;
        const sec = rep?.packages.filter((p) => p.security).length ?? 0;
        return (
          <Card key={s.id}>
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="flex min-w-0 items-start gap-3">
                <span className={cn("grid h-10 w-10 shrink-0 place-items-center rounded-xl", sec > 0 ? "bg-warn-soft text-warn" : rep && !rep.packages.length ? "bg-ok-soft text-ok" : "bg-surface2 text-fg")}><PackageCheck size={17} /></span>
                <div className="min-w-0">
                <Link to={`/servers/${s.id}`} className="font-semibold hover:underline">{s.name}</Link>
                <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted">
                  {rep ? (
                    <>
                      {sec > 0 && <Badge tone="warn">{sec} security</Badge>}
                      <Badge tone={rep.packages.length ? "info" : "ok"}>{rep.packages.length ? `${rep.packages.length} waiting` : "up to date"}</Badge>
                      {rep.rebootRequired && <Badge tone="warn">restart needed</Badge>}
                      {rep.autoSecurity && <Badge>auto security updates on</Badge>}
                      <span>{rep.manager} · checked {relTime(rep.at)}{rep.listsAt ? ` · lists from ${relTime(rep.listsAt)}` : ""}</span>
                    </>
                  ) : st?.scanning ? "Checking…" : s.status === "disconnected" ? "Offline" : "Not checked yet (agent v0.9.38+)"}
                </div>
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                {st?.installing ? (
                  <span className="flex items-center gap-2 text-sm text-muted"><Spinner size={14} /> Installing {st.installing === "security" ? "security " : ""}updates…</span>
                ) : (
                  <>
                    {operator && <Button size="sm" variant="ghost" disabled={st?.scanning} onClick={() => scan(s.id)} title="Download fresh package lists and check">{st?.scanning ? <Spinner size={14} /> : <RefreshCw size={14} />} Check</Button>}
                    {owner && sec > 0 && <Button size="sm" onClick={() => setConfirm({ id: s.id, security: true })}><Download size={14} /> Install security</Button>}
                    {owner && (rep?.packages.length ?? 0) > 0 && <Button size="sm" variant="secondary" onClick={() => setConfirm({ id: s.id, security: false })}>Install all</Button>}
                  </>
                )}
              </div>
            </div>
            {rep?.error && <p className="mt-2 text-sm text-crit">{rep.error}</p>}
            {st?.last && (
              <details className="mt-3 text-sm">
                <summary className={cn("cursor-pointer", st.last.ok ? "text-ok" : "text-crit")}>{st.last.ok ? "Last install worked" : `Last install failed: ${st.last.error}`}{st.lastBy ? ` (${st.lastBy === "schedule" ? "automatic window" : st.lastBy})` : ""}</summary>
                {st.last.output && <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded-xl bg-surface2 p-3 font-mono text-[11px]">{st.last.output}</pre>}
              </details>
            )}
            {rep && rep.packages.length > 0 && (
              <div className="mt-3">
                <button className="text-xs text-muted underline" onClick={() => setOpen(open === s.id ? null : s.id)}>{open === s.id ? "Hide" : "Show"} packages</button>
                {open === s.id && (
                  <ul className="mt-2 max-h-72 divide-y divide-line overflow-auto text-sm">
                    {[...rep.packages].sort((a, b) => Number(b.security) - Number(a.security)).map((p) => (
                      <li key={p.name} className="flex flex-wrap items-center gap-2 py-1.5">
                        <span className="font-mono text-xs">{p.name}</span>
                        {p.security && <Badge tone="warn">security</Badge>}
                        <span className="ml-auto text-xs text-muted">{p.current ? `${p.current} → ` : ""}{p.new}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            )}
          </Card>
        );
      })}
      <ConfirmDialog open={!!confirm} title={confirm?.security ? "Install security updates?" : "Install all updates?"} confirmLabel="Install"
        message={confirm?.security ? "Only the packages marked as security fixes are upgraded. Services using them may restart." : "Every waiting package is upgraded (apt upgrade --with-new-pkgs / dnf upgrade). Services may restart; a few minutes of slowness is normal."}
        onConfirm={install} onCancel={() => setConfirm(null)} />
    </div>
  );
}

function AutoUpdates({ auto, onSaved }: { auto: Auto; onSaved: (a: Auto) => void }) {
  const { toast } = useStore();
  const [f, setF] = useState(auto);
  const dirty = JSON.stringify(f) !== JSON.stringify(auto);
  const save = async () => {
    try {
      const { enabled, securityOnly, days, hour, timeZone } = f;
      onSaved(await api<Auto>("PUT", "/api/settings/auto-updates", { enabled, securityOnly, days, hour, timeZone }));
      toast("Saved");
    } catch (e) {
      fail(toast)(e);
    }
  };
  return (
    <Card>
      <ToggleRow label="Install updates automatically" desc="Once a day in the window below, on every connected server. You get a notification for each server." checked={f.enabled} onChange={(v) => setF({ ...f, enabled: v })} />
      {f.enabled && (
        <div className="mt-3 space-y-3">
          <ToggleRow label="Only security fixes" desc="Recommended: the rest waits for you." checked={f.securityOnly} onChange={(v) => setF({ ...f, securityOnly: v })} />
          <div className="flex flex-wrap gap-1.5">
            {DAYS.map((d, i) => (
              <button key={d} onClick={() => setF({ ...f, days: f.days.includes(i) ? f.days.filter((x) => x !== i) : [...f.days, i] })}
                className={cn("rounded-full border px-3 py-1 text-xs", f.days.includes(i) ? "border-transparent bg-accent text-accent-fg" : "border-line text-muted")}>{d}</button>
            ))}
            <span className="self-center text-xs text-muted">{f.days.length ? "" : "every day"}</span>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="At"><Select value={f.hour} onChange={(e) => setF({ ...f, hour: Number(e.target.value) })}>{Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}</Select></Field>
            <Field label="Time zone"><Input value={f.timeZone} onChange={(e) => setF({ ...f, timeZone: e.target.value })} placeholder="Europe/Paris" /></Field>
          </div>
        </div>
      )}
      {dirty && <div className="mt-3"><Button size="sm" onClick={save}>Save</Button></div>}
    </Card>
  );
}

/* ---------------- Vulnerable dependencies ---------------- */

interface Vuln {
  id: string;
  aliases?: string[];
  summary: string;
  severity: "critical" | "high" | "moderate" | "low" | "unknown";
  package: string;
  version: string;
  ecosystem: string;
  fixed?: string;
  url: string;
}
interface AppVulns {
  appId: string;
  serverId: string;
  scannedAt: number;
  deps: number;
  vulns: Vuln[];
  error?: string;
}
const VSEV: Record<Vuln["severity"], Tone> = { critical: "crit", high: "crit", moderate: "warn", low: "info", unknown: "neutral" };

export function Vulnerabilities() {
  const { getApp, getServer, subscribeLive, toast } = useStore();
  const { operator } = useOwner();
  const data = useApi<{ apps: AppVulns[]; scanning: string[] }>("/api/vulns");
  const { setData } = data;
  useEffect(() => subscribeLive((m) => {
    if (m.type === "vulns") setData((d) => ({ apps: m.data, scanning: d?.scanning.filter(() => false) ?? [] }));
    if (m.type === "vulns.scanning") setData((d) => (d ? { ...d, scanning: [...d.scanning, m.data.serverId] } : d));
  }), [subscribeLive, setData]);
  if (data.error) return <ErrorState message={data.error} onRetry={data.reload} />;
  if (!data.data) return <LoadingState />;
  const all = data.data.apps.flatMap((a) => a.vulns);
  const count = (s: Vuln["severity"][]) => all.filter((v) => s.includes(v.severity)).length;
  const scan = async () => {
    try {
      const r = await api<{ servers: number }>("POST", "/api/vulns/scan");
      toast(r.servers ? `Checking ${r.servers} server(s)…` : "No connected server with apps to check");
    } catch (e) {
      fail(toast)(e);
    }
  };
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap gap-2">
          <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-crit"><Flame size={13} /> {count(["critical", "high"])} critical or high</span>
          <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-warn"><AlertTriangle size={13} /> {count(["moderate"])} moderate</span>
          <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-xs text-muted"><Info size={13} /> {count(["low", "unknown"])} low</span>
        </div>
        {operator && <Button size="sm" variant="secondary" onClick={scan} disabled={data.data.scanning.length > 0}>{data.data.scanning.length ? <Spinner size={14} /> : <RefreshCw size={14} />} {data.data.scanning.length ? "Checking…" : "Check now"}</Button>}
      </div>
      <p className="text-sm text-muted">Exact versions from each app's lock files (package-lock.json, composer.lock, requirements.txt, Pipfile.lock, poetry.lock, go.mod), looked up daily in the OSV database (osv.dev).</p>
      {data.data.apps.length === 0 ? (
        <EmptyState icon={<Bug size={20} />} title="Nothing checked yet" desc="Apps with a lock file in their folder are checked once a day, or now with “Check now”." />
      ) : (
        data.data.apps.map((a) => (
          <Card key={a.appId} pad={false}>
            <div className="flex flex-wrap items-center justify-between gap-2 p-5 pb-3">
              <div className="flex min-w-0 items-center gap-3">
                <span className={cn("grid h-10 w-10 shrink-0 place-items-center rounded-xl", a.vulns.length ? "bg-crit-soft text-crit" : "bg-ok-soft text-ok")}>{a.vulns.length ? <Bug size={17} /> : <ShieldCheck size={17} />}</span>
                <div className="min-w-0">
                  <Link to={`/applications/${a.appId}`} className="font-semibold hover:underline">{getApp(a.appId)?.name ?? a.appId}</Link>
                  <div className="flex flex-wrap items-center gap-x-3 text-xs text-muted"><span className="inline-flex items-center gap-1"><ServerIcon size={11} />{getServer(a.serverId)?.name ?? a.serverId}</span><span className="inline-flex items-center gap-1"><Package size={11} />{a.deps} dependencies</span><span className="inline-flex items-center gap-1"><Clock size={11} />checked {relTime(a.scannedAt)}</span></div>
                </div>
              </div>
              {a.vulns.length === 0 ? <Badge tone="ok"><ShieldCheck size={12} /> no known vulnerabilities</Badge> : <Badge tone="crit">{new Set(a.vulns.map((v) => v.package + "@" + v.version)).size} vulnerable package{new Set(a.vulns.map((v) => v.package + "@" + v.version)).size === 1 ? "" : "s"}</Badge>}
            </div>
            {a.error && <p className="px-5 pb-3 text-sm text-warn">Last check failed: {a.error}</p>}
            {a.vulns.length > 0 && <VulnPackages vulns={a.vulns} />}
          </Card>
        ))
      )}
    </div>
  );
}

const RANK: Record<Vuln["severity"], number> = { critical: 0, high: 1, moderate: 2, low: 3, unknown: 4 };
// cmpVersion orders versions by their numbers (1.10.0 > 1.9.3).
function cmpVersion(a: string, b: string) {
  const x = a.match(/\d+/g)?.map(Number) ?? [];
  const y = b.match(/\d+/g)?.map(Number) ?? [];
  for (let i = 0; i < Math.max(x.length, y.length); i++) if ((x[i] ?? 0) !== (y[i] ?? 0)) return (x[i] ?? 0) - (y[i] ?? 0);
  return 0;
}

/** One row per vulnerable package: its worst advisory, how many, and the version that fixes them all. */
function VulnPackages({ vulns }: { vulns: Vuln[] }) {
  const [open, setOpen] = useState<string | null>(null);
  const groups = new Map<string, Vuln[]>();
  for (const v of vulns) groups.set(`${v.ecosystem}|${v.package}@${v.version}`, [...(groups.get(`${v.ecosystem}|${v.package}@${v.version}`) ?? []), v]);
  const rows = [...groups.entries()]
    .map(([key, list]) => {
      const worst = list.reduce((w, v) => (RANK[v.severity] < RANK[w] ? v.severity : w), "unknown" as Vuln["severity"]);
      const fixes = list.map((v) => v.fixed);
      const upgrade = fixes.includes(undefined) ? undefined : (fixes as string[]).reduce((m, f) => (cmpVersion(f, m) > 0 ? f : m), "0");
      return { key, list, worst, upgrade, head: list[0] };
    })
    .sort((a, b) => RANK[a.worst] - RANK[b.worst] || b.list.length - a.list.length);
  return (
    <Table head={["Package", "Worst", "Advisories", "Upgrade to"]}>
      {rows.map((r) => (
        <Fragment key={r.key}>
          <Tr onClick={() => setOpen(open === r.key ? null : r.key)} className="cursor-pointer">
            <Td className="whitespace-nowrap font-mono text-xs">{r.head.package}@{r.head.version}<div className="text-[10px] text-muted">{r.head.ecosystem}</div></Td>
            <Td><Badge tone={VSEV[r.worst]} className="capitalize">{r.worst}</Badge></Td>
            <Td className="text-sm">{r.list.length} <span className="text-xs text-muted underline">{open === r.key ? "hide" : "show"}</span></Td>
            <Td className="whitespace-nowrap font-mono text-xs">{r.upgrade ? `≥ ${r.upgrade}` : "no full fix yet"}</Td>
          </Tr>
          {open === r.key && r.list.map((v) => (
            <Tr key={r.key + v.id}>
              <Td />
              <Td><Badge tone={VSEV[v.severity]} className="capitalize">{v.severity}</Badge></Td>
              <Td>
                <div className="text-sm">{v.summary}</div>
                <a href={v.url} target="_blank" rel="noreferrer" className="text-[11px] text-muted underline">{[v.id, ...(v.aliases ?? [])].join(" · ")}</a>
              </Td>
              <Td className="whitespace-nowrap font-mono text-xs">{v.fixed ?? "no fix yet"}</Td>
            </Tr>
          ))}
        </Fragment>
      ))}
    </Table>
  );
}

/* ---------------- Firewall (server tab) ---------------- */

interface Rule {
  num: number;
  to: string;
  action: string;
  direction?: string;
  from: string;
  v6: boolean;
  raw: string;
}
interface FirewallState {
  report: { at: number; tool: string; active: boolean; defaultIncoming?: string; rules: Rule[]; raw?: string; sshPort: number } | null;
  scanning: boolean;
  changing?: string;
  last?: { ok: boolean; output?: string; error?: string };
}
interface PortRow {
  proto: string;
  bind: string;
  port: number;
  process: string;
  appName?: string;
}
const PROFILES: Record<string, number[]> = { OpenSSH: [22], "Nginx Full": [80, 443], "Nginx HTTP": [80], "Nginx HTTPS": [443], "Apache Full": [80, 443], Apache: [80], "Apache Secure": [443], WWW: [80], "WWW Secure": [443], "WWW Full": [80, 443] };

// ruleCovers: does a ufw "To" (22/tcp, 80,443/tcp, 6000:6100/udp, Nginx Full) include this port?
function ruleCovers(to: string, port: number, proto: string) {
  const t = to.replace(" (v6)", "").trim();
  if (PROFILES[t]) return PROFILES[t].includes(port);
  const [ports, p] = t.split("/");
  if (p && p !== proto.toLowerCase()) return false;
  return ports.split(",").some((x) => {
    const [a, b] = x.split(":").map(Number);
    return b ? port >= a && port <= b : port === a;
  });
}

export function ServerFirewall() {
  const { id = "" } = useParams();
  const { subscribeLive, toast } = useStore();
  const { owner } = useOwner();
  const st = useApi<FirewallState>(`/api/servers/${id}/firewall`);
  const ports = useApi<PortRow[]>(`/api/servers/${id}/ports`);
  const { setData } = st;
  const [add, setAdd] = useState<{ action: string; port: string; proto: string; from: string } | null>(null);
  const [confirm, setConfirm] = useState<{ action: string; rule?: string; title: string; message: string } | null>(null);
  useEffect(() => {
    api("POST", `/api/servers/${id}/firewall/scan`).catch(() => {});
  }, [id]);
  useEffect(() => subscribeLive((m) => {
    if (m.type !== "firewall" || m.data.serverId !== id) return;
    setData((prev) => {
      const next = m.data.state as FirewallState;
      if (prev?.changing && !next.changing && next.last) toast(next.last.ok ? "Firewall changed" : `Firewall change failed: ${next.last.error}`, next.last.ok ? "ok" : "err");
      return next;
    });
  }), [subscribeLive, setData, id, toast]);
  const change = async (body: object) => {
    try {
      await api("POST", `/api/servers/${id}/firewall/change`, body);
    } catch (e) {
      fail(toast)(e);
    }
  };
  if (st.error) return <ErrorState message={st.error} onRetry={st.reload} />;
  const rep = st.data?.report;
  if (!rep) return st.data?.scanning || !st.data ? <LoadingState label="Reading the firewall…" /> : <EmptyState title="No firewall information" desc="Needs agent v0.9.38 or newer." />;
  const ufw = rep.tool === "ufw";
  const publicPorts = (ports.data ?? []).filter((p) => p.bind === "0.0.0.0" || p.bind === "::" || p.bind === "*");
  const exposure = (p: PortRow): [Tone, string] => {
    if (!ufw || !rep.active) return rep.tool === "none" || (ufw && !rep.active) ? ["crit", "Open to the internet"] : ["neutral", "See the rules"];
    const rule = rep.rules.find((r) => r.direction !== "OUT" && ruleCovers(r.to, p.port, p.proto) && (r.from.startsWith("Anywhere")));
    if (rule) return rule.action === "ALLOW" || rule.action === "LIMIT" ? ["warn", `Open to the internet (${rule.action.toLowerCase()} rule)`] : ["ok", "Blocked by a rule"];
    if (rep.rules.some((r) => ruleCovers(r.to, p.port, p.proto))) return ["info", "Open to some addresses"];
    return rep.defaultIncoming === "allow" ? ["crit", "Open (default allow)"] : ["ok", "Blocked (default deny)"];
  };
  const busy = !!st.data?.changing;
  return (
    <div className="space-y-4">
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div className="flex items-center gap-2 font-semibold">
              {rep.tool === "none" ? "No firewall found" : rep.tool}
              {rep.tool !== "none" && <Badge tone={rep.active ? "ok" : "crit"} dot>{rep.active ? "on" : "off"}</Badge>}
              {busy && <Spinner size={14} />}
            </div>
            <div className="text-xs text-muted">{ufw && rep.defaultIncoming ? `Incoming by default: ${rep.defaultIncoming} · ` : ""}SSH on port {rep.sshPort} · read {relTime(rep.at)}</div>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="ghost" onClick={() => api("POST", `/api/servers/${id}/firewall/scan`).catch(fail(toast))}><RefreshCw size={14} /></Button>
            {owner && ufw && <Button size="sm" disabled={busy} onClick={() => setAdd({ action: "allow", port: "", proto: "tcp", from: "" })}><Plus size={14} /> Rule</Button>}
            {owner && ufw && (rep.active ? (
              <Button size="sm" variant="secondary" disabled={busy} onClick={() => setConfirm({ action: "disable", title: "Turn the firewall off?", message: "Every program listening on a public address becomes reachable from the internet." })}>Turn off</Button>
            ) : (
              <Button size="sm" disabled={busy} onClick={() => setConfirm({ action: "enable", title: "Turn the firewall on?", message: `SSH (port ${rep.sshPort}) is allowed first so you cannot lock yourself out. Anything else without an allow rule stops being reachable.` })}>Turn on</Button>
            ))}
          </div>
        </div>
        {!ufw && rep.tool !== "none" && <p className="mt-3 text-sm text-muted">Rules can be changed from here only with ufw. This is the current configuration:</p>}
        {rep.tool === "none" && <p className="mt-3 text-sm text-muted">Install ufw to manage the firewall from here: <code className="font-mono">sudo apt install ufw</code></p>}
        {rep.raw && <pre className="mt-3 max-h-80 overflow-auto whitespace-pre-wrap rounded-xl bg-surface2 p-3 font-mono text-[11px]">{rep.raw}</pre>}
      </Card>
      {ufw && (
        <Card pad={false}>
          <div className="p-5 pb-2"><SectionTitle icon={<ListOrdered size={15} />} title={`Rules (${rep.rules.length})`} /></div>
          {rep.rules.length === 0 ? <p className="px-5 pb-5 text-sm text-muted">No rules.</p> : (
            <Table head={["#", "Port", "Action", "From", ""]}>
              {rep.rules.map((r) => (
                <Tr key={r.raw}>
                  <Td className="text-xs text-muted">{r.num}</Td>
                  <Td className="font-mono text-xs">{r.to}</Td>
                  <Td><Badge tone={r.action === "ALLOW" ? "ok" : r.action === "LIMIT" ? "info" : "crit"}>{r.action.toLowerCase()}{r.direction && r.direction !== "IN" ? ` ${r.direction.toLowerCase()}` : ""}</Badge></Td>
                  <Td className="font-mono text-xs">{r.from}</Td>
                  <Td className="text-right">{owner && <Button size="sm" variant="ghost" disabled={busy} title="Delete" onClick={() => setConfirm({ action: "delete", rule: r.raw, title: "Delete this rule?", message: r.raw })}><Trash2 size={14} /></Button>}</Td>
                </Tr>
              ))}
            </Table>
          )}
        </Card>
      )}
      <Card pad={false}>
        <div className="p-5 pb-2"><SectionTitle icon={<Globe size={15} />} title="Which ports are open to the internet?" sub="Programs listening on a public address, checked against the firewall." /></div>
        {publicPorts.length === 0 ? <p className="px-5 pb-5 text-sm text-muted">Nothing listens on a public address.</p> : (
          <Table head={["Port", "Program", "Reachable"]}>
            {publicPorts.map((p) => {
              const [tone, label] = exposure(p);
              return (
                <Tr key={`${p.proto}${p.bind}${p.port}`}>
                  <Td className="font-mono text-xs">{p.port}/{p.proto.toLowerCase()}</Td>
                  <Td className="text-sm">{p.appName ?? p.process ?? "?"}</Td>
                  <Td><Badge tone={tone}>{label}</Badge></Td>
                </Tr>
              );
            })}
          </Table>
        )}
      </Card>
      {add && (
        <Modal open onClose={() => setAdd(null)} title="Add a firewall rule"
          footer={<><Button variant="ghost" onClick={() => setAdd(null)}>Cancel</Button><Button disabled={!add.port} onClick={() => { change(add); setAdd(null); }}>Add rule</Button></>}>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Action"><Select value={add.action} onChange={(e) => setAdd({ ...add, action: e.target.value })}>
              <option value="allow">Allow</option><option value="deny">Deny</option><option value="limit">Limit (slows down repeated connections)</option>
            </Select></Field>
            <Field label="Port" hint="e.g. 8080 or 6000:6100"><Input value={add.port} onChange={(e) => setAdd({ ...add, port: e.target.value })} /></Field>
            <Field label="Protocol"><Select value={add.proto} onChange={(e) => setAdd({ ...add, proto: e.target.value })}>
              <option value="tcp">TCP</option><option value="udp">UDP</option><option value="">Both</option>
            </Select></Field>
            <Field label="From" hint="Empty = anywhere"><Input value={add.from} onChange={(e) => setAdd({ ...add, from: e.target.value })} placeholder="203.0.113.4" /></Field>
          </div>
        </Modal>
      )}
      <ConfirmDialog open={!!confirm} title={confirm?.title ?? ""} message={confirm?.message ?? ""} confirmLabel="Continue" danger={confirm?.action === "disable" || confirm?.action === "delete"}
        onConfirm={() => { if (confirm) change({ action: confirm.action, rule: confirm.rule }); setConfirm(null); }} onCancel={() => setConfirm(null)} />
    </div>
  );
}
