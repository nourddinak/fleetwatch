import { useCallback, useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Container, FileText, Play, RefreshCw, RotateCw, Square, Trash2, Download } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, LoadingState, Modal, Segmented, Spinner, Table, Td, Tr, type Tone } from "../components/ui";
import { fmtBytes } from "../components/DiskRescue";
import { cn } from "../utils/cn";

interface Container {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  ports: string;
  created: string;
  project?: string;
  service?: string;
  cpu: number;
  memPct: number;
  memUsed: number;
  memLimit: number;
}
interface Image {
  id: string;
  repo: string;
  tag: string;
  size: number;
  created: string;
  dangling: boolean;
  inUse: boolean;
}
interface Volume {
  name: string;
  driver: string;
  inUse: boolean;
}
interface Project {
  name: string;
  status: string;
  configFiles: string;
}
interface Action {
  runId: string;
  kind: string;
  target: string;
  action: string;
}
interface DockerState {
  report: { at: number; available: boolean; version?: string; error?: string; containers: Container[]; images: Image[]; volumes: Volume[]; projects: Project[] } | null;
  loading: boolean;
  running: Action[];
}

const STATE_TONE: Record<string, Tone> = { running: "ok", exited: "neutral", paused: "warn", restarting: "warn", created: "neutral", dead: "crit" };
const VERB: Record<string, string> = { start: "Starting", stop: "Stopping", restart: "Restarting", update: "Updating", remove: "Removing", prune: "Cleaning up" };

