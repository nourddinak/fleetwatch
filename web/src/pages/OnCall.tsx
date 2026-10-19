import { useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { AlertTriangle, ArrowDown, ArrowRight, ArrowUp, BellRing, CalendarDays, CalendarRange, CalendarX2, Check, Clock, Hand, Mail, MoonStar, PhoneCall, Plus, Siren, Smartphone, Trash2, Trophy, Users, UserRound, X } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ErrorState, Field, IconButton, Input, LoadingState, PageHeader, SectionTitle, Segmented, Select, Spinner, Textarea, Toggle, WhatsApp } from "../components/ui";
import { fmtDur } from "../mock/util";
import { cn } from "../utils/cn";

interface Person {
  id: string;
  name: string;
  email: string;
  devices: number;
}
interface Override {
  person: string;
  from: number;
  to: number;
}
interface Config {
  enabled: boolean;
  rotation: string[];
  handoverDay: number;
  handoverHour: number;
  timeZone: string;
  escalateAfterMin: number;
  severity: "critical" | "warning";
  overrides: Override[];
  start: number;
  fixedOrder?: boolean;
  daysOff?: Override[];
  quietNights?: boolean;
  quietFrom: number;
  quietTo: number;
}
interface CoverRequest {
  id: number;
  person: string;
  from: number;
  to: number;
  note: string;
  takenBy: string;
  status: "open" | "taken" | "canceled" | "past";
}
interface PersonStats {
  person: string;
  name: string;
  pages: number;
  acked: number;
  medianAckMs: number;
  missed: number;
}
interface Shift {
  person: string;
  from: number;
  to: number;
  override: boolean;
}
interface View {
  config: Config;
  people: Person[];
  now: string;
  schedule: Shift[];
  cover: CoverRequest[];
}

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const toLocal = (ms: number) => (ms ? new Date(ms - new Date(ms).getTimezoneOffset() * 60_000).toISOString().slice(0, 16) : "");
const fromLocal = (s: string) => (s ? new Date(s).getTime() : 0);
const when = (ms: number) => new Date(ms).toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
const day = (ms: number) => new Date(ms).toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" });
const hour = (h: number) => `${String(h).padStart(2, "0")}:00`;
const initials = (n: string) => n.split(/\s+/).map((w) => w[0]).join("").slice(0, 2).toUpperCase() || "?";

