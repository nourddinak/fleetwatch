import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { History, Pencil, Play, Plus, ShieldAlert, Terminal, Trash2 } from "lucide-react";
import { useStore } from "../store/store";
import { api, useApi, type ApiError, type SecuritySettings } from "../api/client";
import type { App } from "../mock/types";
import { dateTime, relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, EmptyState, Field, Input, Modal, SectionTitle, Spinner, Table, Td, Textarea, Toggle, Tr, type Tone } from "./ui";
import { cn } from "../utils/cn";

export interface Run {
  id: string;
  appId: string;
  commandId: string;
  name: string;
  preview: string;
  cwd: string;
  runAs: string;
  startedAt: number;
  finishedAt?: number;
  status: "running" | "succeeded" | "failed" | "timeout" | "refused" | "error";
  exitCode: number | null;
  stdout: string;
  stderr: string;
  error: string;
  initiatedBy: string;
}

interface CommandView {
  id: string;
  kind: "builtin" | "custom";
  name: string;
  description: string;
  preview: string;
  cwd: string;
  timeoutSec: number;
  requiresConfirm: boolean;
  danger: boolean;
  envKeys: string[];
  lastRun: Run | null;
}

interface CommandDef {
  id: string;
  name: string;
  command: string;
  cwd: string;
  timeoutSec: number;
  confirm: boolean;
  env: string[];
}

export const runTone: Record<Run["status"], Tone> = { running: "info", succeeded: "ok", failed: "crit", timeout: "crit", refused: "warn", error: "crit" };
export const RunStatus = ({ status, exitCode }: { status: Run["status"]; exitCode?: number | null }) => (
  <Badge tone={runTone[status]} dot pulse={status === "running"}>
    {status}
    {status === "failed" && exitCode != null && exitCode >= 0 ? ` (exit ${exitCode})` : ""}
  </Badge>
);

function Output({ run }: { run: Run }) {
  const box = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [run.stdout.length, run.stderr.length]);
  const empty = !run.stdout && !run.stderr && !run.error;
  return (
    <pre ref={box} className="max-h-[420px] min-h-[120px] overflow-auto whitespace-pre-wrap break-all rounded-2xl bg-surface2 p-4 font-mono text-[12px] leading-5">
      {empty && (run.status === "running" ? <span className="text-muted">Waiting for output…</span> : <span className="text-muted">(no output)</span>)}
      {run.stdout}
      {run.stderr && <span className="text-crit">{run.stderr}</span>}
      {run.error && <span className="text-crit">{`\n[${run.error}]`}</span>}
    </pre>
  );
}

const blankDef = { name: "", command: "", cwd: "", timeoutSec: 300, confirm: true, env: "", password: "" };

