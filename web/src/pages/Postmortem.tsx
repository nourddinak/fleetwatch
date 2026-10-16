import { useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Activity, Bell, Copy, Download, GitCommit, MessageSquare, Siren, Wrench } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import type { Incident } from "../mock/types";
import { dateTime, fmtDur } from "../mock/util";
import { Button, Card, ErrorState, Field, LoadingState, PageHeader, SectionTitle, SeverityBadge, Textarea } from "../components/ui";
import { cn } from "../utils/cn";

interface Moment {
  ts: number;
  kind: "incident" | "event" | "deploy" | "notification" | "comment" | "action";
  text: string;
  who?: string;
}
interface Notes {
  impact: string;
  rootCause: string;
  resolution: string;
  actionItems: string;
  updatedAt?: number;
  updatedBy?: string;
}
interface PM {
  incident: Incident;
  durationMs: number;
  timeToAckMs?: number;
  ackBy?: string;
  timeline: Moment[];
  notes: Notes;
}

const ICON = { incident: Siren, event: Activity, deploy: GitCommit, notification: Bell, comment: MessageSquare, action: Wrench };
const FIELDS: [keyof Notes, string, string][] = [
  ["impact", "Impact", "Who or what was affected, and how badly?"],
  ["rootCause", "Root cause", "Why did it happen?"],
  ["resolution", "Resolution", "What fixed it?"],
  ["actionItems", "Action items", "What will stop it from happening again? One per line."],
];
const dur = (ms?: number) => (ms ? fmtDur(Math.round(ms / 1000)) : "—");

function markdown(pm: PM, notes: Notes, server: string, app?: string) {
  const x = pm.incident;
  const lines = [
    `# Postmortem: ${x.title}`,
    "",
    `- **Type:** ${x.type} (${x.severity})`,
    `- **Where:** ${app ? `${app} on ` : ""}${server}`,
    `- **Started:** ${dateTime(x.firstAt)}`,
    `- **Resolved:** ${x.resolvedAt ? dateTime(x.resolvedAt) : "not yet"}`,
    `- **Duration:** ${dur(pm.durationMs)}`,
    `- **Time to acknowledge:** ${dur(pm.timeToAckMs)}${pm.ackBy ? ` (${pm.ackBy})` : ""}`,
    "",
  ];
  for (const [k, label] of FIELDS) lines.push(`## ${label}`, "", String(notes[k] || "_Not written yet._"), "");
  lines.push("## Timeline", "");
  for (const m of pm.timeline) lines.push(`- ${dateTime(m.ts)} · ${m.text}${m.who ? ` (${m.who})` : ""}`);
  if (x.logSamples.length) lines.push("", "## Log samples", "", "```", ...x.logSamples, "```");
  return lines.join("\n") + "\n";
}