/** On-call: who is woken for an incident, and who next when nobody answers. */
export function OnCallPage() {
  const { toast } = useStore();
  const v = useApi<View>("/api/oncall");
  const wa = useApi<{ status: { linked: boolean }; people: { id: string; phone: string }[] }>("/api/integrations/whatsapp");
  const [c, setC] = useState<Config | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (v.data) setC(v.data.config);
  }, [v.data]);
  if (v.error) return <ErrorState message={v.error} onRetry={v.reload} />;
  if (!v.data || !c) return <LoadingState />;
  const people = v.data.people;
  const byId = (id: string) => people.find((p) => p.id === id);
  const name = (id: string) => (id === "owner" ? byId(id)?.name ?? "You" : byId(id)?.name ?? "(removed)");
  const whatsapp = (id: string) => !!wa.data?.status.linked && !!wa.data.people.find((p) => p.id === id && p.phone);
  const ways = (id: string) => {
    const p = byId(id);
    return { app: (p?.devices ?? 0) > 0, email: !!p?.email, whatsapp: whatsapp(id) };
  };
  const reachable = (id: string) => Object.values(ways(id)).some(Boolean);
  const dirty = JSON.stringify(c) !== JSON.stringify(v.data.config);
  const set = (p: Partial<Config>) => setC({ ...c, ...p });
  const save = async () => {
    setBusy(true);
    try {
      v.setData(await api<View>("PUT", "/api/oncall", c));
      toast("On-call saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  // Who gets paged, in order: whoever is on call, the rest of the rotation, then the owner.
  const first = v.data.now && c.rotation.includes(v.data.now) ? v.data.now : c.rotation[0];
  const at = Math.max(0, c.rotation.indexOf(first));
  const chain = [...c.rotation.slice(at), ...c.rotation.slice(0, at)];
  if (!chain.includes("owner")) chain.push("owner");
  const current = v.data.schedule?.[0];
  const next = v.data.schedule?.find((s) => s.person !== v.data!.now && s.from > Date.now());
  const fresh = c.rotation.length === 0;
  const weekly = !c.fixedOrder;
  // With weekly shifts off (as saved), a shift that runs to the end of the schedule has no real end.
  const sched = v.data.schedule ?? [];
  const endless = (s: Shift) => !!v.data!.config.fixedOrder && !s.override && s === sched[sched.length - 1];

  return (
    <div className="pb-24">
      <PageHeader title="On-call" sub="Who is woken up when something breaks, and who is next when nobody answers."
        badge={<Badge tone={v.data.config.enabled ? "ok" : undefined} dot>{v.data.config.enabled ? "Paging on" : "Paging off"}</Badge>}
        actions={<label className="flex items-center gap-2 text-sm font-medium">Page the person on call <Toggle checked={c.enabled} onChange={(on) => set({ enabled: on })} /></label>} />

      {fresh && <GettingStarted />}

      <div className="grid gap-6 xl:grid-cols-3">
        <div className="space-y-6 xl:col-span-2">
          {/* who now */}
          <Card>
            <SectionTitle icon={<PhoneCall size={15} />} title="On call now" />
            {v.data.config.enabled && v.data.now ? (
              <div className="flex flex-wrap items-center gap-4">
                <span className="grid h-14 w-14 place-items-center rounded-2xl bg-accent text-lg font-semibold text-accent-fg">{initials(name(v.data.now))}</span>
                <div className="min-w-0 flex-1">
                  <div className="text-xl font-semibold">{name(v.data.now)}</div>
                  <div className="text-sm text-muted">{current ? (endless(current) ? "always on call, no weekly shifts" : `until ${when(current.to)}`) : ""}{next ? ` · next: ${name(next.person)}` : ""}</div>
                  <Reach {...ways(v.data.now)} />
                </div>
              </div>
            ) : (
              <p className="text-sm text-muted">{fresh ? "Nobody yet: add the people who take turns below." : "Paging is off: incidents only reach your notification channels. Turn on Page the person on call above."}</p>
            )}
          </Card>

          {/* what happens */}
          <Card>
            <SectionTitle icon={<Siren size={15} />} title="What happens during an incident" sub={`For ${c.severity === "critical" ? "critical incidents" : "warnings and critical incidents"}, while paging is on.`} />
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="inline-flex items-center gap-1.5 rounded-full bg-crit-soft px-3 py-1.5 font-medium text-crit"><BellRing size={13} /> Incident</span>
              {chain.map((id, i) => (
                <span key={id} className="contents">
                  <span className="inline-flex items-center gap-1 text-xs text-muted">
                    <ArrowRight size={13} />{i > 0 && <><Clock size={11} /> {c.escalateAfterMin} min</>}
                  </span>
                  <span className={cn("inline-flex items-center gap-1.5 rounded-full border px-3 py-1.5", reachable(id) ? "border-line" : "border-warn text-warn")} title={reachable(id) ? "" : "Cannot be reached yet"}>
                    <span className="grid h-5 w-5 place-items-center rounded-full bg-surface2 text-[10px] font-semibold">{initials(name(id))}</span>
                    {id === "owner" ? `${name(id)} (owner)` : name(id)}
                    {!reachable(id) && <AlertTriangle size={12} />}
                  </span>
                </span>
              ))}
            </div>
            <ol className="mt-4 space-y-1.5 text-xs text-muted">
              <li><b className="text-fg">1.</b> The person on call is paged: the phone app rings, an email{wa.data?.status.linked ? ", and a WhatsApp call with the AI" : ""}.</li>
              <li><b className="text-fg">2.</b> They tap <b className="text-fg">I'm on it</b> (or say it on the call): the incident is acknowledged and the paging stops.</li>
              <li><b className="text-fg">3.</b> Nobody answers within {c.escalateAfterMin} minutes? The next person is paged, and so on, finally you. Maintenance mode pauses all of it.</li>
            </ol>
          </Card>

          {/* step 1 */}
          <Step n={1} title="Who takes turns" sub={weekly ? "Each person is on call for a week, in this order." : "The first person is always on call; the others are paged in this order if nobody answers."}>
            <div className="space-y-2">
              {c.rotation.length === 0 && <p className="text-sm text-muted">Nobody yet.</p>}
              {c.rotation.map((id, i) => (
                <div key={id} className="flex items-center gap-3 rounded-2xl border border-line px-3 py-2.5">
                  <span className="grid h-8 w-8 shrink-0 place-items-center rounded-xl bg-surface2 text-xs font-semibold">{initials(name(id))}</span>
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium">{name(id)} <span className="text-xs font-normal text-muted">· {weekly ? `week ${i + 1}` : i === 0 ? "always on call" : `backup ${i}`}</span></div>
                    <Reach {...ways(id)} />
                  </div>
                  <IconButton aria-label="Earlier" disabled={i === 0} onClick={() => set({ rotation: swap(c.rotation, i, i - 1) })}><ArrowUp size={14} /></IconButton>
                  <IconButton aria-label="Later" disabled={i === c.rotation.length - 1} onClick={() => set({ rotation: swap(c.rotation, i, i + 1) })}><ArrowDown size={14} /></IconButton>
                  <IconButton aria-label="Remove" onClick={() => set({ rotation: c.rotation.filter((x) => x !== id) })}><Trash2 size={14} /></IconButton>
                </div>
              ))}
              <div className="flex flex-wrap items-center gap-2">
                <Select className="max-w-xs" value="" onChange={(e) => e.target.value && set({ rotation: [...c.rotation, e.target.value] })}>
                  <option value="">+ Add a person…</option>
                  {people.filter((p) => !c.rotation.includes(p.id)).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                </Select>
                <Link to="/team" className="text-xs text-muted underline">Invite someone to the team</Link>
              </div>
              {c.rotation.some((id) => !reachable(id)) && (
                <p className="flex items-start gap-2 rounded-xl bg-warn-soft p-3 text-xs text-warn"><AlertTriangle size={13} className="mt-px shrink-0" />
                  <span>Someone cannot be reached yet. They can turn on notifications on their phone (Settings → Account), add an email, or get a WhatsApp number in <Link to="/integrations?tab=whatsapp" className="underline">Integrations → WhatsApp</Link>.</span></p>
              )}
            </div>
          </Step>

          {/* step 2 */}
          <Step n={2} title="When shifts change" sub={weekly ? `Every ${DAYS[c.handoverDay]} at ${hour(c.handoverHour)} (${c.timeZone || "UTC"}), the next person takes over.` : "Shifts don't change: the first person in the list stays on call. Cover and swaps still apply."}>
            <label className="flex items-center justify-between gap-3 text-sm font-medium">Change shifts every week <Toggle checked={weekly} onChange={(on) => set({ fixedOrder: !on })} /></label>
            {weekly && <>
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Day">
                <Select value={c.handoverDay} onChange={(e) => set({ handoverDay: Number(e.target.value) })}>
                  {DAYS.map((d, i) => <option key={d} value={i}>{d}</option>)}
                </Select>
              </Field>
              <Field label="Time">
                <Select value={c.handoverHour} onChange={(e) => set({ handoverHour: Number(e.target.value) })}>
                  {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{hour(h)}</option>)}
                </Select>
              </Field>
              <Field label="Time zone" hint={<button className="underline" onClick={() => set({ timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone })}>Use mine</button>}>
                <Input value={c.timeZone} placeholder="Africa/Casablanca" onChange={(e) => set({ timeZone: e.target.value })} />
              </Field>
            </div>
            <p className="text-xs text-muted">The person coming on call gets a message at handover, with the note the last one left.</p>
            </>}
          </Step>

          {/* step 3 */}
          <Step n={3} title="What wakes them, and how fast it moves on"
            sub={`${c.severity === "critical" ? "Only critical incidents" : "Warnings and critical incidents"} page; after ${c.escalateAfterMin} minutes without "I'm on it", the next person.`}>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Page for">
                <Segmented value={c.severity} onChange={(val) => set({ severity: val })} options={[{ value: "critical", label: "Critical only" }, { value: "warning", label: "Warnings too" }]} />
              </Field>
              <Field label="Next person after" hint="Minutes without an acknowledgement.">
                <Select value={c.escalateAfterMin} onChange={(e) => set({ escalateAfterMin: Number(e.target.value) })}>
                  {[...new Set([2, 5, 10, 15, 20, 30, 45, 60, c.escalateAfterMin])].sort((a, b) => a - b).map((m) => <option key={m} value={m}>{m} minutes</option>)}
                </Select>
              </Field>
            </div>
            <label className="flex items-center justify-between gap-3 border-t border-line pt-3 text-sm font-medium">
              <span className="flex items-center gap-2"><MoonStar size={14} /> Quiet nights</span>
              <Toggle checked={!!c.quietNights} onChange={(on) => set({ quietNights: on })} />
            </label>
            <p className="text-xs text-muted">{c.quietNights ? `Warnings opened between ${hour(c.quietFrom)} and ${hour(c.quietTo)} (${c.timeZone || "UTC"}) wait until ${hour(c.quietTo)} to page whoever is on call then. Critical incidents still wake people.` : "Off: warnings page at any hour (when they page at all)."}</p>
            {c.quietNights && (
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="From"><Select value={c.quietFrom} onChange={(e) => set({ quietFrom: Number(e.target.value) })}>{Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{hour(h)}</option>)}</Select></Field>
                <Field label="Until"><Select value={c.quietTo} onChange={(e) => set({ quietTo: Number(e.target.value) })}>{Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{hour(h)}</option>)}</Select></Field>
              </div>
            )}
          </Step>
        </div>

        <div className="space-y-6">
          <Periods title="Cover and swaps" sub="Someone else on call for a while: a holiday, a swap." icon={<CalendarRange size={15} />}
            list={c.overrides} people={people} name={name} pick="Who covers…" add="Add cover" onChange={(overrides) => set({ overrides })} />
          <Periods title="Days off" sub="Not on call then: the next person in the rotation takes over, and they are not paged." icon={<CalendarX2 size={15} />}
            list={c.daysOff ?? []} people={people.filter((p) => c.rotation.includes(p.id))} name={name} pick="Who is off…" add="Add days off" onChange={(daysOff) => set({ daysOff })} />
          <CoverRequests view={v.data} people={people.filter((p) => c.rotation.includes(p.id))} name={name} onChange={v.setData} />
          {(v.data.schedule?.length ?? 0) > 0 && (
            <Card>
              <SectionTitle icon={<CalendarDays size={15} />} title={v.data.config.fixedOrder ? "Schedule" : "Coming weeks"} />
              <ol className="relative space-y-3 before:absolute before:bottom-2 before:left-[15px] before:top-2 before:w-px before:bg-line">
                {v.data.schedule.map((s, i) => (
                  <li key={i} className="relative flex items-center gap-3">
                    <span className={cn("z-10 grid h-8 w-8 shrink-0 place-items-center rounded-full border-2 border-surface text-[11px] font-semibold", i === 0 ? "bg-accent text-accent-fg" : "bg-surface2")}>{initials(name(s.person))}</span>
                    <div className="min-w-0 text-sm">
                      <div className="font-medium">{name(s.person)} {s.override && <Badge tone="info">cover</Badge>}{i === 0 && <Badge tone="ok">now</Badge>}</div>
                      <div className="text-xs text-muted">{endless(s) ? `from ${day(s.from)}, ongoing` : `${day(s.from)} → ${day(s.to)}`}</div>
                    </div>
                  </li>
                ))}
              </ol>
            </Card>
          )}
          <WhoHandled />
        </div>
      </div>

      {dirty && (
        <div className="fixed inset-x-0 bottom-4 z-30 flex justify-center px-4">
          <div className="flex items-center gap-3 rounded-full border border-line bg-surface px-4 py-2 shadow-lg">
            <span className="text-sm text-muted">Unsaved changes</span>
            <Button size="sm" variant="ghost" onClick={() => setC(v.data!.config)}>Reset</Button>
            <Button size="sm" disabled={busy} onClick={save}>{busy ? <Spinner size={13} /> : <Check size={13} />} Save</Button>
          </div>
        </div>
      )}
    </div>
  );
}

