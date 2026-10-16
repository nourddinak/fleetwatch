import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Activity as ActivityIcon, Moon, PhoneCall, Users } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import type { Incident } from "../mock/types";
import { dateTime, fmtDur, relTime } from "../mock/util";
import { Badge, Button, Card, EmptyState, ErrorState, Gate, IncidentStatusBadge, LoadingState, PageHeader, SectionTitle, Segmented, Table, Td, Textarea, Tr } from "../components/ui";

interface Activity {
  ts: number;
  actor: string;
  action: string;
  text: string;
  serverId?: string;
  appId?: string;
}
interface Shift {
  person: string;
  from: number;
  to: number;
  override: boolean;
}
interface Note {
  text: string;
  by: string;
  updatedAt: number;
}
interface Handoff {
  enabled: boolean;
  current: Shift | null;
  previous: Shift | null;
  note: Note | null;
  draft: Note | null;
  summary: { opened: number; resolved: number; stillOpen: number; pages: number; incidents: Incident[] } | null;
  openNow: number;
}
interface PersonStats {
  person: string;
  name: string;
  hoursOnCall: number;
  pages: number;
  night: number;
  escalated: number;
  acked: number;
  medianAckMs: number;
  missed: number;
}
interface PageRow {
  ts: number;
  incidentId: string;
  title: string;
  person: string;
  level: number;
  ok: boolean;
  ackMs?: number;
}

export function TeamPage() {
  const [tab, setTab] = useState<"activity" | "oncall">("activity");
  return (
    <>
      <PageHeader title="Team" sub="What everyone did, live, and how on-call duty is going: handoffs between shifts and who carries the load." />
      <div className="mb-5"><Segmented value={tab} onChange={setTab} options={[{ value: "activity", label: "Activity" }, { value: "oncall", label: "On call" }]} /></div>
      <Gate>{tab === "activity" ? <ActivityFeed /> : <OnCallTab />}</Gate>
    </>
  );
}

const initials = (s: string) => s.split(/\s+/).map((w) => w[0]).join("").slice(0, 2).toUpperCase();

function ActivityFeed() {
  const { subscribeLive } = useStore();
  const list = useApi<Activity[]>("/api/activity?limit=200");
  const { setData } = list;
  const [, tick] = useState(0);
  useEffect(() => subscribeLive((m) => {
    if (m.type === "activity") setData((l) => [m.data, ...(l ?? [])].slice(0, 300));
  }), [subscribeLive, setData]);
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 30_000); // keep "2 min ago" fresh
    return () => clearInterval(t);
  }, []);
  if (list.error) return <ErrorState message={list.error} onRetry={list.reload} />;
  if (!list.data) return <LoadingState />;
  if (list.data.length === 0) return <EmptyState icon={<ActivityIcon size={20} />} title="Nothing yet" desc="Restarts, commands, incident handling and changes appear here as people make them." />;
  return (
    <Card>
      <ol className="divide-y divide-line">
        {list.data.map((a, i) => (
          <li key={`${a.ts}-${i}`} className="flex items-start gap-3 py-3">
            <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-surface2 text-xs font-semibold">{initials(a.actor)}</span>
            <div className="min-w-0 flex-1 text-sm">
              <span className="font-semibold">{a.actor}</span> {a.text}
              {(a.appId || a.serverId) && (
                <Link to={a.appId ? `/applications/${a.appId}` : `/servers/${a.serverId}`} className="ml-1 text-xs text-muted underline">open</Link>
              )}
            </div>
            <span className="shrink-0 text-xs text-muted" title={dateTime(a.ts)}>{relTime(a.ts)}</span>
          </li>
        ))}
      </ol>
    </Card>
  );
}

const dur = (ms: number) => (ms ? fmtDur(Math.max(1, Math.round(ms / 1000))) : "—");