export function ServerDocker() {
  const { id = "" } = useParams();
  const { me, toast, subscribeLive, getServer } = useStore();
  const owner = !me || me.role === "owner";
  const canAct = owner || me?.role === "operator";
  const st = useApi<DockerState>(`/api/servers/${id}/docker`);
  const { setData } = st;
  const [tab, setTab] = useState<"containers" | "stacks" | "images" | "volumes">("containers");
  const [logs, setLogs] = useState<{ name: string; text: string | null } | null>(null);
  const [confirm, setConfirm] = useState<{ kind: string; target: string; action: string; title: string; message: string } | null>(null);
  const refresh = useCallback(() => {
    api("POST", `/api/servers/${id}/docker/refresh`).catch((e: ApiError) => toast(e.message, "err"));
  }, [id, toast]);
  useEffect(() => {
    refresh();
    const t = setInterval(() => document.visibilityState === "visible" && refresh(), 20_000);
    return () => clearInterval(t);
  }, [refresh]);
  useEffect(
    () =>
      subscribeLive((m) => {
        if (m.data?.serverId !== id) return;
        if (m.type === "docker") setData(m.data.state);
        if (m.type === "docker.result") {
          const r = m.data.result;
          toast(r.ok ? `Done: ${r.action} ${r.target || "images"}` : `${r.action} ${r.target} failed: ${r.error}`, r.ok ? "ok" : "err");
        }
      }),
    [subscribeLive, setData, id, toast],
  );
  const act = async (kind: string, target: string, action: string) => {
    const destructive = action === "remove" || action === "prune";
    try {
      await api("POST", `/api/servers/${id}/docker/${destructive ? "delete" : "action"}`, { kind, target, action });
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const showLogs = async (c: Container) => {
    setLogs({ name: c.name, text: null });
    try {
      const r = await api<{ ok: boolean; output?: string; error?: string }>("GET", `/api/servers/${id}/docker/logs?container=${encodeURIComponent(c.id)}`);
      setLogs({ name: c.name, text: r.output || r.error || "(no output)" });
    } catch (e) {
      setLogs({ name: c.name, text: (e as ApiError).message });
    }
  };
  if (st.error) return <ErrorState message={st.error} onRetry={st.reload} />;
  const rep = st.data?.report;
  if (!rep) return st.data && !st.data.loading ? <EmptyState icon={<Container size={20} />} title="No Docker information yet" desc="The agent answers in a few seconds; Docker needs agent v0.9.38 or newer." action={<Button onClick={refresh}>Ask again</Button>} /> : <LoadingState label="Asking the agent about Docker…" />;
  if (!rep.available) return <EmptyState icon={<Container size={20} />} title="Docker is not installed" desc={`No docker command on ${getServer(id)?.name ?? "this server"}.`} />;
  const busy = (kind: string, target: string) => st.data!.running.find((a) => a.kind === kind && a.target === target);
  const running = rep.containers.filter((c) => c.state === "running").length;
  const dangling = rep.images.filter((i) => i.dangling);
  const actions = (kind: string, target: string, isRunning: boolean, extra?: React.ReactNode) => {
    const b = busy(kind, target);
    if (b) return <span className="flex items-center gap-1.5 text-xs text-muted"><Spinner size={12} /> {VERB[b.action] ?? b.action}…</span>;
    if (!canAct) return extra ?? null;
    return (
      <div className="flex items-center justify-end gap-0.5">
        {isRunning ? (
          <>
            <Button size="sm" variant="ghost" title="Restart" onClick={() => act(kind, target, "restart")}><RotateCw size={14} /></Button>
            <Button size="sm" variant="ghost" title="Stop" onClick={() => setConfirm({ kind, target, action: "stop", title: `Stop ${target}?`, message: "It stops serving until you start it again." })}><Square size={14} /></Button>
          </>
        ) : (
          <Button size="sm" variant="ghost" title="Start" onClick={() => act(kind, target, "start")}><Play size={14} /></Button>
        )}
        <Button size="sm" variant="ghost" title="Pull the newest image and recreate" onClick={() => setConfirm({ kind, target, action: "update", title: `Update ${target}?`, message: "Pulls the newest image and recreates it with docker compose (a few seconds of downtime)." })}><Download size={14} /></Button>
        {extra}
      </div>
    );
  };
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Badge tone="ok">{running} running</Badge>
          <Badge>{rep.containers.length - running} stopped</Badge>
          <span className="text-xs text-muted">Docker {rep.version} · updated {relTime(rep.at)}</span>
        </div>
        <Button size="sm" variant="secondary" onClick={refresh} disabled={st.data?.loading}>{st.data?.loading ? <Spinner size={14} /> : <RefreshCw size={14} />} Refresh</Button>
      </div>
      {rep.error && <div className="rounded-2xl bg-crit-soft px-4 py-3 text-sm text-crit">{rep.error}</div>}
      <Segmented value={tab} onChange={setTab} options={[
        { value: "containers", label: `Containers (${rep.containers.length})` },
        { value: "stacks", label: `Compose stacks (${rep.projects.length})` },
        { value: "images", label: `Images (${rep.images.length})` },
        { value: "volumes", label: `Volumes (${rep.volumes.length})` },
      ]} />
      {tab === "containers" && (
        <Card pad={false}>
          {rep.containers.length === 0 ? <div className="p-5"><EmptyState title="No containers" /></div> : (
            <Table head={["Container", "Image", "State", "CPU", "Memory", "Ports", ""]}>
              {rep.containers.map((c) => (
                <Tr key={c.id}>
                  <Td>
                    <div className="font-medium">{c.name}</div>
                    {c.project && <div className="text-[11px] text-muted">{c.project} · {c.service}</div>}
                  </Td>
                  <Td className="max-w-[14rem] truncate font-mono text-xs" title={c.image}>{c.image}</Td>
                  <Td><Badge tone={STATE_TONE[c.state] ?? "neutral"} dot>{c.state}</Badge><div className="mt-0.5 text-[11px] text-muted">{c.status}</div></Td>
                  <Td>{c.state === "running" ? `${c.cpu.toFixed(1)}%` : "—"}</Td>
                  <Td>{c.state === "running" ? <span title={`${c.memPct.toFixed(1)}% of ${fmtBytes(c.memLimit)}`}>{fmtBytes(c.memUsed)}</span> : "—"}</Td>
                  <Td className="max-w-[12rem] truncate text-xs text-muted" title={c.ports}>{c.ports || "—"}</Td>
                  <Td className="text-right">
                    {actions("container", c.name, c.state === "running", (
                      <>
                        <Button size="sm" variant="ghost" title="Logs" onClick={() => showLogs(c)}><FileText size={14} /></Button>
                        {owner && c.state !== "running" && <Button size="sm" variant="ghost" title="Remove" onClick={() => setConfirm({ kind: "container", target: c.name, action: "remove", title: `Remove ${c.name}?`, message: "The stopped container is deleted. Its named volumes stay." })}><Trash2 size={14} /></Button>}
                      </>
                    ))}
                  </Td>
                </Tr>
              ))}
            </Table>
          )}
        </Card>
      )}
      {tab === "stacks" && (
        <Card pad={false}>
          {rep.projects.length === 0 ? <div className="p-5"><EmptyState title="No docker compose stacks" /></div> : (
            <Table head={["Stack", "Status", "Compose file", ""]}>
              {rep.projects.map((p) => (
                <Tr key={p.name}>
                  <Td className="font-medium">{p.name}</Td>
                  <Td><Badge tone={p.status.startsWith("running") ? "ok" : "neutral"} dot>{p.status}</Badge></Td>
                  <Td className="max-w-[18rem] truncate font-mono text-xs text-muted" title={p.configFiles}>{p.configFiles}</Td>
                  <Td className="text-right">{actions("project", p.name, p.status.startsWith("running"))}</Td>
                </Tr>
              ))}
            </Table>
          )}
        </Card>
      )}
      {tab === "images" && (
        <Card pad={false}>
          {owner && dangling.length > 0 && (
            <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line p-4 text-sm">
              <span>{dangling.length} unused image layers ({fmtBytes(dangling.reduce((n, i) => n + i.size, 0))}) left over from older builds.</span>
              <Button size="sm" variant="secondary" disabled={!!busy("image", "")} onClick={() => act("image", "", "prune")}>{busy("image", "") ? <Spinner size={14} /> : <Trash2 size={14} />} Clean up</Button>
            </div>
          )}
          <Table head={["Image", "Size", "Created", "", ""]}>
            {rep.images.map((i) => (
              <Tr key={i.id + i.tag}>
                <Td className="font-mono text-xs">{i.dangling ? <span className="text-muted">{i.id.replace("sha256:", "").slice(0, 12)}</span> : `${i.repo}:${i.tag}`}</Td>
                <Td>{fmtBytes(i.size)}</Td>
                <Td className="text-xs text-muted">{i.created}</Td>
                <Td>{i.inUse ? <Badge tone="ok">in use</Badge> : <Badge>{i.dangling ? "dangling" : "unused"}</Badge>}</Td>
                <Td className="text-right">
                  {owner && !i.inUse && (busy("image", i.id) ? <Spinner size={14} /> : <Button size="sm" variant="ghost" title="Remove" onClick={() => setConfirm({ kind: "image", target: i.id, action: "remove", title: "Remove this image?", message: `${i.dangling ? i.id : `${i.repo}:${i.tag}`} is deleted; it can be pulled again later.` })}><Trash2 size={14} /></Button>)}
                </Td>
              </Tr>
            ))}
          </Table>
        </Card>
      )}
      {tab === "volumes" && (
        <Card pad={false}>
          <Table head={["Volume", "Driver", "", ""]}>
            {rep.volumes.map((v) => (
              <Tr key={v.name}>
                <Td className="max-w-[22rem] truncate font-mono text-xs" title={v.name}>{v.name}</Td>
                <Td className="text-xs text-muted">{v.driver}</Td>
                <Td>{v.inUse ? <Badge tone="ok">in use</Badge> : <Badge tone="warn">not used</Badge>}</Td>
                <Td className="text-right">
                  {owner && !v.inUse && (busy("volume", v.name) ? <Spinner size={14} /> : <Button size="sm" variant="ghost" title="Delete" onClick={() => setConfirm({ kind: "volume", target: v.name, action: "remove", title: "Delete this volume and its data?", message: `Everything stored in ${v.name} is deleted for good. No container uses it right now.` })}><Trash2 size={14} /></Button>)}
                </Td>
              </Tr>
            ))}
          </Table>
        </Card>
      )}
      <ConfirmDialog open={!!confirm} title={confirm?.title ?? ""} message={confirm?.message ?? ""} confirmLabel={confirm?.action === "remove" ? "Delete" : "Continue"} danger={confirm?.action === "remove"}
        onConfirm={() => { if (confirm) act(confirm.kind, confirm.target, confirm.action); setConfirm(null); }} onCancel={() => setConfirm(null)} />
      <Modal open={!!logs} onClose={() => setLogs(null)} title={`Logs: ${logs?.name ?? ""}`} wide>
        {logs?.text == null ? <LoadingState label="Reading the last 300 lines…" /> : (
          <pre className={cn("max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded-xl bg-surface2 p-3 font-mono text-[11px] leading-relaxed")}>{logs.text}</pre>
        )}
      </Modal>
    </div>
  );
}
