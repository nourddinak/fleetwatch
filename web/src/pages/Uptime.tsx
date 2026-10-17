import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, AlertTriangle, CheckCircle2, CircleArrowDown, Clock, Gauge, Globe, Link2, MapPin, PauseCircle, Pencil, Plus, RefreshCw, Server as ServerIcon, Snail, Sparkles, Timer, Trash2, Zap } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import type { Range } from "../mock/types";
import { dateTime, relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, Drawer, EmptyState, ErrorState, Field, Gate, Input, LoadingState, Modal, PageHeader, SectionTitle, Table, Td, TimeRangeSelector, Tr, toneDot, type Tone } from "../components/ui";
import { MetricPanel } from "../components/MetricChart";
import { cn } from "../utils/cn";

interface Check {
  id: string;
  name: string;
  url: string;
  intervalSec: number;
  timeoutMs: number;
  expectedStatus: number;
  slowMs: number;
  locations: string[];
  serverId?: string;
  enabled: boolean;
  createdAt: number;
}
interface State {
  location: string;
  status: "up" | "down" | "unknown";
  ts?: number;
  code?: number;
  latencyMs?: number;
  error?: string;
}
interface View extends Check {
  status: "up" | "down" | "partial" | "slow" | "paused" | "unknown";
  states: State[];
  uptime24h: number;
  uptime7d: number;
  uptime30d: number;
  p50: number;
  p95: number;
  days: { day: number; uptime: number }[];
}
interface History {
  points: { t: number; p50: number; p95: number; total: number; fails: number }[];
  locations: { location: string; p50: number; p95: number; uptime: number; checks: number }[];
  failures: { location: string; ts: number; status: number; latencyMs: number; error?: string }[];
}
interface Suggestion {
  name: string;
  url: string;
  serverId: string;
}