function swap(list: string[], i: number, j: number) {
  const r = [...list];
  [r[i], r[j]] = [r[j], r[i]];
  return r;
}

/** How someone is reached when paged. */
function Reach({ app, email, whatsapp }: { app: boolean; email: boolean; whatsapp: boolean }) {
  if (!app && !email && !whatsapp) return <span className="mt-1 inline-flex items-center gap-1 text-xs text-warn"><AlertTriangle size={11} /> cannot be reached yet</span>;
  return (
    <span className="mt-1 flex flex-wrap gap-1.5">
      {app && <Badge><Smartphone size={11} /> phone</Badge>}
      {email && <Badge><Mail size={11} /> email</Badge>}
      {whatsapp && <Badge><WhatsApp size={11} /> WhatsApp</Badge>}
    </span>
  );
}

function Step({ n, title, sub, children }: { n: number; title: string; sub: string; children: ReactNode }) {
  return (
    <Card>
      <div className="mb-4 flex items-start gap-3">
        <span className="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-accent text-sm font-semibold text-accent-fg">{n}</span>
        <div className="min-w-0">
          <h3 className="font-semibold">{title}</h3>
          <p className="text-sm text-muted">{sub}</p>
        </div>
      </div>
      <div className="space-y-3">{children}</div>
    </Card>
  );
}