export function Postmortem() {
  const { id = "" } = useParams();
  const { me, getServer, getApp, toast } = useStore();
  const data = useApi<{ postmortem: PM; runbook?: string }>(`/api/incidents/${id}/postmortem`);
  const [notes, setNotes] = useState<Notes | null>(null);
  const [busy, setBusy] = useState(false);
  const canWrite = !me || me.role !== "viewer";
  useEffect(() => {
    if (data.data) setNotes(data.data.postmortem.notes);
  }, [data.data]);
  if (data.error) return <ErrorState message={data.error} onRetry={data.reload} />;
  if (!data.data || !notes) return <LoadingState />;
  const pm = data.data.postmortem;
  const x = pm.incident;
  const server = getServer(x.serverId)?.name ?? "outside check";
  const app = x.appId ? getApp(x.appId)?.name : undefined;
  const dirty = JSON.stringify(notes) !== JSON.stringify(pm.notes);
  const save = async () => {
    setBusy(true);
    try {
      const n = await api<Notes>("POST", `/api/incidents/${id}/postmortem`, { impact: notes.impact, rootCause: notes.rootCause, resolution: notes.resolution, actionItems: notes.actionItems });
      data.setData({ ...data.data!, postmortem: { ...pm, notes: n } });
      toast("Postmortem saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const md = () => markdown(pm, notes, server, app);
  const download = () => {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([md()], { type: "text/markdown" }));
    a.download = `postmortem-${x.id}.md`;
    a.click();
    URL.revokeObjectURL(a.href);
  };
  return (
    <>
      <PageHeader title={`Postmortem: ${x.title}`} back={{ to: "/incidents", label: "Incidents" }}
        sub={`${x.type} · ${app ? `${app} on ` : ""}${server}`}
        actions={<>
          <Button variant="secondary" onClick={() => navigator.clipboard.writeText(md()).then(() => toast("Copied as Markdown"))}><Copy size={15} /> Copy</Button>
          <Button variant="secondary" onClick={download}><Download size={15} /> .md</Button>
        </>} />
      <div className="mb-6 grid grid-cols-2 gap-3 md:grid-cols-4">
        {[
          ["Severity", <SeverityBadge key="s" severity={x.severity} />],
          ["Duration", dur(pm.durationMs)],
          ["Time to acknowledge", `${dur(pm.timeToAckMs)}${pm.ackBy ? ` · ${pm.ackBy}` : ""}`],
          ["Status", x.status === "resolved" ? `resolved ${x.resolvedAt ? dateTime(x.resolvedAt) : ""}` : x.status],
        ].map(([label, value]) => (
          <Card key={label as string}><div className="text-xs text-muted">{label}</div><div className="mt-1 font-semibold">{value}</div></Card>
        ))}
      </div>
      <div className="grid gap-6 xl:grid-cols-[1fr_420px]">
        <Card>
          <SectionTitle title="Report" sub={pm.notes.updatedAt ? `Last edited by ${pm.notes.updatedBy} ${dateTime(pm.notes.updatedAt)}` : "Write what happened while it is fresh."} />
          <div className="space-y-4">
            {FIELDS.map(([k, label, hint]) => (
              <Field key={k} label={label} hint={hint}>
                <Textarea rows={k === "actionItems" ? 4 : 3} disabled={!canWrite} value={notes[k] as string} onChange={(e) => setNotes({ ...notes, [k]: e.target.value })} />
              </Field>
            ))}
          </div>
          {canWrite && <div className="mt-4"><Button disabled={!dirty || busy} onClick={save}>Save</Button></div>}
          {data.data.runbook && (
            <div className="mt-6 rounded-2xl bg-surface2 p-4 text-sm">
              <div className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted">Runbook for {x.type}</div>
              <div className="whitespace-pre-wrap">{data.data.runbook}</div>
            </div>
          )}
        </Card>
        <Card>
          <SectionTitle title="Timeline" sub="Built from incident changes, server and app events, deploys, notifications, dashboard actions and comments." />
          <ol className="relative space-y-3 border-l border-line pl-5">
            {pm.timeline.map((m, i) => {
              const Icon = ICON[m.kind] ?? Activity;
              return (
                <li key={i} className="relative">
                  <span className={cn("absolute -left-[31px] top-0.5 flex h-5 w-5 items-center justify-center rounded-full border border-line bg-surface", m.kind === "deploy" && "text-info", m.kind === "incident" && "text-crit")}>
                    <Icon size={11} />
                  </span>
                  <div className="text-sm">{m.text}{m.who ? <span className="text-muted"> · {m.who}</span> : null}</div>
                  <div className="text-[11px] text-muted">{dateTime(m.ts)}</div>
                </li>
              );
            })}
          </ol>
          {x.logSamples.length > 0 && (
            <div className="mt-5">
              <div className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted">Log samples</div>
              <div className="space-y-1 rounded-2xl bg-surface2 p-3 font-mono text-[11px]">{x.logSamples.map((l, i) => <div key={i} className="break-all">{l}</div>)}</div>
            </div>
          )}
        </Card>
      </div>
    </>
  );
}
