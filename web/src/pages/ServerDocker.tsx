import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowUpCircle, Check, Container, Download, ExternalLink, FileCode2, FileText, Globe, HardDrive, Layers, Lock, Package, Play, RefreshCw, RotateCw, Square, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, Input, LoadingState, Modal, Segmented, Select, Spinner, Table, Td, Tr, type Tone } from "../components/ui";
import { fmtBytes } from "../components/DiskRescue";
import { cn } from "../utils/cn";
import { parsePorts, type PortMap } from "../utils/ports";
import { webServers } from "../utils/web";

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
  digests?: string[];
  update?: "available" | "current"; // whether the image tag points to a newer image
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
interface Route {
  host: string;
  port: number;
  server?: string; // "" Caddy | "nginx"
}
interface DockerState {
  report: { at: number; available: boolean; version?: string; error?: string; containers: Container[]; images: Image[]; volumes: Volume[]; projects: Project[] } | null;
  loading: boolean;
  running: Action[];
}

const STATE_TONE: Record<string, Tone> = { running: "ok", exited: "neutral", paused: "warn", restarting: "warn", created: "neutral", dead: "crit" };
const VERB: Record<string, string> = { start: "Starting", stop: "Stopping", restart: "Restarting", update: "Updating", remove: "Removing", prune: "Cleaning up",
  delete: "Deleting", down: "Taking down", install: "Installing Docker", uninstall: "Uninstalling Docker", purge: "Uninstalling Docker" };
const DESTRUCTIVE = ["remove", "prune", "delete", "down", "install", "uninstall", "purge"];

/** A server's Docker: its containers (all of them, however they were started), compose
 * stacks, images and volumes, and installing or removing Docker itself. */