export function CommandRunner({ app }: { app: App }) {
  const { toast, subscribeLive, servers } = useStore();
  const [params, setParams] = useSearchParams();
  const commands = useApi<CommandView[]>(`/api/apps/${app.id}/commands`);
  const runs = useApi<Run[]>(`/api/apps/${app.id}/runs`);
  const security = useApi<SecuritySettings>("/api/settings/security");
  const [confirm, setConfirm] = useState<CommandView | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [active, setActive] = useState<Run | null>(null);
  const [editor, setEditor] = useState<{ id?: string; f: typeof blankDef } | null>(null);
  const [del, setDel] = useState<CommandView | null>(null);
  const [err, setErr] = useState("");
  const offline = servers.find((s) => s.id === app.serverId)?.status !== "online";
  const reauth = !!security.data?.reauthForCommands;

  // Live output and completion for runs of this app.
  useEffect(
    () =>
      subscribeLive((m) => {
        if (m.type === "run.output" && m.data.appId === app.id) {
          setActive((r) => (r && r.id === m.data.runId ? { ...r, [m.data.stream === "stderr" ? "stderr" : "stdout"]: r[m.data.stream === "stderr" ? "stderr" : "stdout"] + m.data.data } : r));
        } else if (m.type === "run" && m.data.appId === app.id) {
          setActive((r) => (r && r.id === m.data.id ? { ...r, ...m.data, stdout: m.data.stdout || r.stdout, stderr: m.data.stderr || r.stderr } : r));
          if (m.data.status !== "running") {
            runs.reload();
            commands.reload();
          }
        }
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [app.id, subscribeLive],
  );

  // Deep link: ?run=builtin:restart opens the confirmation for that command.
  useEffect(() => {
    const want = params.get("run");
    if (want && commands.data) {
      const c = commands.data.find((x) => x.id === want);
      if (c) setConfirm(c);
      params.delete("run");
      setParams(params, { replace: true });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [commands.data, params]);

  const ask = (c: CommandView) => {
    setErr("");
    setPassword("");
    if (c.requiresConfirm || reauth) setConfirm(c);
    else start(c);
  };
  const start = async (c: CommandView) => {
    setBusy(true);
    setErr("");
    try {
      const run = await api<Run>("POST", `/api/apps/${app.id}/commands/${encodeURIComponent(c.id)}/run`, reauth ? { password } : undefined);
      setActive(run);
      setConfirm(null);
      runs.reload();
    } catch (e) {
      const m = (e as ApiError).message;
      if (confirm) setErr(m);
      else toast(m, "err");
    }
    setBusy(false);
  };
  const openRun = async (id: string) => {
    try {
      setActive(await api<Run>("GET", `/api/runs/${id}`));
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const openEditor = async (c?: CommandView) => {
    setErr("");
    if (!c) return setEditor({ f: { ...blankDef } });
    try {
      const d = await api<CommandDef>("GET", `/api/commands/${c.id}`);
      setEditor({ id: d.id, f: { name: d.name, command: d.command, cwd: d.cwd, timeoutSec: d.timeoutSec, confirm: d.confirm, env: d.env.join("\n"), password: "" } });
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const saveEditor = async () => {
    if (!editor) return;
    setBusy(true);
    setErr("");
    const { password: pw, env, ...rest } = editor.f;
    const body = { ...rest, env: env.split("\n").map((l) => l.trim()).filter(Boolean), password: pw };
    try {
      if (editor.id) await api("PUT", `/api/commands/${editor.id}`, body);
      else await api("POST", `/api/apps/${app.id}/commands`, body);
      toast(editor.id ? "Command updated" : "Command added");
      setEditor(null);
      commands.reload();
    } catch (e) {
      setErr((e as ApiError).message);
    }
    setBusy(false);
  };
  const remove = async () => {
    if (!del) return;
    try {
      await api("DELETE", `/api/commands/${del.id}`);
      toast(`${del.name} deleted`);
      commands.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setDel(null);
  };

  const list = commands.data ?? [];
  const set = <K extends keyof typeof blankDef>(k: K, v: (typeof blankDef)[K]) => setEditor((e) => (e ? { ...e, f: { ...e.f, [k]: v } } : e));

  return (
    <div className="space-y-6">
      {offline && <div className="rounded-2xl bg-warn-soft px-5 py-3 text-sm text-warn">The agent on this server is not connected. Commands can be run once it reconnects.</div>}
      <div className="grid gap-6 xl:grid-cols-5">
        <Card className="xl:col-span-2">
          <SectionTitle icon={<Terminal size={15} />} title="Commands" sub={app.manager === "standalone" ? "Standalone apps have no built-in actions" : `Built-ins go through ${app.manager}`} action={<Button size="sm" variant="secondary" onClick={() => openEditor()}><Plus size={13} /> Add command</Button>} />
          {!commands.data ? (
            <Spinner />
          ) : list.length === 0 ? (
            <p className="text-sm text-muted">No commands yet. Add predefined commands such as <span className="font-mono">npm run build</span> or <span className="font-mono">git pull --ff-only</span>.</p>
          ) : (
            <ul className="divide-y divide-line">
              {list.map((c) => (
                <li key={c.id} className="flex items-start gap-3 py-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{c.name}</span>
                      {c.kind === "builtin" ? <Badge>built-in</Badge> : <Badge tone="info">custom</Badge>}
                      {c.danger && <Badge tone="crit">disruptive</Badge>}
                    </div>
                    <code className="mt-1 block truncate font-mono text-[11px] text-muted" title={c.preview}>{c.preview}</code>
                    {c.lastRun && <button onClick={() => openRun(c.lastRun!.id)} className="mt-1 text-[11px] text-muted hover:text-fg">last run {relTime(c.lastRun.startedAt)} · <span className={cn(c.lastRun.status === "succeeded" ? "text-ok" : c.lastRun.status === "running" ? "text-info" : "text-crit")}>{c.lastRun.status}</span></button>}
                  </div>
                  {c.kind === "custom" && (
                    <>
                      <button onClick={() => openEditor(c)} className="mt-1 text-muted hover:text-fg" aria-label={`Edit ${c.name}`}><Pencil size={14} /></button>
                      <button onClick={() => setDel(c)} className="mt-1 text-muted hover:text-crit" aria-label={`Delete ${c.name}`}><Trash2 size={14} /></button>
                    </>
                  )}
                  <Button size="sm" variant={c.danger ? "danger" : "secondary"} disabled={offline || busy} onClick={() => ask(c)}><Play size={13} /> Run</Button>
                </li>
              ))}
            </ul>
          )}
        </Card>
        <Card className="xl:col-span-3">
          {active ? (
            <>
              <SectionTitle icon={<Play size={15} />} title={active.name} sub={`${active.preview}${active.runAs ? ` · as ${active.runAs}` : ""}`} action={<RunStatus status={active.status} exitCode={active.exitCode} />} />
              <Output run={active} />
              <p className="mt-2 text-xs text-muted">Started {dateTime(active.startedAt)} by {active.initiatedBy}{active.finishedAt ? ` · took ${((active.finishedAt - active.startedAt) / 1000).toFixed(1)}s` : ""}</p>
            </>
          ) : (
            <EmptyState icon={<Terminal size={20} />} title="Run a command to see its output here" desc="Output streams live from the agent. Only predefined commands can run; there is no free-form shell." />
          )}
        </Card>
      </div>

      <Card pad={false}>
        <div className="p-5 pb-0"><SectionTitle icon={<History size={15} />} title="Execution history" sub="Last 50 runs, kept with their output" /></div>
        {(runs.data ?? []).length === 0 ? (
          <p className="p-5 text-sm text-muted">No runs yet.</p>
        ) : (
          <Table head={["Started", "Command", "Status", "Duration", "By", ""]}>
            {(runs.data ?? []).map((r) => (
              <Tr key={r.id} onClick={() => openRun(r.id)}>
                <Td className="whitespace-nowrap text-xs">{dateTime(r.startedAt)}</Td>
                <Td><div className="font-medium">{r.name}</div><div className="max-w-[320px] truncate font-mono text-[11px] text-muted">{r.preview}</div></Td>
                <Td><RunStatus status={r.status} exitCode={r.exitCode} /></Td>
                <Td className="whitespace-nowrap text-xs text-muted">{r.finishedAt ? `${((r.finishedAt - r.startedAt) / 1000).toFixed(1)}s` : "—"}</Td>
                <Td className="whitespace-nowrap text-xs text-muted">{r.initiatedBy}</Td>
                <Td className="text-xs text-muted">View</Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>

      <Modal open={!!confirm} onClose={() => setConfirm(null)} title={`Run “${confirm?.name}”?`} footer={<><Button variant="secondary" onClick={() => setConfirm(null)}>Cancel</Button><Button variant={confirm?.danger ? "danger" : "primary"} disabled={busy || (reauth && !password)} onClick={() => confirm && start(confirm)}>{busy && <Spinner size={14} />} Run</Button></>}>
        {confirm && (
          <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); start(confirm); }}>
            <p className="text-sm text-muted">{confirm.description || `Runs in ${app.path}${confirm.cwd ? "/" + confirm.cwd : ""} as the application's user.`}</p>
            <code className="block break-all rounded-xl bg-surface2 px-3 py-2 font-mono text-xs">{confirm.preview}</code>
            {reauth && <Field label="Your password" hint="Required by Security settings"><Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus /></Field>}
            {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>}
          </form>
        )}
      </Modal>

      <Modal open={!!editor} onClose={() => setEditor(null)} wide title={editor?.id ? "Edit command" : "Add command"} footer={<><Button variant="secondary" onClick={() => setEditor(null)}>Cancel</Button><Button onClick={saveEditor} disabled={busy || !editor?.f.password}>{busy && <Spinner size={14} />} Save</Button></>}>
        {editor && (
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="flex gap-2 rounded-2xl bg-warn-soft px-4 py-3 text-xs text-warn sm:col-span-2">
              <ShieldAlert size={16} className="shrink-0" />
              <span>This text runs through <span className="font-mono">/bin/sh</span> on the server, as the application's user, inside <span className="font-mono">{app.path || "the app directory"}</span>. Only define commands you would run yourself.</span>
            </div>
            <Field label="Name"><Input value={editor.f.name} maxLength={60} onChange={(e) => set("name", e.target.value)} placeholder="Build" /></Field>
            <Field label="Subdirectory (optional)" hint="Relative to the app directory"><Input value={editor.f.cwd} onChange={(e) => set("cwd", e.target.value)} placeholder="web" className="font-mono" /></Field>
            <Field label="Command" className="sm:col-span-2"><Textarea rows={3} value={editor.f.command} onChange={(e) => set("command", e.target.value)} placeholder="npm ci && npm run build" className="font-mono" /></Field>
            <Field label="Timeout (seconds)"><Input type="number" min={5} max={3600} value={editor.f.timeoutSec} onChange={(e) => set("timeoutSec", Number(e.target.value))} /></Field>
            <div className="flex items-center justify-between gap-3 pt-6"><span className="text-sm">Ask for confirmation before running</span><Toggle checked={editor.f.confirm} onChange={(v) => set("confirm", v)} /></div>
            <Field label="Environment (optional)" hint="One NAME=value per line. Saved values show as •••••• and are kept unless you replace them." className="sm:col-span-2"><Textarea rows={3} value={editor.f.env} onChange={(e) => set("env", e.target.value)} placeholder="NODE_ENV=production" className="font-mono" /></Field>
            <Field label="Your password" hint="Required to save commands" className="sm:col-span-2"><Input type="password" autoComplete="current-password" value={editor.f.password} onChange={(e) => set("password", e.target.value)} /></Field>
            {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit sm:col-span-2">{err}</div>}
          </div>
        )}
      </Modal>
      <ConfirmDialog open={!!del} danger title={`Delete “${del?.name}”?`} confirmLabel="Delete" message="Its run history stays available." onCancel={() => setDel(null)} onConfirm={remove} />
    </div>
  );
}