const STATUS: Record<View["status"], [Tone, string]> = {
  up: ["ok", "Up"],
  down: ["crit", "Down"],
  partial: ["warn", "Down in some places"],
  slow: ["warn", "Slow"],
  paused: ["neutral", "Paused"],
  unknown: ["neutral", "Waiting for results"],
};
const pct = (v: number) => (v < 0 ? "—" : `${v >= 99.995 ? "100" : v.toFixed(2)}%`);
const ms = (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(1)} s` : `${v} ms`);
const barTone = (v: number) => (v < 0 ? "bg-line" : v >= 99.9 ? "bg-ok" : v >= 98 ? "bg-warn" : "bg-crit");

export function Uptime() {
  const { me, subscribeLive, toast } = useStore();
  const owner = !me || me.role === "owner";
  const list = useApi<View[]>("/api/uptime");
  const { setData } = list;
  const [edit, setEdit] = useState<Partial<Check> | null>(null);
  const [del, setDel] = useState<View | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  useEffect(
    () =>
      subscribeLive((m) => {
        if (m.type === "uptime") setData((l) => (l ? (l.some((x) => x.id === m.data.id) ? l.map((x) => (x.id === m.data.id ? m.data : x)) : [...l, m.data]) : l));
        if (m.type === "uptime.removed") setData((l) => l?.filter((x) => x.id !== m.data.id) ?? l);
      }),
    [subscribeLive, setData],
  );
  const checkNow = async (id: string) => {
    try {
      await api("POST", `/api/uptime/${id}/check`);
      toast("Checking now");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const remove = async () => {
    if (!del) return;
    try {
      await api("DELETE", `/api/uptime/${del.id}`);
      setData((l) => l?.filter((x) => x.id !== del.id) ?? l);
      toast("Check deleted");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setDel(null);
  };
  const count = (s: View["status"][]) => list.data?.filter((v) => s.includes(v.status)).length ?? 0;
  return (
    <>
      <PageHeader
        title="Uptime"
        sub="Checks your sites from the outside, from the dashboard and from your other servers, so “the server is fine but the site is down from over there” shows up. Response times, slow alerts and 30 days of history."
        actions={owner && <Button onClick={() => setEdit({ locations: ["dashboard"], enabled: true })}><Plus size={15} /> Add check</Button>}
      />
      <Gate>
        {list.error ? (
          <ErrorState message={list.error} onRetry={list.reload} />
        ) : !list.data ? (
          <LoadingState />
        ) : (
          <div className="space-y-4">
            {list.data.length > 0 && (
              <div className="flex flex-wrap gap-2 text-xs">
                {([
                  [CheckCircle2, `${count(["up"])} up`, count(["up"]) >= 0, ""],
                  [CircleArrowDown, `${count(["down", "partial"])} down`, count(["down", "partial"]) > 0, "border-crit/40 text-crit"],
                  [Snail, `${count(["slow"])} slow`, count(["slow"]) > 0, "border-warn/40 text-warn"],
                  [PauseCircle, `${count(["paused"])} paused`, count(["paused"]) > 0, ""],
                ] as const).map(([Icon, text, show, cls]) => show && (
                  <span key={text} className={cn("inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-muted", cls)}><Icon size={13} /> {text}</span>
                ))}
              </div>
            )}
            {owner && <Suggestions onAdd={(s) => setEdit({ name: s.name, url: s.url, serverId: s.serverId, locations: ["dashboard"], enabled: true })} known={list.data.length} />}
            {list.data.length === 0 ? (
              <EmptyState icon={<Gauge size={20} />} title="No uptime checks yet" desc="Add a site's address; it is checked every minute from the places you pick." />
            ) : (
              list.data.map((v) => <CheckCard key={v.id} v={v} owner={owner} onOpen={() => setOpen(v.id)} onCheck={() => checkNow(v.id)} onEdit={() => setEdit(v)} onDelete={() => setDel(v)} />)
            )}
          </div>
        )}
      </Gate>
      {edit && (
        <EditCheck
          check={edit}
          onClose={() => setEdit(null)}
          onSaved={(v) => {
            setData((l) => (l ? (l.some((x) => x.id === v.id) ? l.map((x) => (x.id === v.id ? v : x)) : [...l, v]) : [v]));
            setEdit(null);
          }}
        />
      )}
      <ConfirmDialog open={!!del} title="Delete this check?" message={`${del?.name}: its 30 days of history are deleted too.`} confirmLabel="Delete" danger onConfirm={remove} onCancel={() => setDel(null)} />
      <CheckDrawer view={list.data?.find((v) => v.id === open) ?? null} onClose={() => setOpen(null)} />
    </>
  );
}

function locName(l: string, name: (id: string) => string | undefined) {
  return l === "dashboard" ? "Dashboard" : (name(l) ?? "Removed server");
}

function CheckCard({ v, owner, onOpen, onCheck, onEdit, onDelete }: { v: View; owner: boolean; onOpen: () => void; onCheck: () => void; onEdit: () => void; onDelete: () => void }) {
  const { getServer } = useStore();
  const [tone, label] = STATUS[v.status];
  const name = (id: string) => getServer(id)?.name;
  return (
    <Card className="transition hover:border-fg/25">
      <div className="flex items-start gap-3">
        <button onClick={onOpen} className="relative grid h-11 w-11 shrink-0 place-items-center rounded-2xl border border-line bg-surface2 text-fg" aria-label={`Open ${v.name}`}>
          <Globe size={18} />
          <span className={cn("absolute -right-0.5 -top-0.5 h-3 w-3 rounded-full border-2 border-surface", toneDot[tone], v.status === "down" && "pulse-dot")} />
        </button>
        <button onClick={onOpen} className="min-w-0 flex-1 text-left">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[15px] font-semibold tracking-tight hover:underline">{v.name}</span>
            <Badge tone={tone} dot pulse={v.status === "down"}>{label}</Badge>
          </div>
          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted">
            <span className="inline-flex min-w-0 items-center gap-1.5"><Link2 size={12} className="shrink-0" /><span className="truncate">{v.url}</span></span>
            <span className="inline-flex items-center gap-1.5"><Timer size={12} />every {v.intervalSec < 60 ? `${v.intervalSec}s` : `${v.intervalSec / 60} min`}</span>
            {v.slowMs > 0 && <span className="inline-flex items-center gap-1.5"><Snail size={12} />slow above {ms(v.slowMs)}</span>}
          </div>
        </button>
        <div className="flex shrink-0 items-center gap-0.5">
          {([
            [RefreshCw, "Check now", onCheck, true, !v.enabled],
            [Pencil, "Edit", onEdit, owner, false],
            [Trash2, "Delete", onDelete, owner, false],
          ] as const).map(([Icon, t, fn, show, off]) => show && (
            <button key={t} onClick={fn} disabled={off} title={t} aria-label={t} className="grid h-9 w-9 place-items-center rounded-full text-muted transition hover:bg-surface2 hover:text-fg disabled:opacity-40"><Icon size={15} /></button>
          ))}
        </div>
      </div>
      <div className="mt-4 grid gap-4 md:grid-cols-[1fr_auto]">
        <div>
          <div className="flex h-8 items-end gap-[3px]" aria-label="Uptime per day, last 30 days">
            {v.days.map((d) => (
              <span key={d.day} title={`${new Date(d.day).toLocaleDateString()}: ${pct(d.uptime)}`} className={cn("h-full min-w-0 flex-1 rounded-sm", barTone(d.uptime))} />
            ))}
          </div>
          <div className="mt-1 flex justify-between text-[11px] text-muted"><span>30 days ago</span><span>{pct(v.uptime30d)} uptime</span><span>today</span></div>
        </div>
        <div className="grid grid-cols-3 gap-2">
          {([
            [Activity, "24 h", pct(v.uptime24h), ""],
            [Gauge, "median", v.p50 ? ms(v.p50) : "—", ""],
            [Zap, "p95", v.p95 ? ms(v.p95) : "—", v.slowMs && v.p95 > v.slowMs ? "text-warn" : ""],
          ] as const).map(([Icon, l, val, cls]) => (
            <div key={l} className="rounded-xl bg-surface2/60 px-3 py-2">
              <div className="flex items-center gap-1 text-[11px] text-muted"><Icon size={11} />{l}</div>
              <div className={cn("text-sm font-semibold tabular-nums", cls)}>{val}</div>
            </div>
          ))}
        </div>
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        {v.states.map((s) => (
          <span key={s.location} title={s.error || (s.ts ? `HTTP ${s.code} · ${relTime(s.ts)}` : "No result yet")}
            className={cn("inline-flex items-center gap-1.5 rounded-full border border-line px-2.5 py-1 text-xs", s.status === "down" && "border-crit/40 text-crit")}>
            <span className={cn("h-1.5 w-1.5 rounded-full", s.status === "up" ? "bg-ok" : s.status === "down" ? "bg-crit" : "bg-muted")} />
            {s.location === "dashboard" ? <MapPin size={11} className="text-muted" /> : <ServerIcon size={11} className="text-muted" />}
            {locName(s.location, name)}
            <span className="text-muted">{s.status === "up" ? ms(s.latencyMs ?? 0) : s.status === "down" ? (s.code ? `HTTP ${s.code}` : "no answer") : "…"}</span>
          </span>
        ))}
      </div>
    </Card>
  );
}

function Suggestions({ onAdd, known }: { onAdd: (s: Suggestion) => void; known: number }) {
  const { data } = useApi<Suggestion[]>(`/api/uptime/suggestions?n=${known}`);
  if (!data?.length) return null;
  return (
    <Card>
      <SectionTitle icon={<Sparkles size={15} />} title="Your sites without a check" sub="Found in Caddy and on your apps. One click to watch them." />
      <div className="flex flex-wrap gap-2">
        {data.slice(0, 12).map((s) => (
          <Button key={s.url} size="sm" variant="secondary" onClick={() => onAdd(s)}><Plus size={13} /> {s.name}</Button>
        ))}
      </div>
    </Card>
  );
}

function EditCheck({ check, onClose, onSaved }: { check: Partial<Check>; onClose: () => void; onSaved: (v: View) => void }) {
  const { servers, toast } = useStore();
  const [f, setF] = useState({
    name: check.name ?? "",
    url: check.url ?? "https://",
    intervalSec: check.intervalSec ?? 60,
    timeoutMs: check.timeoutMs ?? 10000,
    expectedStatus: check.expectedStatus ?? 200,
    slowMs: check.slowMs ?? 0,
    locations: check.locations ?? ["dashboard"],
    serverId: check.serverId ?? "",
    enabled: check.enabled ?? true,
  });
  const [busy, setBusy] = useState(false);
  const toggle = (l: string) => setF({ ...f, locations: f.locations.includes(l) ? f.locations.filter((x) => x !== l) : [...f.locations, l] });
  const save = async () => {
    setBusy(true);
    try {
      const body = { ...f, serverId: f.serverId || undefined };
      onSaved(check.id ? await api<View>("PUT", `/api/uptime/${check.id}`, body) : await api<View>("POST", "/api/uptime", body));
      toast(check.id ? "Check saved" : "Check added: first results in a few seconds");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <Modal open onClose={onClose} title={check.id ? "Edit uptime check" : "Add uptime check"}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button onClick={save} disabled={busy || !f.url}>{check.id ? "Save" : "Add check"}</Button></>}>
      <div className="space-y-4">
        <Field label="Address"><Input value={f.url} onChange={(e) => setF({ ...f, url: e.target.value })} placeholder="https://example.com/health" /></Field>
        <Field label="Name" hint="Shown in alerts. Empty: the domain."><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Check every (seconds)"><Input type="number" min={30} value={f.intervalSec} onChange={(e) => setF({ ...f, intervalSec: Number(e.target.value) })} /></Field>
          <Field label="Expected status"><Input type="number" value={f.expectedStatus} onChange={(e) => setF({ ...f, expectedStatus: Number(e.target.value) })} /></Field>
          <Field label="Timeout (ms)"><Input type="number" value={f.timeoutMs} onChange={(e) => setF({ ...f, timeoutMs: Number(e.target.value) })} /></Field>
          <Field label="Slow above (ms)" hint="0 = no slow alert"><Input type="number" min={0} value={f.slowMs} onChange={(e) => setF({ ...f, slowMs: Number(e.target.value) })} /></Field>
        </div>
        <Field label="Check from" hint="Pick servers in other places than the one hosting the site: they see what visitors there see.">
          <div className="flex flex-wrap gap-2">
            {["dashboard", ...servers.filter((s) => s.enrolled).map((s) => s.id)].map((l) => (
              <button key={l} type="button" onClick={() => toggle(l)}
                className={cn("rounded-full border px-3 py-1.5 text-xs", f.locations.includes(l) ? "border-transparent bg-accent text-accent-fg" : "border-line text-muted hover:text-fg")}>
                {l === "dashboard" ? "Dashboard" : servers.find((s) => s.id === l)?.name}
              </button>
            ))}
          </div>
        </Field>
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={f.enabled} onChange={(e) => setF({ ...f, enabled: e.target.checked })} /> Checking (untick to pause)</label>
      </div>
    </Modal>
  );
}

function CheckDrawer({ view, onClose }: { view: View | null; onClose: () => void }) {
  const { getServer } = useStore();
  const [range, setRange] = useState<Range>("24h");
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setTick((n) => n + 1), 60_000); // fresh charts every minute, not on every result
    return () => clearInterval(t);
  }, []);
  const h = useApi<History>(view ? `/api/uptime/${view.id}/history?range=${range}&t=${tick}` : "/api/health");
  const data = view && h.data && "points" in h.data ? h.data : null;
  return (
    <Drawer open={!!view} onClose={onClose} title={<span className="flex items-center gap-3"><span className="grid h-9 w-9 place-items-center rounded-xl border border-line bg-surface2"><Globe size={16} /></span>{view?.name ?? ""}</span>}>
      {view && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <a href={view.url} target="_blank" rel="noreferrer" className="truncate text-sm text-muted hover:underline">{view.url}</a>
            <TimeRangeSelector value={range} onChange={setRange} />
          </div>
          {view.serverId && <p className="text-xs text-muted">Hosted on <Link className="underline" to={`/servers/${view.serverId}`}>{getServer(view.serverId)?.name ?? "a removed server"}</Link></p>}
          {!data ? (
            <LoadingState />
          ) : (
            <>
              <MetricPanel title="Response time (95% of requests)" data={data.points.map((p) => ({ t: p.t, v: p.p95 }))} unit=" ms" decimals={0} threshold={view.slowMs || undefined} />
              <MetricPanel title="Response time (median)" data={data.points.map((p) => ({ t: p.t, v: p.p50 }))} unit=" ms" decimals={0} />
              <Card pad={false}>
                <Table head={["From", "Uptime", "Median", "p95", "Checks"]}>
                  {data.locations.map((l) => (
                    <Tr key={l.location}>
                      <Td>{locName(l.location, (id) => getServer(id)?.name)}</Td>
                      <Td className={cn(l.uptime < 99 && "text-crit")}>{pct(l.uptime)}</Td>
                      <Td>{ms(l.p50)}</Td>
                      <Td>{ms(l.p95)}</Td>
                      <Td className="text-muted">{l.checks}</Td>
                    </Tr>
                  ))}
                </Table>
              </Card>
              <Card>
                <SectionTitle icon={<AlertTriangle size={15} />} title="Recent failures" sub={data.failures.length ? undefined : "None in this range."} />
                <ul className="divide-y divide-line">
                  {data.failures.map((f, i) => (
                    <li key={i} className="flex flex-wrap items-baseline gap-2 py-2 text-sm">
                      <span className="inline-flex items-center gap-1 text-xs text-muted" title={dateTime(f.ts)}><Clock size={11} />{relTime(f.ts)}</span>
                      <span className="font-medium">{locName(f.location, (id) => getServer(id)?.name)}</span>
                      <span className="min-w-0 flex-1 break-words text-muted">{f.error || `HTTP ${f.status}`}</span>
                    </li>
                  ))}
                </ul>
              </Card>
            </>
          )}
        </div>
      )}
    </Drawer>
  );
}