export function ServerDocker({ serverId }: { serverId?: string } = {}) {
  const params = useParams();
  const id = serverId ?? params.id ?? "";
  const { me, toast, subscribeLive, getServer } = useStore();
  const owner = !me || me.role === "owner";
  const canAct = owner || me?.role === "operator";
  const st = useApi<DockerState>(`/api/servers/${id}/docker`);
  const routes = useApi<Route[]>(`/api/servers/${id}/routes`);
  const [domainFor, setDomainFor] = useState<{ name: string; ports: number[] } | null>(null);
  const srv = getServer(id);
  const caddy = !!srv && (webServers(srv)?.includes("Caddy") ?? true); // older agents do not say
  const { setData } = st;
  const [tab, setTab] = useState<"containers" | "stacks" | "images" | "volumes">("containers");
  const [logs, setLogs] = useState<{ name: string; text: string | null } | null>(null);
  const [confirm, setConfirm] = useState<{ kind: string; target: string; action: string; title: string; message: string } | null>(null);
  const refresh = useCallback(() => {
    api("POST", `/api/servers/${id}/docker/refresh`).catch((e: ApiError) => toast(e.message, "err"));
  }, [id, toast]);
  const checkUpdates = () => {
    api("POST", `/api/servers/${id}/docker/updates`).then(() => toast("Asking the registries for newer images…")).catch((e: ApiError) => toast(e.message, "err"));
  };
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
  const [uninstall, setUninstall] = useState(false);
  const act = async (kind: string, target: string, action: string) => {
    const destructive = DESTRUCTIVE.includes(action);
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
  const busy = (kind: string, target: string) => st.data!.running.find((a) => a.kind === kind && a.target === target);
  const engine = st.data!.running.find((a) => a.kind === "engine");
  if (engine) return <LoadingState label={`${VERB[engine.action]} on ${getServer(id)?.name ?? "the server"}: this takes a few minutes…`} />;
  if (!rep.available) {
    return (
      <EmptyState icon={<Container size={20} />} title="Docker is not installed" desc={`No docker command on ${getServer(id)?.name ?? "this server"}. Fleetwatch can install it with Docker's own install script (Docker Engine and Compose).`}
        action={owner ? <Button onClick={() => act("engine", "", "install")}><Download size={15} /> Install Docker</Button> : undefined} />
    );
  }
  const running = rep.containers.filter((c) => c.state === "running").length;
  const updates = rep.containers.filter((c) => c.update === "available").length;
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
          {updates > 0 && <Badge tone="warn"><ArrowUpCircle size={11} /> {updates} update{updates === 1 ? "" : "s"} available</Badge>}
          <span className="text-xs text-muted">Docker {rep.version} · updated {relTime(rep.at)}</span>
        </div>
        <div className="flex gap-2">
          {owner && <Button size="sm" variant="ghost" onClick={() => setUninstall(true)} title="Remove Docker from this server"><Trash2 size={14} /> Uninstall</Button>}
          <Button size="sm" variant="ghost" onClick={checkUpdates} title="Ask Docker Hub, GHCR and the other registries whether the images have newer versions"><ArrowUpCircle size={14} /> Check for updates</Button>
          <Button size="sm" variant="secondary" onClick={refresh} disabled={st.data?.loading}>{st.data?.loading ? <Spinner size={14} /> : <RefreshCw size={14} />} Refresh</Button>
        </div>
      </div>
      {rep.error && <div className="rounded-2xl bg-crit-soft px-4 py-3 text-sm text-crit">{rep.error}</div>}
      <Segmented value={tab} onChange={setTab} options={[
        { value: "containers", label: <span className="inline-flex items-center gap-1.5"><Container size={13} />Containers ({rep.containers.length})</span> },
        { value: "stacks", label: <span className="inline-flex items-center gap-1.5"><Layers size={13} />Compose stacks ({rep.projects.length})</span> },
        { value: "images", label: <span className="inline-flex items-center gap-1.5"><Package size={13} />Images ({rep.images.length})</span> },
        { value: "volumes", label: <span className="inline-flex items-center gap-1.5"><HardDrive size={13} />Volumes ({rep.volumes.length})</span> },
      ]} />
      {tab === "containers" && (
        <Card pad={false}>
          {rep.containers.length === 0 ? <div className="p-5"><EmptyState title="No containers" /></div> : (
            <Table head={["Container", "Image", "State", "CPU", "Memory", "Address and ports", ""]}>
              {rep.containers.map((c) => {
                const ports = parsePorts(c.ports);
                const hostPorts = ports.flatMap((x) => (x.host ? [x.host] : []));
                return (
                <Tr key={c.id}>
                  <Td>
                    <div className="font-medium">{c.name}</div>
                    {c.project && <div className="text-[11px] text-muted">{c.project} · {c.service}</div>}
                  </Td>
                  <Td className="max-w-[14rem]">
                    <div className="truncate font-mono text-xs" title={c.image}>{c.image}</div>
                    {c.update === "available" ? (
                      <button className="mt-1" title="Pull the newest image and recreate"
                        onClick={() => setConfirm({ kind: "container", target: c.name, action: "update", title: `Update ${c.name}?`, message: `${c.image} has a newer image. This pulls it and recreates the container${c.project ? " with docker compose" : ""} (a few seconds of downtime).` })}>
                        <Badge tone="warn"><ArrowUpCircle size={11} /> Update available</Badge>
                      </button>
                    ) : c.update === "current" ? (
                      <div className="mt-0.5 flex items-center gap-1 text-[11px] text-muted"><Check size={11} /> Up to date</div>
                    ) : null}
                  </Td>
                  <Td><Badge tone={STATE_TONE[c.state] ?? "neutral"} dot>{c.state}</Badge><div className="mt-0.5 text-[11px] text-muted">{c.status}</div></Td>
                  <Td>{c.state === "running" ? `${c.cpu.toFixed(1)}%` : "—"}</Td>
                  <Td>{c.state === "running" ? <span title={`${c.memPct.toFixed(1)}% of ${fmtBytes(c.memLimit)}`}>{fmtBytes(c.memUsed)}</span> : "—"}</Td>
                  <Td className="min-w-[12rem]"><Address ports={ports} raw={c.ports} ip={srv?.ip} routes={(routes.data ?? []).filter((r) => hostPorts.includes(r.port))} /></Td>
                  <Td className="text-right">
                    {actions("container", c.name, c.state === "running", (
                      <>
                        {owner && caddy && hostPorts.length > 0 && <Button size="sm" variant="ghost" title="Give it a domain with Caddy (HTTPS)" onClick={() => setDomainFor({ name: c.name, ports: [...new Set(hostPorts)] })}><Globe size={14} /></Button>}
                        <Button size="sm" variant="ghost" title="Logs" onClick={() => showLogs(c)}><FileText size={14} /></Button>
                        {owner && c.state !== "running" && <Button size="sm" variant="ghost" title="Remove" onClick={() => setConfirm({ kind: "container", target: c.name, action: "remove", title: `Remove ${c.name}?`, message: "The stopped container is deleted. Its named volumes stay." })}><Trash2 size={14} /></Button>}
                        {owner && c.state === "running" && <Button size="sm" variant="ghost" title="Stop and delete" onClick={() => setConfirm({ kind: "container", target: c.name, action: "delete", title: `Stop and delete ${c.name}?`, message: `It stops and is deleted. Its named volumes stay.${c.project ? ` It belongs to the compose stack ${c.project}: docker compose up would make it again.` : ""}` })}><Trash2 size={14} /></Button>}
                      </>
                    ))}
                  </Td>
                </Tr>
                );
              })}
            </Table>
          )}
        </Card>
      )}
      {domainFor && <DomainDialog serverId={id} ip={srv?.ip} container={domainFor.name} ports={domainFor.ports} onClose={() => setDomainFor(null)} onDone={() => setTimeout(routes.reload, 4000)} />}
      {tab === "stacks" && (
        <Card pad={false}>
          {rep.projects.length === 0 ? <div className="p-5"><EmptyState title="No docker compose stacks" /></div> : (
            <Table head={["Stack", "Status", "Compose file", ""]}>
              {rep.projects.map((p) => (
                <Tr key={p.name}>
                  <Td className="font-medium">{p.name}</Td>
                  <Td><Badge tone={p.status.startsWith("running") ? "ok" : "neutral"} dot>{p.status}</Badge></Td>
                  <Td className="max-w-[18rem] truncate font-mono text-xs text-muted" title={p.configFiles}>{p.configFiles}</Td>
                  <Td className="text-right">{actions("project", p.name, p.status.startsWith("running"), owner ? (
                    <Button size="sm" variant="ghost" title="Take down" onClick={() => setConfirm({ kind: "project", target: p.name, action: "down", title: `Take ${p.name} down?`, message: "docker compose down: its containers and networks are deleted. Named volumes (its data) and images stay; start it again from its folder with docker compose up." })}><Trash2 size={14} /></Button>
                  ) : undefined)}</Td>
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
      <Modal open={uninstall} onClose={() => setUninstall(false)} title={`Uninstall Docker from ${getServer(id)?.name ?? "this server"}?`}
        footer={<Button variant="ghost" onClick={() => setUninstall(false)}>Cancel</Button>}>
        <div className="space-y-3 text-sm">
          <p className="text-muted">Every container stops. Choose what happens to their data:</p>
          <button className="w-full rounded-xl border border-line p-3 text-left hover:bg-surface2" onClick={() => { setUninstall(false); act("engine", "", "uninstall"); }}>
            <div className="font-medium">Uninstall, keep the data</div>
            <div className="text-xs text-muted">Docker's packages go; containers, images and volumes stay in /var/lib/docker, back if Docker is installed again.</div>
          </button>
          <button className="w-full rounded-xl border border-crit/40 p-3 text-left hover:bg-crit-soft" onClick={() => { setUninstall(false); act("engine", "", "purge"); }}>
            <div className="font-medium text-crit">Uninstall and delete everything</div>
            <div className="text-xs text-muted">Also deletes every container, image and volume, with the data in them. This cannot be undone.</div>
          </button>
        </div>
      </Modal>
      <ConfirmDialog open={!!confirm} title={confirm?.title ?? ""} message={confirm?.message ?? ""} confirmLabel={["remove", "delete", "down"].includes(confirm?.action ?? "") ? "Delete" : "Continue"} danger={["remove", "delete", "down"].includes(confirm?.action ?? "")}
        onConfirm={() => { if (confirm) act(confirm.kind, confirm.target, confirm.action); setConfirm(null); }} onCancel={() => setConfirm(null)} />
      <Modal open={!!logs} onClose={() => setLogs(null)} title={`Logs: ${logs?.name ?? ""}`} wide>
        {logs?.text == null ? <LoadingState label="Reading the last 300 lines…" /> : (
          <pre className={cn("max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded-xl bg-surface2 p-3 font-mono text-[11px] leading-relaxed")}>{logs.text}</pre>
        )}
      </Modal>
    </div>
  );
}

/** Where a container is reached: its domains (from Caddy or Nginx), then each port, with
 * a link for those open on the server's address. */
function Address({ ports, raw, ip, routes }: { ports: PortMap[]; raw: string; ip?: string; routes: Route[] }) {
  if (ports.length === 0 && routes.length === 0) return <span className="text-xs text-muted" title={raw || "No port published"}>{raw ? raw : "No port"}</span>;
  return (
    <div className="space-y-1 text-xs">
      {routes.map((r) => (
        <a key={r.host} href={`https://${r.host}`} target="_blank" rel="noreferrer" className="flex items-center gap-1 font-medium text-fg hover:underline" title={`${r.server === "nginx" ? "Nginx" : "Caddy"} sends ${r.host} to port ${r.port}`}>
          <Lock size={11} className="shrink-0 text-ok" /><span className="truncate">{r.host}</span><ExternalLink size={10} className="shrink-0 text-muted" />
        </a>
      ))}
      <div className="flex flex-wrap gap-1">
        {ports.map((x) => {
          const label = x.host ? `${x.host} → ${x.container}` : x.container;
          const tip = !x.host ? `Port ${x.container}/${x.proto} only inside Docker (other containers reach it; the server does not publish it)`
            : x.local ? `Published on the server's 127.0.0.1:${x.host} only: reachable through Caddy or from the server itself, not from outside`
            : `Open on the server's port ${x.host} to anyone (container port ${x.container}/${x.proto})`;
          const cls = "inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 font-mono text-[11px]";
          if (x.host && !x.local && ip && x.proto === "tcp") {
            return <a key={label + x.proto} href={`http://${ip}:${x.host}`} target="_blank" rel="noreferrer" title={tip} className={cn(cls, "bg-warn/10 text-warn hover:underline")}>{label}<ExternalLink size={9} /></a>;
          }
          return <span key={label + x.proto} title={tip} className={cn(cls, x.host ? "bg-surface2 text-fg" : "bg-surface2 text-muted")}>{x.local && <Lock size={9} />}{label}{x.proto !== "tcp" && `/${x.proto}`}</span>;
        })}
      </div>
    </div>
  );
}

const hostRe = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$/;

/** Gives a container a domain: a site block added to the server's Caddyfile, which the
 * agent checks with Caddy before keeping it, then reloads. Caddy gets the HTTPS certificate. */
function DomainDialog({ serverId, ip, container, ports, onClose, onDone }: { serverId: string; ip?: string; container: string; ports: number[]; onClose: () => void; onDone: () => void }) {
  const { toast } = useStore();
  const [domain, setDomain] = useState("");
  const [port, setPort] = useState(ports[0]);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const host = domain.trim().toLowerCase().replace(/^https?:\/\//, "").replace(/\/.*$/, "");
  const block = `${host || "app.example.com"} {\n\tencode zstd gzip\n\treverse_proxy localhost:${port}\n}`;
  const save = async () => {
    setBusy(true);
    try {
      const cur = await api<{ content: string; error?: string }>("POST", `/api/servers/${serverId}/web`, { password, web: "caddy" });
      if (cur.error) throw new Error(cur.error);
      if (cur.content.split("\n").some((l) => l.trim().replace(/[{,]+$/, "").split(/[\s,]+/).includes(host))) throw new Error(`${host} is in the Caddyfile already: change it there.`);
      const content = `${cur.content.replace(/\s*$/, "")}\n\n# ${container} (Docker), added by Fleetwatch\n${block}\n`;
      const res = await api<{ error?: string }>("PUT", `/api/servers/${serverId}/web`, { password, web: "caddy", content });
      if (res.error) throw new Error(res.error);
      api("POST", "/api/discoveries/scan", { serverId }).catch(() => {});
      toast(`Caddy now serves https://${host}`);
      onDone();
      onClose();
    } catch (e) {
      toast((e as ApiError).message ?? String(e), "err");
    }
    setBusy(false);
  };
  return (
    <Modal open onClose={onClose} title={`A domain for ${container}`}
      footer={<>
        <Link to={`/servers/${serverId}/web`} className="mr-auto"><Button variant="ghost"><FileCode2 size={14} /> Whole Caddyfile</Button></Link>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button disabled={busy || !hostRe.test(host) || !password} onClick={save}>{busy ? <Spinner size={14} /> : <Globe size={14} />} Add to Caddy</Button>
      </>}>
      <div className="space-y-4">
        <Field label="Domain" hint={<>First point its DNS (an A record) at this server{ip ? <>: <b className="font-mono">{ip}</b></> : ""}. Caddy then gets the HTTPS certificate by itself.</>}>
          <Input value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" autoFocus />
        </Field>
        {ports.length > 1 && (
          <Field label="Port on the server">
            <Select value={port} onChange={(e) => setPort(Number(e.target.value))}>{ports.map((p) => <option key={p} value={p}>{p}</option>)}</Select>
          </Field>
        )}
        <div>
          <div className="mb-1 text-xs font-medium text-muted">Added to /etc/caddy/Caddyfile</div>
          <pre className="rounded-xl bg-surface2 p-3 font-mono text-xs">{block}</pre>
          <p className="mt-1 text-[11px] text-muted">Caddy checks the new file before it is kept; if it refuses, nothing changes.</p>
        </div>
        <Field label="Your password" hint="Changing the server's Caddyfile asks for it.">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
        </Field>
      </div>
    </Modal>
  );
}