/** Stretches of time for someone: cover (they are on call) or days off (they are not). */
function Periods({ title, sub, icon, list, people, name, pick, add: addLabel, onChange }: { title: string; sub: string; icon: ReactNode; list: Override[]; people: Person[]; name: (id: string) => string; pick: string; add: string; onChange: (l: Override[]) => void }) {
  const [add, setAdd] = useState<Override>({ person: "", from: 0, to: 0 });
  return (
    <Card>
      <SectionTitle icon={icon} title={title} sub={sub} />
      <div className="space-y-2">
        {list.length === 0 && <p className="text-sm text-muted">None.</p>}
        {list.map((o, i) => (
          <div key={i} className="flex items-center gap-2 rounded-2xl border border-line px-3 py-2 text-sm">
            <UserRound size={13} className="shrink-0 text-muted" />
            <div className="min-w-0 flex-1">
              <div className="font-medium">{name(o.person)}</div>
              <div className="text-xs text-muted">{when(o.from)} → {when(o.to)}</div>
            </div>
            <IconButton aria-label="Remove" onClick={() => onChange(list.filter((_, k) => k !== i))}><Trash2 size={14} /></IconButton>
          </div>
        ))}
        <div className="space-y-2 rounded-2xl bg-surface2/60 p-3">
          <Select value={add.person} onChange={(e) => setAdd({ ...add, person: e.target.value })}>
            <option value="">{pick}</option>
            {people.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </Select>
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-1">
            <Field label="From"><Input type="datetime-local" value={toLocal(add.from)} onChange={(e) => setAdd({ ...add, from: fromLocal(e.target.value) })} /></Field>
            <Field label="Until"><Input type="datetime-local" value={toLocal(add.to)} onChange={(e) => setAdd({ ...add, to: fromLocal(e.target.value) })} /></Field>
          </div>
          <Button size="sm" variant="secondary" disabled={!add.person || !add.from || add.to <= add.from} onClick={() => (onChange([...list, add]), setAdd({ person: "", from: 0, to: 0 }))}>
            <Plus size={14} /> {addLabel}
          </Button>
        </div>
      </div>
    </Card>
  );
}

/** "Can someone cover me Saturday?": asked here or on WhatsApp; the first yes on WhatsApp takes it. */
function CoverRequests({ view, people, name, onChange }: { view: View; people: Person[]; name: (id: string) => string; onChange: (v: View) => void }) {
  const { toast } = useStore();
  const [ask, setAsk] = useState({ person: "", from: 0, to: 0, note: "" });
  const [busy, setBusy] = useState(false);
  const list = (view.cover ?? []).filter((r) => r.status === "open" || r.status === "taken").slice(0, 6);
  const run = async (fn: () => Promise<View>, done: string) => {
    setBusy(true);
    try {
      onChange(await fn());
      toast(done);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <Card>
      <SectionTitle icon={<Hand size={15} />} title="Cover requests" sub="Ask the rotation to cover someone: they get a WhatsApp message, and the first to answer “I'll cover” is put on call. Anyone can also ask the AI on WhatsApp." />
      <div className="space-y-2">
        {list.length === 0 && <p className="text-sm text-muted">None open.</p>}
        {list.map((r) => (
          <div key={r.id} className="flex items-center gap-2 rounded-2xl border border-line px-3 py-2 text-sm">
            <div className="min-w-0 flex-1">
              <div className="font-medium">{name(r.person)} {r.status === "taken" ? <Badge tone="ok">covered by {name(r.takenBy)}</Badge> : <Badge tone="warn">waiting</Badge>}</div>
              <div className="text-xs text-muted">{when(r.from)} → {when(r.to)}{r.note ? ` · ${r.note}` : ""}</div>
            </div>
            {r.status === "open" && <IconButton aria-label="Cancel" disabled={busy} onClick={() => run(() => api<View>("DELETE", `/api/oncall/cover/${r.id}`), "Request canceled")}><X size={14} /></IconButton>}
          </div>
        ))}
        <div className="space-y-2 rounded-2xl bg-surface2/60 p-3">
          <Select value={ask.person} onChange={(e) => setAsk({ ...ask, person: e.target.value })}>
            <option value="">Who needs cover…</option>
            {people.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </Select>
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-1">
            <Field label="From"><Input type="datetime-local" value={toLocal(ask.from)} onChange={(e) => setAsk({ ...ask, from: fromLocal(e.target.value) })} /></Field>
            <Field label="Until"><Input type="datetime-local" value={toLocal(ask.to)} onChange={(e) => setAsk({ ...ask, to: fromLocal(e.target.value) })} /></Field>
          </div>
          <Textarea rows={2} value={ask.note} onChange={(e) => setAsk({ ...ask, note: e.target.value })} placeholder="Why (optional)" />
          <Button size="sm" variant="secondary" disabled={busy || !ask.person || !ask.from || ask.to <= ask.from}
            onClick={() => run(() => api<View>("POST", "/api/oncall/cover", ask), "Asked the team").then(() => setAsk({ person: "", from: 0, to: 0, note: "" }))}>
            <Hand size={14} /> Ask the team
          </Button>
        </div>
      </div>
    </Card>
  );
}

/** Who acknowledged incidents, how fast, in the last 30 days. */
function WhoHandled() {
  const r = useApi<{ people: PersonStats[] }>("/api/reports/oncall?days=30");
  const people = (r.data?.people ?? []).filter((p) => p.pages > 0 || p.acked > 0).sort((a, b) => b.acked - a.acked);
  return (
    <Card>
      <SectionTitle icon={<Trophy size={15} />} title="Who handled what" sub="The last 30 days: incidents acknowledged after a page, and how fast." action={<Link to="/team" className="text-xs text-muted underline">Full report in Team</Link>} />
      {people.length === 0 && <p className="text-sm text-muted">No pages yet.</p>}
      <ul className="space-y-2">
        {people.map((p, i) => (
          <li key={p.person} className="flex items-center gap-3 text-sm">
            <span className={cn("grid h-7 w-7 shrink-0 place-items-center rounded-full text-[11px] font-semibold", i === 0 && p.acked > 0 ? "bg-accent text-accent-fg" : "bg-surface2")}>{initials(p.name)}</span>
            <div className="min-w-0 flex-1">
              <div className="font-medium">{p.name}</div>
              <div className="text-xs text-muted">{p.acked} handled · {p.pages} pages{p.missed ? ` · ${p.missed} missed` : ""}</div>
            </div>
            <span className="text-xs tabular-nums text-muted" title="Median time to acknowledge">{p.medianAckMs ? fmtDur(Math.max(1, Math.round(p.medianAckMs / 1000))) : "—"}</span>
          </li>
        ))}
      </ul>
    </Card>
  );
}

/** First visit: what on-call is and the three things to set. */
function GettingStarted() {
  const steps = [
    { icon: <Users size={16} />, title: "Pick who takes turns", text: "Add the people who share the nights and weekends; each takes a week." },
    { icon: <Clock size={16} />, title: "Choose the handover", text: "The day and time the next person takes over, or no weekly shifts at all." },
    { icon: <BellRing size={16} />, title: "Turn paging on", text: "Then critical incidents wake the right person, and move on if nobody answers." },
  ];
  return (
    <Card className="mb-6">
      <SectionTitle icon={<PhoneCall size={15} />} title="Set up on-call in three steps" sub="So someone is always woken up when something breaks, without everyone getting every alert." />
      <div className="grid gap-3 sm:grid-cols-3">
        {steps.map((s, i) => (
          <div key={i} className="rounded-2xl border border-line p-4">
            <div className="mb-2 flex items-center gap-2"><span className="grid h-7 w-7 place-items-center rounded-full bg-surface2">{s.icon}</span><b className="text-sm">{i + 1}. {s.title}</b></div>
            <p className="text-xs text-muted">{s.text}</p>
          </div>
        ))}
      </div>
    </Card>
  );
}
