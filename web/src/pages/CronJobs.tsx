import { useEffect, useRef, useState } from "react";
import { AlarmClock, HeartPulse, Pause, Pencil, Play, Plus, RotateCcw, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CodeBlock, ConfirmDialog, EmptyState, ErrorState, Field, Gate, IconButton, Input, LoadingState, Modal, PageHeader, SectionTitle, Select } from "../components/ui";
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
  status: "ok" | "missed" | "failed" | "paused" | "waiting" | "unknown";
  paused?: boolean;
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
  paused: ["neutral", "Paused"],
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

const PRESETS = [
  ["*/5 * * * *", "Every 5 minutes"],
  ["*/15 * * * *", "Every 15 minutes"],
  ["0 * * * *", "Every hour"],
  ["0 3 * * *", "Every day at 03:00"],
  ["0 3 * * 0", "Every Sunday at 03:00"],
  ["0 3 1 * *", "The 1st of each month at 03:00"],
] as const;

interface CronResult {
  serverId: string;
  runId: string;
  op: string;
  error?: string;
  output?: string;
}

const MINUTES = [[5, "5 minutes"], [15, "15 minutes"], [60, "hour"], [1440, "day"], [10080, "week"]] as const;

export function CronJobs() {
  const { me, getServer, toast, subscribeLive } = useStore();
  const owner = !me || me.role === "owner";
  const data = useApi<{ servers: ServerJobs[]; pings: Ping[] }>("/api/cron");
  const [jobEdit, setJobEdit] = useState<{ serverId: string; job?: Job } | null>(null);
  const [jobDel, setJobDel] = useState<{ serverId: string; job: Job } | null>(null);
  const [output, setOutput] = useState<{ title: string; text: string; error?: string } | null>(null);
  const pending = useRef(new Map<string, string>()); // run ID → what was asked, for the answer
  const { reload } = data;
  useEffect(
    () =>
      subscribeLive((m) => {
        if (m.type === "cron") reload();
        if (m.type !== "cron.result") return;
        const r = m.data as CronResult;
        const what = pending.current.get(r.runId);
        if (!what) return;
        pending.current.delete(r.runId);
        if (r.op === "run") setOutput({ title: what, text: r.output || "(no output)", error: r.error });
        else if (r.error) toast(r.error, "err");
        else toast(what);
        reload();
      }),
    [subscribeLive, reload, toast],
  );
  const change = async (serverId: string, body: { op: string; jobId?: string; user?: string; schedule?: string; command?: string }, done: string) => {
    try {
      const { runId } = await api<{ runId: string }>("POST", `/api/servers/${serverId}/cron`, body);
      pending.current.set(runId, done);
      toast(body.op === "run" ? "Running it now…" : "Sent to the server…");
      return true;
    } catch (e) {
      toast((e as ApiError).message, "err");
      return false;
    }
  };
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
  const servers = data.data?.servers ?? [];
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
                  sub={`${s.jobs.length} job${s.jobs.length === 1 ? "" : "s"} · times in ${s.timeZone || "UTC"} · checked ${relTime(s.at)}${s.hasLog ? "" : " · this server's log does not show cron runs, so missed runs cannot be told"}`}
                  action={owner && <Button size="sm" variant="secondary" onClick={() => setJobEdit({ serverId: s.serverId })}><Plus size={14} /> Add job</Button>} />
                {s.jobs.length === 0 && <p className="text-sm text-muted">No jobs on this server yet.</p>}
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
                        {owner && (
                          <div className="mt-1 flex justify-end gap-0.5">
                            <IconButton aria-label="Run now" title="Run now" onClick={() => change(s.serverId, { op: "run", jobId: j.id }, `Ran ${j.source === "timer" ? j.file : j.command}`)}><Play size={14} /></IconButton>
                            {j.paused ? (
                              <IconButton aria-label="Resume" title="Resume" onClick={() => change(s.serverId, { op: "resume", jobId: j.id }, "Job resumed")}><RotateCcw size={14} /></IconButton>
                            ) : (
                              <IconButton aria-label="Pause" title="Pause" onClick={() => change(s.serverId, { op: "pause", jobId: j.id }, "Job paused: it will not run until resumed")}><Pause size={14} /></IconButton>
                            )}
                            {j.source !== "timer" && <IconButton aria-label="Edit" title="Edit" onClick={() => setJobEdit({ serverId: s.serverId, job: j })}><Pencil size={14} /></IconButton>}
                            {j.source !== "timer" && <IconButton aria-label="Delete" title="Delete" onClick={() => setJobDel({ serverId: s.serverId, job: j })}><Trash2 size={14} /></IconButton>}
                          </div>
                        )}
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
      {jobEdit && (
        <JobForm job={jobEdit.job} server={getServer(jobEdit.serverId)?.name ?? jobEdit.serverId} onClose={() => setJobEdit(null)}
          onSave={async (v) => {
            const ok = await change(jobEdit.serverId, jobEdit.job ? { op: "update", jobId: jobEdit.job.id, schedule: v.schedule, command: v.command } : { op: "add", user: v.user, schedule: v.schedule, command: v.command },
              jobEdit.job ? "Job changed" : "Job added");
            if (ok) setJobEdit(null);
          }} />
      )}
      <ConfirmDialog open={!!jobDel} danger title="Delete this job?" confirmLabel="Delete job" message={<>It is removed from the server's crontab: <code className="break-all font-mono text-xs">{jobDel?.job.command}</code>. Pausing keeps it to turn back on later.</>}
        onCancel={() => setJobDel(null)} onConfirm={() => { const d = jobDel!; setJobDel(null); change(d.serverId, { op: "delete", jobId: d.job.id }, "Job deleted"); }} />
      <Modal open={!!output} onClose={() => setOutput(null)} title={output?.title ?? ""} wide footer={<Button onClick={() => setOutput(null)}>Close</Button>}>
        {output?.error && <p className="mb-2 text-sm text-crit">{output.error}</p>}
        <pre className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-all rounded-xl bg-surface2 p-3 font-mono text-xs">{output?.text}</pre>
      </Modal>
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

function JobForm({ job, server, onSave, onClose }: { job?: Job; server: string; onSave: (v: { user: string; schedule: string; command: string }) => void; onClose: () => void }) {
  const [f, setF] = useState({ user: job?.user ?? "root", schedule: job?.schedule ?? "0 3 * * *", command: job?.command ?? "" });
  const [busy, setBusy] = useState(false);
  const preset = PRESETS.some(([v]) => v === f.schedule) ? f.schedule : "custom";
  return (
    <Modal open onClose={onClose} title={job ? "Edit job" : `New job on ${server}`}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button disabled={busy || !f.command.trim() || !f.schedule.trim()} onClick={async () => { setBusy(true); await onSave(f); setBusy(false); }}>Save</Button></>}>
      <div className="space-y-4">
        <Field label="When" hint={inWords(f.schedule) !== f.schedule ? inWords(f.schedule) : "Minute, hour, day of month, month, day of week (crontab), in the server's time zone."}>
          <div className="grid gap-2 sm:grid-cols-2">
            <Select value={preset} onChange={(e) => e.target.value !== "custom" && setF({ ...f, schedule: e.target.value })}>
              {PRESETS.map(([v, label]) => <option key={v} value={v}>{label}</option>)}
              <option value="custom">Custom…</option>
            </Select>
            <Input value={f.schedule} className="font-mono" placeholder="*/5 * * * *" onChange={(e) => setF({ ...f, schedule: e.target.value })} />
          </div>
        </Field>
        <Field label="Command" hint="Runs with /bin/sh in the user's home folder. Write \% for a percent sign.">
          <Input value={f.command} className="font-mono" placeholder="cd /srv/app && php artisan schedule:run" onChange={(e) => setF({ ...f, command: e.target.value })} />
        </Field>
        {!job && (
          <Field label="Run as user">
            <Input value={f.user} className="font-mono" onChange={(e) => setF({ ...f, user: e.target.value.trim() })} />
          </Field>
        )}
        <p className="text-xs text-muted">Adding or changing a command needs “custom commands” allowed on that server (allowCustomCommands in /etc/ops-agent/config.json). Pausing, running and deleting always work.</p>
      </div>
    </Modal>
  );
}
