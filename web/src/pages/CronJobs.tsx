import { useState } from "react";
import { AlarmClock, HeartPulse, Pencil, Plus, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CodeBlock, ConfirmDialog, EmptyState, ErrorState, Field, Gate, IconButton, Input, LoadingState, Modal, PageHeader, SectionTitle } from "../components/ui";
import { dateTime, relTime } from "../mock/util";

interface Job {
  id: string;
  source: "crontab" | "cron.d" | "timer";
  file: string;
  user: string;
  schedule: string;
  command: string;
  lastRun?: number;
  failed?: boolean;
  lastError?: string;
  status: "ok" | "missed" | "failed" | "waiting" | "unknown";
  expected?: number;
  next?: number;
}
interface ServerJobs {
  serverId: string;
  jobs: Job[];
  hasLog: boolean;
  timeZone: string;
  at: number;
}
interface Ping {
  id: string;
  name: string;
  token: string;
  everyMin: number;
  graceMin: number;
  lastPing: number;
  lastFail: number;
  message: string;
  createdAt: number;
  status: "ok" | "late" | "failed" | "waiting";
}

const STATUS: Record<string, [tone: "ok" | "warn" | "crit" | "neutral", label: string]> = {
  ok: ["ok", "OK"],
  missed: ["crit", "Missed"],
  failed: ["crit", "Failed"],
  late: ["crit", "Late"],
  waiting: ["neutral", "Not run yet"],
  unknown: ["neutral", "Runs not logged"],
};

/** "every 5 minutes", "daily at 03:00": a crontab schedule in words, when it is a common one. */
function inWords(s: string) {
  const short: Record<string, string> = { "@hourly": "every hour", "@daily": "every day at 00:00", "@midnight": "every day at 00:00", "@weekly": "every Sunday at 00:00", "@monthly": "on the 1st of each month", "@yearly": "every 1 January", "@annually": "every 1 January" };
  if (short[s]) return short[s];
  const f = s.split(/\s+/);
  if (f.length !== 5) return s;
  const [m, h, dom, mon, dow] = f;
  const at = /^\d+$/.test(m) && /^\d+$/.test(h) ? `${h.padStart(2, "0")}:${m.padStart(2, "0")}` : "";
  if (m === "*" && h === "*" && dom === "*" && mon === "*" && dow === "*") return "every minute";
  if (/^\*\/\d+$/.test(m) && h === "*" && dom === "*" && mon === "*" && dow === "*") return `every ${m.slice(2)} minutes`;
  if (/^\d+$/.test(m) && h === "*" && dom === "*" && mon === "*" && dow === "*") return `every hour at :${m.padStart(2, "0")}`;
  if (at && dom === "*" && mon === "*" && dow === "*") return `every day at ${at}`;
  if (at && dom === "*" && mon === "*" && (dow === "1-5" || dow === "mon-fri")) return `weekdays at ${at}`;
  return s;
}

const MINUTES = [[5, "5 minutes"], [15, "15 minutes"], [60, "hour"], [1440, "day"], [10080, "week"]] as const;