function OnCallTab() {
  const { toast } = useStore();
  const h = useApi<{ handoff: Handoff; names: Record<string, string> }>("/api/handoff");
  const [days, setDays] = useState<"7" | "30" | "90">("30");
  const report = useApi<{ people: PersonStats[]; pages: PageRow[] }>(`/api/reports/oncall?days=${days}`);
  const [draft, setDraft] = useState<string | null>(null);
  useEffect(() => {
    if (h.data && draft === null) setDraft(h.data.handoff.draft?.text ?? "");
  }, [h.data, draft]);
  if (h.error) return <ErrorState message={h.error} onRetry={h.reload} />;
  if (!h.data) return <LoadingState />;
  const { handoff, names } = h.data;
  const name = (id: string) => names[id] ?? id;
  const saveDraft = async () => {
    try {
      await api("PUT", "/api/handoff", { note: draft ?? "" });
      toast("Note saved for the next shift");
      h.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  if (!handoff.enabled || !handoff.current) {
    return <EmptyState icon={<PhoneCall size={20} />} title="No on-call schedule" desc="The owner sets up the rotation under Settings → On-call." />;
  }
  return (
    <div className="space-y-6">
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <SectionTitle title="On call now" />
          <div className="text-2xl font-semibold">{name(handoff.current.person)}</div>
          <div className="text-sm text-muted">since {dateTime(handoff.current.from)} · until {dateTime(handoff.current.to)}{handoff.current.override ? " (cover)" : ""}</div>
          <div className="mt-3"><Badge tone={handoff.openNow ? "warn" : "ok"}>{handoff.openNow} open incident{handoff.openNow === 1 ? "" : "s"}</Badge></div>
          {handoff.note?.text && (
            <div className="mt-4 rounded-2xl bg-surface2 p-4">
              <div className="mb-1 text-xs text-muted">Handoff note from {handoff.note.by}, {relTime(handoff.note.updatedAt)}</div>
              <div className="whitespace-pre-wrap text-sm">{handoff.note.text}</div>
            </div>
          )}
          <div className="mt-4">
            <div className="mb-1 text-xs font-medium">Note for whoever is on call next</div>
            <Textarea rows={3} value={draft ?? ""} onChange={(e) => setDraft(e.target.value)} placeholder="What is going on, what to watch, what is half done…" />
            <div className="mt-2 flex items-center justify-between gap-2">
              <span className="text-xs text-muted">{handoff.draft ? `Saved by ${handoff.draft.by} ${relTime(handoff.draft.updatedAt)}` : "They get it when their shift starts."}</span>
              <Button size="sm" disabled={(draft ?? "") === (handoff.draft?.text ?? "")} onClick={saveDraft}>Save note</Button>
            </div>
          </div>
        </Card>
        <Card>
          <SectionTitle title="Last shift" sub={handoff.previous ? `${name(handoff.previous.person)} · ${dateTime(handoff.previous.from)} → ${dateTime(handoff.previous.to)}` : "No shift before this one yet."} />
          {handoff.summary && (
            <>
              <div className="mb-3 flex flex-wrap gap-2">
                <Badge>{handoff.summary.opened} incidents</Badge>
                <Badge tone="ok">{handoff.summary.resolved} resolved</Badge>
                {handoff.summary.stillOpen > 0 && <Badge tone="warn">{handoff.summary.stillOpen} still open</Badge>}
                <Badge tone="info">{handoff.summary.pages} pages</Badge>
              </div>
              <ul className="divide-y divide-line">
                {handoff.summary.incidents.map((x) => (
                  <li key={x.id} className="flex items-center gap-2 py-2 text-sm">
                    <span className="min-w-0 flex-1 truncate">{x.title}</span>
                    <IncidentStatusBadge status={x.status} />
                  </li>
                ))}
                {handoff.summary.incidents.length === 0 && <li className="py-2 text-sm text-muted">A quiet shift.</li>}
              </ul>
            </>
          )}
        </Card>
      </div>
      <Card pad={false}>
        <div className="flex flex-wrap items-center justify-between gap-3 p-5 pb-2">
          <SectionTitle title="Who carries the load" sub="Pages, nights and how fast each person answered." />
          <Segmented size="sm" value={days} onChange={setDays} options={[{ value: "7", label: "7 days" }, { value: "30", label: "30 days" }, { value: "90", label: "90 days" }]} />
        </div>
        {!report.data ? <LoadingState /> : report.data.people.length === 0 ? <div className="p-5"><EmptyState icon={<Users size={20} />} title="No one in the rotation" /></div> : (
          <Table head={["Person", "On call", "Pages", "At night", "Escalated to them", "Answered", "Missed", "Median answer"]}>
            {report.data.people.map((p) => (
              <Tr key={p.person}>
                <Td className="font-medium">{p.name}</Td>
                <Td>{Math.round(p.hoursOnCall)} h</Td>
                <Td>{p.pages}</Td>
                <Td>{p.night > 0 ? <span className="inline-flex items-center gap-1"><Moon size={12} /> {p.night}</span> : 0}</Td>
                <Td>{p.escalated}</Td>
                <Td className="text-ok">{p.acked}</Td>
                <Td className={p.missed ? "text-warn" : ""}>{p.missed}</Td>
                <Td>{dur(p.medianAckMs)}</Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>
      {report.data && report.data.pages.length > 0 && (
        <Card>
          <SectionTitle title="Recent pages" />
          <ul className="divide-y divide-line text-sm">
            {report.data.pages.map((p, i) => (
              <li key={i} className="flex flex-wrap items-center gap-2 py-2">
                <span className="text-xs text-muted" title={dateTime(p.ts)}>{relTime(p.ts)}</span>
                <span className="font-medium">{name(p.person)}</span>
                {p.level > 0 && <Badge tone="warn">escalated</Badge>}
                {!p.ok && <Badge tone="crit">not delivered</Badge>}
                <span className="min-w-0 flex-1 truncate text-muted">{p.title}</span>
                <span className="text-xs">{p.ackMs ? `answered in ${dur(p.ackMs)}` : ""}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
