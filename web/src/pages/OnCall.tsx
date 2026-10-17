import { useEffect, useState } from "react";
import { ArrowDown, ArrowUp, CalendarClock, CalendarDays, PhoneCall, Plus, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ErrorState, Field, IconButton, Input, LoadingState, SectionTitle, Segmented, Select, Spinner, ToggleRow } from "../components/ui";

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
}

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const toLocal = (ms: number) => (ms ? new Date(ms - new Date(ms).getTimezoneOffset() * 60_000).toISOString().slice(0, 16) : "");
const fromLocal = (s: string) => (s ? new Date(s).getTime() : 0);
const when = (ms: number) => new Date(ms).toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

/** Settings → On-call: who is woken for an incident, and who next when nobody answers. */
export function OnCallSettings() {
  const { toast } = useStore();
  const v = useApi<View>("/api/oncall");
  const [c, setC] = useState<Config | null>(null);
  const [busy, setBusy] = useState(false);
  const [add, setAdd] = useState<Override>({ person: "", from: 0, to: 0 });
  useEffect(() => {
    if (v.data) setC(v.data.config);
  }, [v.data]);
  if (v.error) return <ErrorState message={v.error} onRetry={v.reload} />;
  if (!v.data || !c) return <LoadingState />;
  const people = v.data.people;
  const byId = (id: string) => people.find((p) => p.id === id);
  const name = (id: string) => byId(id)?.name ?? "(removed)";
  const dirty = JSON.stringify(c) !== JSON.stringify(v.data.config);
  const set = (p: Partial<Config>) => setC({ ...c, ...p });
  const move = (i: number, d: number) => {
    const r = [...c.rotation];
    [r[i], r[i + d]] = [r[i + d], r[i]];
    set({ rotation: r });
  };
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
  const reach = (p?: Person) =>
    !p ? null : p.devices > 0 || p.email ? (
      <span className="text-xs text-muted">{[p.devices ? `${p.devices} phone${p.devices > 1 ? "s" : ""}` : "", p.email ? "email" : ""].filter(Boolean).join(" · ")}</span>
    ) : (
      <Badge tone="warn">cannot be reached</Badge>
    );
  const current = v.data.schedule?.[0];

  return (
    <div className="space-y-6">
      <Card>
        <SectionTitle icon={<PhoneCall size={15} />} title="On call now" />
        {v.data.config.enabled && v.data.now ? (
          <p className="text-sm">
            <span className="text-lg font-semibold">{name(v.data.now)}</span>
            {current && <span className="text-muted"> until {when(current.to)}</span>} {reach(byId(v.data.now))}
          </p>
        ) : (
          <p className="text-sm text-muted">On-call paging is off. Incidents reach your normal notification channels only.</p>
        )}
        <p className="mt-3 text-xs text-muted">
          A page is a push notification on that person's phones (each person turns push on under Settings → Account, on their phone) plus an email when email sending is set up. Tapping it opens an “I'm on it” button: that acknowledges the incident and stops the paging. Maintenance mode pauses paging too.
        </p>
      </Card>

      <Card>
        <SectionTitle icon={<CalendarClock size={15} />} title="Schedule" sub="Saved with the Save button below." />
        <ToggleRow label="Page the person on call" checked={c.enabled} onChange={(val) => set({ enabled: val })} />
        <Field label="Rotation (each person takes a week, in this order)" className="mt-3">
          <div className="space-y-2">
            {c.rotation.map((id, i) => (
              <div key={id} className="flex items-center gap-2 rounded-xl bg-surface2 px-3 py-2">
                <span className="w-5 text-xs text-muted">{i + 1}</span>
                <span className="flex-1 text-sm font-medium">{name(id)}</span>
                {reach(byId(id))}
                <IconButton aria-label="Move up" disabled={i === 0} onClick={() => move(i, -1)}><ArrowUp size={14} /></IconButton>
                <IconButton aria-label="Move down" disabled={i === c.rotation.length - 1} onClick={() => move(i, 1)}><ArrowDown size={14} /></IconButton>
                <IconButton aria-label="Remove" onClick={() => set({ rotation: c.rotation.filter((x) => x !== id) })}><Trash2 size={14} /></IconButton>
              </div>
            ))}
            <Select className="max-w-xs" value="" onChange={(e) => e.target.value && set({ rotation: [...c.rotation, e.target.value] })}>
              <option value="">Add a person…</option>
              {people.filter((p) => !c.rotation.includes(p.id)).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </Select>
            <p className="text-xs text-muted">Add team members under Settings → Team. When the rotation changes, its first person is on call from the current week.</p>
          </div>
        </Field>
        <div className="mt-4 grid gap-4 sm:grid-cols-3">
          <Field label="Handover day">
            <Select value={c.handoverDay} onChange={(e) => set({ handoverDay: Number(e.target.value) })}>
              {DAYS.map((d, i) => <option key={d} value={i}>{d}</option>)}
            </Select>
          </Field>
          <Field label="At">
            <Select value={c.handoverHour} onChange={(e) => set({ handoverHour: Number(e.target.value) })}>
              {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}
            </Select>
          </Field>
          <Field label="Time zone" hint={<button className="underline" onClick={() => set({ timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone })}>Use mine</button>}>
            <Input value={c.timeZone} placeholder="Europe/Paris" onChange={(e) => set({ timeZone: e.target.value })} />
          </Field>
          <Field label="Next person after (minutes without acknowledgement)">
            <Input type="number" min={1} max={240} value={c.escalateAfterMin} onChange={(e) => set({ escalateAfterMin: Number(e.target.value) })} />
          </Field>
          <Field label="Page for" className="sm:col-span-2">
            <Segmented value={c.severity} onChange={(val) => set({ severity: val })} options={[{ value: "critical", label: "Critical incidents" }, { value: "warning", label: "Warnings too" }]} />
          </Field>
        </div>
        <p className="mt-2 text-xs text-muted">Escalation goes to the next person in the rotation, then the one after, and finally to you (the owner).</p>

        <Field label="Overrides (holiday cover, swaps)" className="mt-5">
          <div className="space-y-2">
            {c.overrides.map((o, i) => (
              <div key={i} className="flex flex-wrap items-center gap-2 rounded-xl bg-surface2 px-3 py-2 text-sm">
                <span className="font-medium">{name(o.person)}</span>
                <span className="text-muted">{when(o.from)} → {when(o.to)}</span>
                <IconButton className="ml-auto" aria-label="Remove override" onClick={() => set({ overrides: c.overrides.filter((_, k) => k !== i) })}><Trash2 size={14} /></IconButton>
              </div>
            ))}
            <div className="flex flex-wrap items-center gap-2">
              <Select className="max-w-[180px]" value={add.person} onChange={(e) => setAdd({ ...add, person: e.target.value })}>
                <option value="">Who…</option>
                {people.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </Select>
              <Input type="datetime-local" className="max-w-[210px]" value={toLocal(add.from)} onChange={(e) => setAdd({ ...add, from: fromLocal(e.target.value) })} />
              <Input type="datetime-local" className="max-w-[210px]" value={toLocal(add.to)} onChange={(e) => setAdd({ ...add, to: fromLocal(e.target.value) })} />
              <Button size="sm" variant="secondary" disabled={!add.person || !add.from || add.to <= add.from} onClick={() => (set({ overrides: [...c.overrides, add] }), setAdd({ person: "", from: 0, to: 0 }))}>
                <Plus size={14} /> Add
              </Button>
            </div>
          </div>
        </Field>
        <div className="mt-6 flex gap-2 border-t border-line pt-5">
          <Button disabled={!dirty || busy} onClick={save}>{busy && <Spinner size={14} />} Save</Button>
          <Button variant="ghost" disabled={!dirty} onClick={() => setC(v.data!.config)}>Reset</Button>
        </div>
      </Card>

      {(v.data.schedule?.length ?? 0) > 0 && (
        <Card>
          <SectionTitle icon={<CalendarDays size={15} />} title="Coming weeks" />
          <ul className="divide-y divide-line">
            {v.data.schedule.map((s, i) => (
              <li key={i} className="flex flex-wrap items-center gap-2 py-2 text-sm">
                <span className="w-40 font-medium">{name(s.person)}</span>
                <span className="text-muted">{when(s.from)} → {when(s.to)}</span>
                {s.override && <Badge tone="info">override</Badge>}
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