export function CronJobs() {
  const { me, getServer, toast } = useStore();
  const owner = !me || me.role === "owner";
  const data = useApi<{ servers: ServerJobs[]; pings: Ping[] }>("/api/cron");
  const [edit, setEdit] = useState<Partial<Ping> | null>(null);
  const [del, setDel] = useState<Ping | null>(null);
  const [shown, setShown] = useState<string | null>(null);
  const save = async (p: Partial<Ping>) => {
    try {
      const out = await api<Ping>(p.id ? "PUT" : "POST", p.id ? `/api/cron/pings/${p.id}` : "/api/cron/pings", p);
      setEdit(null);
      if (!p.id) setShown(out.id);
      data.reload();
      toast(p.id ? "Heartbeat saved" : "Heartbeat added: put its link at the end of your job");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const remove = async () => {
    const p = del!;
    setDel(null);
    try {
      await api("DELETE", `/api/cron/pings/${p.id}`);
      data.reload();
      toast("Heartbeat deleted");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const servers = data.data?.servers.filter((s) => s.jobs.length > 0) ?? [];
  return (
    <>
      <PageHeader
        title="Cron jobs"
        sub="The scheduled jobs on your servers, found by the agents by themselves: crontabs and systemd timers. You get an alert when one does not run on time or fails."
        actions={owner && <Button onClick={() => setEdit({ everyMin: 60, graceMin: 10 })}><Plus size={15} /> Add heartbeat</Button>}
      />
      <Gate>
        {data.error ? (
          <ErrorState message={data.error} onRetry={data.reload} />
        ) : !data.data ? (
          <LoadingState />
        ) : (
          <div className="space-y-6">
            {servers.length === 0 && (
              <EmptyState icon={<AlarmClock size={20} />} title="No cron jobs found yet" desc="Agents look every 5 minutes, in users' crontabs, /etc/cron.d and systemd timers you made. Agents older than this version need an update first." />
            )}
            {servers.map((s) => (
              <Card key={s.serverId}>
                <SectionTitle icon={<AlarmClock size={15} />} title={getServer(s.serverId)?.name ?? s.serverId}
                  sub={`${s.jobs.length} job${s.jobs.length === 1 ? "" : "s"} · times in ${s.timeZone || "UTC"} · checked ${relTime(s.at)}${s.hasLog ? "" : " · this server's log does not show cron runs, so missed runs cannot be told"}`} />
                <ul className="divide-y divide-line">
                  {s.jobs.map((j) => (
                    <li key={j.id} className="flex flex-wrap items-start justify-between gap-3 py-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
                          {inWords(j.schedule)}
                          {inWords(j.schedule) !== j.schedule && <span className="font-mono text-xs text-muted">{j.schedule}</span>}
                          <Badge>{j.source === "timer" ? "systemd timer" : `as ${j.user}`}</Badge>
                        </div>
                        <code className="mt-1 block break-all font-mono text-xs text-muted" title={j.file}>{j.source === "timer" ? `${j.file}: ${j.command}` : j.command}</code>
                        {j.status === "missed" && j.expected && <p className="mt-1 text-xs text-crit">Did not run at {dateTime(j.expected)}.</p>}
                        {j.status === "failed" && j.lastError && <p className="mt-1 text-xs text-crit">Last run failed: {j.lastError}</p>}
                      </div>
                      <div className="text-right text-xs text-muted">
                        <Badge tone={STATUS[j.status][0]}>{STATUS[j.status][1]}</Badge>
                        <div className="mt-1">{j.lastRun ? `ran ${relTime(j.lastRun)}` : "no run seen"}</div>
                        {j.next ? <div>next {dateTime(j.next)}</div> : null}
                      </div>
                    </li>
                  ))}
                </ul>
              </Card>
            ))}
            {owner && (
              <Card>
                <SectionTitle icon={<HeartPulse size={15} />} title="Heartbeats"
                  sub="For any job, here or elsewhere: it calls its own link when it finishes. No call in time, or a call to the /fail link, and you get an alert. It also tells failures apart, which cron's log cannot." />
                {data.data.pings.length === 0 ? (
                  <p className="text-sm text-muted">None yet.</p>
                ) : (
                  <ul className="divide-y divide-line">
                    {data.data.pings.map((p) => {
                      const link = `${location.origin}/api/public/ping/${p.token}`;
                      return (
                        <li key={p.id} className="space-y-2 py-3">
                          <div className="flex flex-wrap items-center justify-between gap-3">
                            <div className="min-w-0">
                              <div className="flex items-center gap-2 text-sm font-medium">{p.name} <Badge tone={STATUS[p.status][0]}>{STATUS[p.status][1]}</Badge></div>
                              <div className="text-xs text-muted">
                                every {MINUTES.find(([m]) => m === p.everyMin)?.[1] ?? `${p.everyMin} min`} (+{p.graceMin} min) · {p.lastPing ? `last check-in ${relTime(p.lastPing)}` : "no check-in yet"}
                                {p.status === "failed" && p.message ? ` · failed: ${p.message}` : ""}
                              </div>
                            </div>
                            <div className="flex items-center gap-1">
                              <Button size="sm" variant="ghost" onClick={() => setShown(shown === p.id ? null : p.id)}>{shown === p.id ? "Hide" : "How to use"}</Button>
                              <IconButton aria-label="Edit" onClick={() => setEdit(p)}><Pencil size={14} /></IconButton>
                              <IconButton aria-label="Delete" onClick={() => setDel(p)}><Trash2 size={14} /></IconButton>
                            </div>
                          </div>
                          {shown === p.id && (
                            <div className="space-y-2 text-xs text-muted">
                              <p>Add this to the end of the job's line in the crontab (it reports success, or failure when the job fails):</p>
                              <CodeBlock>{`your-command && curl -fsS -m 10 ${link} || curl -fsS -m 10 ${link}/fail`}</CodeBlock>
                              <p>Or call it from your code when the job is done; send why it failed with <code>{`${link}/fail?m=disk+full`}</code>.</p>
                            </div>
                          )}
                        </li>
                      );
                    })}
                  </ul>
                )}
              </Card>
            )}
          </div>
        )}
      </Gate>
      {edit && <PingForm value={edit} onSave={save} onClose={() => setEdit(null)} />}
      <ConfirmDialog open={!!del} danger title={`Delete ${del?.name}?`} confirmLabel="Delete" message="Its link stops working; the job's calls to it are refused." onCancel={() => setDel(null)} onConfirm={remove} />
    </>
  );
}

function PingForm({ value, onSave, onClose }: { value: Partial<Ping>; onSave: (p: Partial<Ping>) => void; onClose: () => void }) {
  const [f, setF] = useState(value);
  return (
    <Modal open onClose={onClose} title={value.id ? `Edit ${value.name}` : "New heartbeat"}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button disabled={!f.name?.trim() || !f.everyMin} onClick={() => onSave(f)}>Save</Button></>}>
      <div className="space-y-4">
        <Field label="Name" hint="Like “Nightly database backup”.">
          <Input value={f.name ?? ""} maxLength={80} onChange={(e) => setF({ ...f, name: e.target.value })} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Runs every (minutes)" hint="60 = hourly, 1440 = daily.">
            <Input type="number" min={1} value={f.everyMin ?? ""} onChange={(e) => setF({ ...f, everyMin: Number(e.target.value) })} />
          </Field>
          <Field label="Grace (minutes)" hint="How late it may be before you hear of it.">
            <Input type="number" min={0} value={f.graceMin ?? 0} onChange={(e) => setF({ ...f, graceMin: Number(e.target.value) })} />
          </Field>
        </div>
      </div>
    </Modal>
  );
}
