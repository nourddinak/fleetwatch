import { useEffect, useState, type InputHTMLAttributes, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { AlertTriangle, BarChart3, Container, Check, CheckCircle2, Clock, ExternalLink, FileCode2, FolderGit2, GitBranch, GitCommitHorizontal, Globe, Hammer, Hand, History, Info, KeyRound, LayoutDashboard, Link2, Lock, PauseCircle, Pencil, Plus, RefreshCw, Rocket, Save, SearchCheck, Server as ServerIcon, Settings2, Terminal, Trash2, Undo2, UserRound, Wand2, Workflow } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { MetricChart } from "../components/MetricChart";
import { fmtBytes } from "../components/DiskRescue";
import { providerName } from "../components/DnsAccounts";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, Gate, Github, Input, LoadingState, Modal, PageHeader, SearchInput, SectionTitle, Spinner, Textarea, ToggleRow, toneDot, type Tone, Toggle, Segmented, CopyButton } from "../components/ui";
import { cn } from "../utils/cn";

interface Plan {
  kind: "static" | "build" | "docker";
  compose?: string;
  port?: number;
  hostPort?: number;
  framework: string;
  dir: string;
  install: string;
  build: string;
  output: string;
  node: string;
  spa: boolean;
  note?: string;
}
interface Site {
  id: string;
  name: string;
  repo: string;
  branch: string;
  serverId: string;
  plan: Plan;
  domains: string[];
  dnsManual?: boolean; // the person adds the records; Fleetwatch only shows them
  preview: string;
  autoDeploy: boolean;
  envKeys: string[];
  status: "building" | "deploying" | "live" | "failed" | "waiting";
  commit: string;
  message: string;
  deployedAt: number;
  error?: string;
  buildUrl?: string;
  pinned?: string;
  createdAt: number;
  createdBy?: string;
  atPath: boolean;
  mine: boolean; // this viewer may change it (who added it, or the owner)
  caddyConfig?: string; // the Caddy site file the last deploy put in place
  caddySource?: "generated" | "repository" | "custom" | "";
  caddyCustom?: string; // the owner's own config, used on every deploy
  dns?: Record<string, { provider: string; zone: string; status: "pointed" | "updated" | "error"; error?: string; proxied?: boolean; previous?: string; created?: boolean }>;
}
interface MyGitHub {
  connected: boolean; // the owner connected the app
  canDeploy: boolean; // owner or operator
  owner: boolean;
  app?: string;
  link?: { login: string; installations: number[]; linkedAt: number };
}

export async function goGitHub(path: "connect" | "install", toast: (m: string, k?: "ok" | "err") => void) {
  try {
    location.href = (await api<{ url: string }>("POST", `/api/me/github/${path}`)).url;
  } catch (e) {
    toast((e as ApiError).message, "err");
  }
}
interface Deploy {
  ts: number;
  commit: string;
  message: string;
  ok: boolean;
  error?: string;
  log?: string;
  by: string;
  deployed?: string;
}

const STATUS: Record<Site["status"], [Tone, string]> = {
  live: ["ok", "Live"],
  building: ["info", "Building on GitHub"],
  deploying: ["info", "Deploying"],
  failed: ["crit", "Failed"],
  waiting: ["neutral", "Waiting"],
};

/** The sites of one server: its Sites tab. */
export function ServerSites() {
  const { id } = useParams();
  return <Sites serverId={id} />;
}

/** Sites from GitHub; with docker, the Docker apps instead (the Docker page). */
export function Sites({ serverId, docker }: { serverId?: string; docker?: boolean } = {}) {
  const { subscribeLive, toast } = useStore();
  const all = useApi<Site[]>("/api/sites");
  const list = { ...all, data: all.data?.filter((s) => (!serverId || s.serverId === serverId) && (s.plan.kind === "docker") === !!docker) };
  const gh = useApi<MyGitHub>("/api/me/github");
  const g = gh.data;
  const owner = !!g?.owner;
  const linked = !!g && (g.owner || !!g.link);
  const canAdd = !!g && g.connected && g.canDeploy && linked;
  const [params, setParams] = useSearchParams();
  useEffect(() => {
    // Back from GitHub's sign-in.
    const r = params.get("github");
    if (!r) return;
    if (r === "linked") toast("GitHub connected: your repositories are ready to deploy");
    else toast(params.get("message") ?? "The GitHub sign-in did not finish", "err");
    params.delete("github");
    params.delete("message");
    setParams(params, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const unlink = async () => {
    try {
      await api("DELETE", "/api/me/github");
      gh.reload();
      toast("Your GitHub account is no longer linked");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const ghChip = g?.link ? (
    <span className="inline-flex items-center gap-2 rounded-full border border-line bg-surface px-3 py-1.5 text-xs">
      <Github size={13} /> <span className="font-medium">{g.link.login}</span>
      <button className="text-muted hover:text-crit" onClick={unlink} title="Unlink your GitHub account">Unlink</button>
    </span>
  ) : g && !g.owner && g.connected && g.canDeploy ? (
    <Button variant="secondary" size={serverId ? "sm" : "md"} onClick={() => goGitHub("connect", toast)}><Github size={14} /> Connect your GitHub</Button>
  ) : null;
  const { setData } = all;
  const [adding, setAdding] = useState(false);
  const nav = useNavigate();
  useEffect(() => subscribeLive((m) => {
    if (m.type === "site") setData((l) => (l ? (l.some((x) => x.id === m.data.id) ? l.map((x) => (x.id === m.data.id ? m.data : x)) : [...l, m.data]) : l));
    if (m.type === "site.removed") setData((l) => l?.filter((x) => x.id !== m.data.id) ?? l);
  }), [subscribeLive, setData]);
  const connected = g?.connected ?? true;
  return (
    <>
      {docker ? (
        <SectionTitle icon={<Container size={15} />} title="Apps from GitHub" sub="Repositories with a Dockerfile or a compose file, built and run with Docker on the server you pick. Every push deploys again; Caddy puts your domains in front with HTTPS."
          action={<div className="flex flex-wrap items-center gap-2">{ghChip}{canAdd && <Button size="sm" onClick={() => setAdding(true)}><Plus size={13} /> Deploy from GitHub</Button>}</div>} />
      ) : serverId ? (
        <SectionTitle icon={<Rocket size={15} />} title="Sites on this server" sub="Static sites deployed here from GitHub, in /var/www."
          action={<div className="flex flex-wrap items-center gap-2">{ghChip}{canAdd && <Button size="sm" onClick={() => setAdding(true)}><Plus size={13} /> New site</Button>}</div>} />
      ) : (
        <PageHeader title="Sites" sub="Static sites from your GitHub repositories, served by Caddy from /var/www on the server you pick. Plain HTML is published as is; React, Vite, Next and the like are built by GitHub Actions. Every push deploys again."
          actions={<>{ghChip}{canAdd && <Button onClick={() => setAdding(true)}><Plus size={15} /> New site</Button>}</>} />
      )}
      <Gate>
        {list.error ? <ErrorState message={list.error} onRetry={list.reload} /> : !list.data ? <LoadingState /> : !connected ? (
          owner ? (
            <EmptyState icon={<Rocket size={20} />} title="Connect GitHub first" desc={docker ? "Apps are deployed from GitHub repositories." : "Sites are deployed from GitHub repositories."} action={<Link to="/integrations?tab=github"><Button>Open Integrations</Button></Link>} />
          ) : (
            <EmptyState icon={<Github size={20} />} title="GitHub is not set up yet" desc="The owner connects it once in Integrations (sidebar). Then you link your own GitHub account here and deploy your sites." />
          )
        ) : list.data.length === 0 ? (
          <EmptyState icon={docker ? <Container size={20} /> : <Rocket size={20} />} title={docker ? "No apps from GitHub yet" : "No sites yet"} desc={docker ? "Pick a repository with a Dockerfile or docker-compose.yml and a server: Fleetwatch builds it there, runs it, and puts your domain in front." : serverId ? "Deploy a GitHub repository to this server: it is online in a minute." : "Pick a repository and a server: the site is online in a minute, at an address of its own."} action={canAdd ? <Button onClick={() => setAdding(true)}><Plus size={15} /> {docker ? "Deploy from GitHub" : "New site"}</Button> : g?.canDeploy && !linked ? <Button onClick={() => goGitHub("connect", toast)}><Github size={15} /> Connect your GitHub</Button> : undefined} />
        ) : (
          <div className="space-y-4">
            {g && g.connected && g.canDeploy && !linked && (
              <div className="flex flex-wrap items-center gap-3 rounded-2xl border border-line bg-surface px-4 py-3 text-sm">
                <span className="grid h-9 w-9 place-items-center rounded-xl bg-surface2"><Github size={16} /></span>
                <span className="min-w-0 flex-1">Link your own GitHub account to deploy your repositories here. You manage the sites you add; the owner can manage all of them.</span>
                <Button size="sm" onClick={() => goGitHub("connect", toast)}>Connect your GitHub</Button>
              </div>
            )}
            <div className="flex flex-wrap gap-2 text-xs">
              {([["live", "live", CheckCircle2], ["building", "building", Workflow], ["failed", "failed", AlertTriangle]] as const).map(([k, word, Icon]) => {
                const n = list.data!.filter((x) => (k === "building" ? x.status === "building" || x.status === "deploying" : x.status === k)).length;
                return n > 0 && <span key={k} className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-muted"><Icon size={13} /> {n} {word}</span>;
              })}
              <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-muted"><Rocket size={13} /> {list.data.length} {list.data.length === 1 ? "site" : "sites"}</span>
            </div>
            <div className="grid gap-4 xl:grid-cols-2">{list.data.map((s) => <SiteCard key={s.id} site={s} onOpen={() => nav(`/sites/${s.id}`)} />)}</div>
          </div>
        )}
      </Gate>
      {adding && <NewSite docker={docker} serverId={serverId} owner={owner} onMoreRepos={() => goGitHub("install", toast)} onClose={() => setAdding(false)} onCreated={(s) => { setAdding(false); all.setData((l) => [...(l ?? []).filter((x) => x.id !== s.id), s]); }} />}
    </>
  );
}

/** Where a site answers: its automatic address first, then its own domains. */
function addresses(site: Site) {
  const out: { label: string; href: string; auto?: boolean }[] = [];
  if (site.atPath) out.push({ label: `${location.host}/sites/${site.name}/`, href: `/sites/${site.name}/`, auto: true });
  else if (site.preview) out.push({ label: site.preview, href: `https://${site.preview}`, auto: true });
  for (const d of site.domains) if (d !== site.preview) out.push({ label: d, href: `https://${d}` });
  return out;
}

const upper = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

async function redeploySite(site: Site, toast: (msg: string, kind?: "ok" | "err") => void) {
  try {
    await api("POST", `/api/sites/${site.id}/redeploy`);
    toast(site.plan.kind === "build" ? "Building again on GitHub" : "Deploying");
  } catch (e) {
    toast((e as ApiError).message, "err");
  }
}

/** The site's tile: what kind of site it is, with its status as a dot. */
function SiteIcon({ site, size = 44 }: { site: Site; size?: number }) {
  const [tone] = STATUS[site.status];
  return (
    <span style={{ width: size, height: size }} className="relative grid shrink-0 place-items-center rounded-2xl border border-line bg-surface2 text-fg">
      {site.plan.kind === "docker" ? <Container size={size * 0.4} /> : site.plan.kind === "build" ? <Workflow size={size * 0.4} /> : <FileCode2 size={size * 0.4} />}
      <span className={cn("absolute -right-0.5 -top-0.5 h-3 w-3 rounded-full border-2 border-surface", toneDot[tone],
        (site.status === "building" || site.status === "deploying") && "pulse-dot")} />
    </span>
  );
}

function SiteError({ site, onRetry }: { site: Site; onRetry: () => void }) {
  const { me } = useStore();
  if (!site.error) return null;
  return (
    <div className="flex gap-3 rounded-2xl bg-crit-soft px-4 py-3 text-sm text-crit">
      <AlertTriangle size={16} className="mt-0.5 shrink-0" />
      <div className="min-w-0 space-y-1.5">
        <p className="break-words">{upper(site.error)}</p>
        {me?.role !== "viewer" && (
          <div className="flex flex-wrap gap-3 text-xs font-medium">
            {site.error.includes("permissions") && <Link to="/integrations?tab=github" className="inline-flex items-center gap-1 underline"><KeyRound size={12} /> How to grant them</Link>}
            <button className="inline-flex items-center gap-1 underline" onClick={onRetry}><RefreshCw size={12} /> Retry</button>
          </div>
        )}
      </div>
    </div>
  );
}

function SiteCard({ site, onOpen }: { site: Site; onOpen: () => void }) {
  const { getServer, me, toast } = useStore();
  const [tone, label] = STATUS[site.status];
  const urls = addresses(site);
  const redeploy = () => redeploySite(site, toast);
  return (
    <Card className="flex flex-col gap-4 transition hover:border-fg/25">
      <div className="flex items-start gap-3">
        <button onClick={onOpen} className="shrink-0" aria-label={`Open ${site.name}`}><SiteIcon site={site} /></button>
        <button onClick={onOpen} className="min-w-0 flex-1 text-left">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate text-[15px] font-semibold tracking-tight hover:underline">{site.name}</span>
            <Badge tone={tone} dot pulse={site.status === "building" || site.status === "deploying"}>{label}</Badge>
          </div>
          <div className="mt-1 flex min-w-0 items-center gap-1.5 text-xs text-muted">
            <Github size={12} /><span className="truncate">{site.repo}</span>
            <GitBranch size={12} className="ml-1 shrink-0" /><span className="font-mono">{site.branch}</span>
          </div>
        </button>
        <div className="flex shrink-0 items-center gap-1">
          {me?.role !== "viewer" && (
            <button onClick={redeploy} disabled={site.status === "deploying"} title={site.plan.kind === "build" ? "Build and deploy again" : "Deploy again"} aria-label="Deploy again"
              className="grid h-9 w-9 place-items-center rounded-full text-muted transition hover:bg-surface2 hover:text-fg disabled:opacity-40">
              {site.status === "deploying" ? <Spinner size={15} /> : <RefreshCw size={15} />}
            </button>
          )}
          <button onClick={onOpen} title="Settings and history" aria-label="Settings and history" className="grid h-9 w-9 place-items-center rounded-full text-muted transition hover:bg-surface2 hover:text-fg">
            <Settings2 size={15} />
          </button>
        </div>
      </div>

      <div className="flex flex-wrap gap-2">
        {urls.map((u) => (
          <a key={u.label} href={u.href} target="_blank" rel="noreferrer"
            className="group/url inline-flex min-w-0 max-w-full items-center gap-2 rounded-full border border-line bg-surface2/50 px-3 py-1.5 font-mono text-[11px] transition hover:border-fg/40">
            {u.auto ? <Link2 size={12} className="shrink-0 text-muted" /> : <Globe size={12} className="shrink-0 text-muted" />}
            <span className="truncate">{u.label}</span>
            <ExternalLink size={11} className="shrink-0 text-muted group-hover/url:text-fg" />
          </a>
        ))}
        {urls.length === 0 && <span className="inline-flex items-center gap-1.5 text-xs text-muted"><Info size={13} /> No address yet: add your domain in its settings.</span>}
      </div>

      <SiteError site={site} onRetry={redeploy} />

      <div className="mt-auto flex flex-wrap items-center gap-x-4 gap-y-1.5 border-t border-line pt-3 text-[11px] text-muted">
        <span className="inline-flex min-w-0 items-center gap-1.5"><ServerIcon size={12} /><span className="truncate font-mono">{getServer(site.serverId)?.name ?? "removed server"}:/var/www/{site.name}</span></span>
        {site.commit ? (
          <span className="inline-flex min-w-0 items-center gap-1.5" title={site.message}>
            <GitCommitHorizontal size={12} /><span className="font-mono">{site.commit.slice(0, 7)}</span>
            <Clock size={12} className="ml-1" />{relTime(site.deployedAt)}
          </span>
        ) : <span className="inline-flex items-center gap-1.5"><Clock size={12} /> Not deployed yet</span>}
        <span className="inline-flex items-center gap-1.5">{site.plan.kind === "build" ? <Workflow size={12} /> : <FileCode2 size={12} />}{site.plan.framework}</span>
        {site.buildUrl && site.plan.kind === "build" && <a className="inline-flex items-center gap-1.5 hover:text-fg" href={site.buildUrl} target="_blank" rel="noreferrer"><Terminal size={12} /> Last build</a>}
        {!site.autoDeploy && <span className="inline-flex items-center gap-1.5"><PauseCircle size={12} /> Auto deploy off</span>}
      </div>
    </Card>
  );
}

function PlanFields({ plan, onChange }: { plan: Plan; onChange: (p: Plan) => void }) {
  if (plan.kind === "docker") {
    return (
      <Field label={plan.compose ? "Host port the web traffic goes to" : "The app's port in the container"}
        hint={plan.compose ? `${plan.compose} publishes it (like "8080:80" → 8080). Empty: no web address, it only runs.` : `Its EXPOSE port. Published on this server's 127.0.0.1${plan.hostPort ? `:${plan.hostPort}` : ""} only; Caddy sends your domains to it. Empty: no web address.`}>
        <Input type="number" min={0} max={65535} value={plan.port || ""} placeholder="3000" onChange={(e) => onChange({ ...plan, port: Number(e.target.value) || 0 })} />
      </Field>
    );
  }
  if (plan.kind === "static") {
    return (
      <Field label="Folder to publish" hint="Empty: the whole repository.">
        <Input value={plan.dir} onChange={(e) => onChange({ ...plan, dir: e.target.value })} placeholder="public" />
      </Field>
    );
  }
  return (
    <div className="grid gap-3 sm:grid-cols-3">
      <Field label="Install"><Input value={plan.install} onChange={(e) => onChange({ ...plan, install: e.target.value })} /></Field>
      <Field label="Build"><Input value={plan.build} onChange={(e) => onChange({ ...plan, build: e.target.value })} /></Field>
      <Field label="Output folder"><Input value={plan.output} onChange={(e) => onChange({ ...plan, output: e.target.value })} /></Field>
    </div>
  );
}

/** One part of the new-site form: an icon on a guide line, a title, then the fields. */
function Step({ icon, title, sub, last, children }: { icon: ReactNode; title: string; sub?: string; last?: boolean; children: ReactNode }) {
  return (
    <section className="grid gap-3 sm:grid-cols-[2.25rem_1fr] sm:gap-4">
      <div className="hidden flex-col items-center sm:flex">
        <span className="grid h-9 w-9 place-items-center rounded-xl border border-line bg-surface2 text-fg">{icon}</span>
        {!last && <span className="mt-2 w-px flex-1 bg-line" />}
      </div>
      <div className={cn("min-w-0 space-y-3", !last && "pb-4")}>
        <div className="sm:pt-1">
          <h4 className="text-sm font-semibold">{title}</h4>
          {sub && <p className="text-xs text-muted">{sub}</p>}
        </div>
        {children}
      </div>
    </section>
  );
}

/** An input with an icon (or fixed text) at its start. */
function IconInput({ icon, prefix, className, ...p }: InputHTMLAttributes<HTMLInputElement> & { icon?: ReactNode; prefix?: string }) {
  return (
    <div className="relative">
      <span className="pointer-events-none absolute left-4 top-1/2 flex -translate-y-1/2 items-center gap-2 text-muted">
        {icon}
        {prefix && <span className="font-mono text-[13px]">{prefix}</span>}
      </span>
      <Input {...p} className={cn(prefix ? "pl-[5.6rem] font-mono text-[13px]" : "pl-10", className)} />
    </div>
  );
}

function NewSite({ onClose, onCreated, serverId, owner, onMoreRepos, docker }: { onClose: () => void; onCreated: (s: Site) => void; serverId?: string; owner?: boolean; onMoreRepos?: () => void; docker?: boolean }) {
  const { servers, toast } = useStore();
  const repos = useApi<{ repo: { fullName: string; defaultBranch: string; private: boolean } }[]>("/api/me/github/repos");
  const [f, setF] = useState({ repo: "", serverId: serverId ?? "", branch: "", name: "", domains: "", env: "", dnsManual: false });
  const cloudflare = useApi<{ accounts: unknown[] }>("/api/me/dns");
  const [plan, setPlan] = useState<Plan | null>(null);
  const [exampleFile, setExampleFile] = useState("");
  const [busy, setBusy] = useState("");
  const [q, setQ] = useState("");
  const enrolled = servers.filter((s) => s.enrolled);
  useEffect(() => {
    if (!f.serverId && enrolled[0]) setF((x) => ({ ...x, serverId: enrolled[0].id }));
  }, [enrolled, f.serverId]);
  const detect = async (repo: string, branch: string) => {
    setPlan(null);
    if (!repo) return;
    setBusy("detect");
    try {
      const r = await api<{ plan: Plan; branch: string; exampleFile: string; example: string }>("POST", "/api/sites/detect", { repo, branch, docker });
      setPlan(r.plan);
      setExampleFile(r.exampleFile);
      // The repository's .env template, to fill in.
      setF((x) => ({ ...x, branch: r.branch, env: x.env || r.example }));
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const create = async () => {
    setBusy("create");
    try {
      const s = await api<Site>("POST", "/api/sites", { repo: f.repo, serverId: f.serverId, branch: f.branch, name: f.name, plan,
        domains: f.domains.split(/[\s,]+/).filter(Boolean), env: plan?.kind !== "static" ? f.env : "", dnsManual: f.dnsManual });
      toast(s.plan.kind === "build" ? "Site added: GitHub Actions is building it" : s.plan.kind === "docker" ? "App added: building it on the server" : "Site added: deploying");
      onCreated(s);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const pick = (repo: string) => {
    setF({ ...f, repo, branch: "", env: "" });
    detect(repo, "");
  };
  const list = (repos.data ?? []).filter(({ repo }) => repo.fullName.toLowerCase().includes(q.trim().toLowerCase()));
  // The folder the server picks when the name is left empty (the repository's name).
  const name = f.name || (f.repo.split("/")[1] ?? "").toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");
  const server = enrolled.find((s) => s.id === f.serverId);
  return (
    <Modal open wide onClose={onClose}
      title={
        <span className="flex items-center gap-3">
          <span className="grid h-9 w-9 place-items-center rounded-xl bg-fg text-bg">{docker ? <Container size={16} /> : <Rocket size={16} />}</span>
          <span>
            <span className="block">{docker ? "Deploy a Docker app" : "New site"}</span>
            <span className="block text-xs font-normal text-muted">{docker ? "A GitHub repository with a Dockerfile or compose file, run on one of your servers" : "Deploy a GitHub repository to one of your servers"}</span>
          </span>
        </span>
      }
      footer={
        <div className="flex w-full flex-wrap items-center justify-between gap-3">
          <span className="flex min-w-0 items-center gap-1.5 truncate font-mono text-[11px] text-muted">
            {f.repo ? <><FolderGit2 size={13} className="shrink-0" />{server?.name ?? "…"}:/var/www/{name || "…"}</> : "Pick a repository to start"}
          </span>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={!plan || !f.serverId || !!busy} onClick={create}>
              {busy === "create" ? <Spinner size={14} /> : <Rocket size={14} />} Deploy site
            </Button>
          </div>
        </div>
      }>
      <div>
        <Step icon={<Github size={16} />} title="Repository" sub="From the repositories the GitHub app can see.">
          {repos.error ? (
            <p className="text-sm text-crit">{repos.error}</p>
          ) : (
            <div className="overflow-hidden rounded-2xl border border-line">
              <div className="border-b border-line p-2">
                <SearchInput value={q} onChange={setQ} placeholder="Search repositories…" />
              </div>
              <div className="max-h-52 overflow-y-auto p-1.5">
                {!repos.data && <div className="flex items-center gap-2 px-3 py-3 text-sm text-muted"><Spinner size={14} /> Loading repositories…</div>}
                {repos.data && list.length === 0 && (
                  <p className="px-3 py-3 text-sm text-muted">No repository matches. {owner ? <Link to="/integrations?tab=github" className="underline">Give the app access to more</Link> : <button type="button" className="underline" onClick={onMoreRepos}>Give the app access to more of your repositories</button>}</p>
                )}
                {list.map(({ repo }) => {
                  const [owner, rname] = repo.fullName.split("/");
                  const on = f.repo === repo.fullName;
                  return (
                    <button key={repo.fullName} type="button" onClick={() => pick(repo.fullName)}
                      className={cn("flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left transition", on ? "bg-surface2 ring-1 ring-fg/25" : "hover:bg-surface2")}>
                      <span className="grid h-8 w-8 shrink-0 place-items-center rounded-lg border border-line bg-surface text-muted"><Github size={15} /></span>
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium">{rname}</span>
                        <span className="block truncate text-[11px] text-muted">{owner}</span>
                      </span>
                      {repo.private && <Lock size={13} className="shrink-0 text-muted" aria-label="Private" />}
                      <span className="hidden shrink-0 items-center gap-1 rounded-full border border-line px-2 py-0.5 font-mono text-[11px] text-muted sm:flex"><GitBranch size={11} />{repo.defaultBranch}</span>
                      <span className={cn("grid h-5 w-5 shrink-0 place-items-center rounded-full", on ? "bg-fg text-bg" : "border border-line")}>{on && <Check size={12} />}</span>
                    </button>
                  );
                })}
              </div>
            </div>
          )}
        </Step>

        <Step icon={<ServerIcon size={16} />} title="Destination" sub="Caddy serves the site from this folder on the server.">
          {enrolled.length === 0 && (
            <p className="flex items-center gap-2 rounded-2xl border border-dashed border-line px-4 py-3 text-sm text-muted">
              <ServerIcon size={14} /> No server yet. <Link to="/servers/new" className="underline">Add one</Link> first.
            </p>
          )}
          <div className="grid gap-2 sm:grid-cols-3">
            {enrolled.map((s) => {
              const on = f.serverId === s.id;
              return (
                <button key={s.id} type="button" onClick={() => setF({ ...f, serverId: s.id })}
                  className={cn("flex items-center gap-3 rounded-2xl border px-3 py-2.5 text-left transition", on ? "border-fg/40 bg-surface2" : "border-line hover:bg-surface2")}>
                  <span className="relative grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-surface2 text-muted">
                    <ServerIcon size={15} />
                    <span className={cn("absolute -right-0.5 -top-0.5 h-2.5 w-2.5 rounded-full border-2 border-surface", s.status === "disconnected" ? "bg-crit" : s.status === "pending" ? "bg-muted" : "bg-ok")} />
                  </span>
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium">{s.name}</span>
                    <span className="block truncate font-mono text-[11px] text-muted">{s.ip || s.hostname}</span>
                  </span>
                </button>
              );
            })}
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Branch" hint="Every push to it deploys again.">
              <IconInput icon={<GitBranch size={14} />} value={f.branch} onChange={(e) => setF({ ...f, branch: e.target.value })} onBlur={() => f.repo && detect(f.repo, f.branch)} placeholder="main" />
            </Field>
            <Field label="Folder" hint="Empty: the repository's name.">
              <IconInput prefix="/var/www/" value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder={name || "my-site"} />
            </Field>
          </div>
        </Step>

        {f.repo && (
          <Step icon={<Hammer size={16} />} title="Build" sub="Found by looking at the repository.">
            {busy === "detect" && <div className="flex items-center gap-2 rounded-2xl border border-line px-4 py-3 text-sm text-muted"><Spinner size={14} /> Looking at the repository…</div>}
            {plan && (
              <div className="space-y-3 rounded-2xl border border-line bg-surface2/60 p-4">
                <div className="flex flex-wrap items-center gap-3">
                  <span className="grid h-9 w-9 place-items-center rounded-xl border border-line bg-surface text-fg">{plan.kind === "docker" ? <Container size={16} /> : plan.kind === "build" ? <Workflow size={16} /> : <FileCode2 size={16} />}</span>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 text-sm font-medium">{plan.framework} <Badge tone={plan.kind === "static" ? "ok" : "info"}>{plan.kind === "docker" ? "Docker" : plan.kind === "build" ? "Build" : "Static"}</Badge></div>
                    <p className="text-xs text-muted">{plan.kind === "docker" ? (plan.compose ? `docker compose up --build on the server, every push (${plan.compose}).` : "docker build and docker run on the server, every push.") : plan.kind === "build" ? "GitHub Actions builds every push, then the output goes live." : "The files are published as they are on every push."}</p>
                  </div>
                </div>
                {plan.note && <p className="flex gap-2 rounded-xl bg-warn-soft px-3 py-2 text-xs text-warn"><Info size={14} className="mt-px shrink-0" />{plan.note}</p>}
                <PlanFields plan={plan} onChange={setPlan} />
                {plan.kind !== "static" && (
                  <Field label={`${plan.kind === "docker" ? ".env for the app" : ".env for the build"}${exampleFile ? ` (from ${exampleFile})` : ""}`}
                    hint={plan.kind === "docker" ? "Encrypted here; written next to the app on the server (readable by root only) and given to the container." : "Encrypted here and stored as a GitHub Actions secret. It ends up in the public JavaScript, so no server secrets."}>
                    <div className="relative">
                      <KeyRound size={14} className="pointer-events-none absolute left-4 top-3.5 text-muted" />
                      <Textarea rows={4} value={f.env} onChange={(e) => setF({ ...f, env: e.target.value })} placeholder="VITE_API_URL=https://api.example.com" className="pl-10 font-mono text-[12px]" />
                    </div>
                  </Field>
                )}
              </div>
            )}
          </Step>
        )}

        <Step icon={<Globe size={16} />} title="Addresses" last>
          {f.repo && name && (
            <div className="flex items-center gap-3 rounded-2xl border border-line px-4 py-2.5">
              <Link2 size={14} className="shrink-0 text-muted" />
              <span className="min-w-0 flex-1 truncate font-mono text-[12px]">{location.host}/sites/{name}/</span>
              <Badge tone="ok">Automatic</Badge>
            </div>
          )}
          <Field label="Your own domains (optional)" hint="Caddy gets the HTTPS certificate for each.">
            <IconInput icon={<Globe size={14} />} value={f.domains} onChange={(e) => setF({ ...f, domains: e.target.value })} placeholder="example.com, www.example.com" />
          </Field>
          {f.domains.trim() && <DnsMode manual={f.dnsManual} onChange={(m) => setF({ ...f, dnsManual: m })} connected={(cloudflare.data?.accounts?.length ?? 0) > 0} />}
          {plan?.kind !== "docker" && <p className="flex items-center gap-2 text-[11px] text-muted"><FileCode2 size={12} className="shrink-0" />A Caddyfile at the top of the repository is used for the site instead of the generated one.</p>}
        </Step>
      </div>
    </Modal>
  );
}

/** A block of the site drawer: icon, title, then its content. */
function Panel({ icon, title, sub, danger, children }: { icon: ReactNode; title: string; sub?: ReactNode; danger?: boolean; children: ReactNode }) {
  return (
    <section className={cn("rounded-3xl border bg-surface p-5", danger ? "border-crit/30" : "border-line")}>
      <div className="mb-3 flex items-start gap-3">
        <span className={cn("grid h-8 w-8 shrink-0 place-items-center rounded-lg", danger ? "bg-crit-soft text-crit" : "bg-surface2 text-fg")}>{icon}</span>
        <div className="min-w-0 pt-0.5">
          <h4 className="text-sm font-semibold">{title}</h4>
          {sub && <p className="text-xs text-muted">{sub}</p>}
        </div>
      </div>
      <div className="space-y-3">{children}</div>
    </section>
  );
}

const CADDY_SOURCE: Record<string, string> = {
  generated: "Generated by Fleetwatch from the site's addresses",
  repository: "From the Caddyfile in the repository",
  custom: "Your own config, used on every deploy",
};

/** The Caddy site file of a site: what is live, and the owner's own version of it. */
function SiteCaddy({ site, editing, setEditing, onSaved }: { site: Site; editing: string | null; setEditing: (v: string | null) => void; onSaved: (s: Site) => void }) {
  const { me, toast } = useStore();
  const owner = !me || me.role === "owner";
  const [busy, setBusy] = useState(false);
  // What to edit: the owner's config, else the live one without Fleetwatch's comment line.
  const start = site.caddyCustom || (site.caddyConfig ?? "").replace(/^# Site [^\n]*\n/, "").trim();
  const save = async (config: string) => {
    setBusy(true);
    try {
      onSaved(await api<Site>("PUT", `/api/sites/${site.id}/caddy`, { config }));
      toast(config ? "Saved: deploying again, Caddy checks it before it goes live" : "Back to the generated config: deploying again");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const live = site.caddyConfig;
  return (
    <Panel icon={<FileCode2 size={15} />} title="Caddy config" sub={site.caddySource ? CADDY_SOURCE[site.caddySource] : "How Caddy serves the site's own addresses."}>
      {editing === null ? (
        <>
          {live ? (
            <pre className="max-h-72 overflow-auto rounded-xl bg-surface2 p-3 font-mono text-[11px] leading-relaxed">{live}</pre>
          ) : (
            <p className="flex gap-2 text-xs text-muted"><Info size={13} className="shrink-0" />
              {site.deployedAt ? "No Caddy site: it has no address of its own (it is served by the dashboard), or its server's agent is older than 0.9.72. Add a domain, or deploy again after the agent updates." : "Shown after the first deploy."}
            </p>
          )}
          {owner && (
            <div className="flex flex-wrap justify-end gap-2">
              {site.caddyCustom && <Button size="sm" variant="ghost" disabled={busy} onClick={() => save("")}><Undo2 size={13} /> Use generated</Button>}
              <Button size="sm" variant="secondary" onClick={() => setEditing(start || `${site.domains[0] || site.preview || "example.com"} {\n\troot * /var/www/${site.name}\n\tfile_server\n}`)}><Pencil size={13} /> Edit</Button>
            </div>
          )}
        </>
      ) : (
        <>
          <Textarea rows={12} value={editing} onChange={(e) => setEditing(e.target.value)} spellCheck={false} className="font-mono text-[12px] leading-relaxed" />
          <p className="flex gap-2 text-[11px] text-muted"><Info size={13} className="shrink-0" />Replaces the generated config (and a Caddyfile in the repository) on every deploy. Caddy checks it first: if it is rejected, the old one stays and the deploy shows why.</p>
          <div className="flex flex-wrap justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
            <Button size="sm" disabled={busy || !editing.trim()} onClick={() => save(editing)}>{busy ? <Spinner size={13} /> : <Save size={13} />} Save and apply</Button>
          </div>
        </>
      )}
    </Panel>
  );
}

/** Automatic DNS (records made through the person's Cloudflare) or manual (they add them). */
function DnsMode({ manual, onChange, connected }: { manual: boolean; onChange: (manual: boolean) => void; connected: boolean }) {
  return (
    <div className="space-y-2">
      <Segmented value={manual ? "manual" : "auto"} onChange={(v) => onChange(v === "manual")} options={[
        { value: "auto", label: <span className="inline-flex items-center gap-1.5"><Wand2 size={13} />Automatic DNS</span> },
        { value: "manual", label: <span className="inline-flex items-center gap-1.5"><Hand size={13} />Manual DNS</span> },
      ]} />
      <p className="flex gap-2 text-[11px] text-muted">
        <Info size={13} className="shrink-0" />
        {manual
          ? "You add the DNS records yourself; the site shows exactly which, and checks them."
          : connected
            ? "Fleetwatch creates the A records in your Cloudflare for the domains it holds, and removes them with the site."
            : <span>Connect Cloudflare first (<Link to="/integrations?tab=cloudflare" className="underline">Integrations</Link>, or Settings → Account for team members) so Fleetwatch can create the records. Until then, add them yourself.</span>}
      </p>
    </div>
  );
}

/** The records to add by hand, and where each domain points right now. */
function ManualDNS({ site, ip }: { site: Site; ip?: string }) {
  const [check, setCheck] = useState<Record<string, string[]> | null>(null);
  const [busy, setBusy] = useState(false);
  const { toast } = useStore();
  if (site.domains.length === 0) return null;
  const run = async () => {
    setBusy(true);
    try {
      setCheck(await api<Record<string, string[]>>("GET", `/api/sites/${site.id}/dns/check`));
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2 text-xs font-medium text-muted">
        <span>Add these records at your DNS provider</span>
        <button className="inline-flex items-center gap-1 hover:text-fg" disabled={busy} onClick={run}>{busy ? <Spinner size={11} /> : <SearchCheck size={12} />} Check DNS</button>
      </div>
      <div className="overflow-hidden rounded-xl border border-line text-xs">
        <div className="grid grid-cols-[3rem_1fr_1fr] gap-2 bg-surface2/60 px-3 py-1.5 font-medium text-muted"><span>Type</span><span>Name</span><span>Value</span></div>
        {site.domains.map((d) => {
          const got = check?.[d];
          return (
            <div key={d} className="border-t border-line px-3 py-2">
              <div className="grid grid-cols-[3rem_1fr_1fr] items-center gap-2">
                <span className="font-mono font-medium">A</span>
                <span className="flex min-w-0 items-center gap-1"><span className="truncate font-mono">{d}</span><CopyButton text={d} /></span>
                <span className="flex min-w-0 items-center gap-1"><span className="truncate font-mono">{ip || "the server's IP"}</span>{ip && <CopyButton text={ip} />}</span>
              </div>
              {got && (
                <div className="mt-1.5 text-[11px]">
                  {got.length === 0 ? <span className="flex items-center gap-1 text-warn"><AlertTriangle size={11} />No record found yet (new records can take a few minutes)</span>
                    : ip && got.includes(ip) ? <span className="flex items-center gap-1 text-ok"><Check size={11} />Points at this server</span>
                    : <span className="flex items-center gap-1 text-warn" title="Behind Cloudflare's proxy, a domain shows Cloudflare's addresses instead"><AlertTriangle size={11} />Points at {got.join(", ")}{" "}(fine if it goes through Cloudflare's proxy)</span>}
                </div>
              )}
            </div>
          );
        })}
      </div>
      <p className="text-[11px] text-muted">Using Cloudflare? Set the record to <b>DNS only</b> (grey cloud) until the site loads over HTTPS, then you can turn the proxy on.</p>
    </div>
  );
}

/** How each of a site's domains stands with its owner's DNS accounts. */
function SiteDNS({ site, onChange }: { site: Site; onChange: (s: Site) => void }) {
  const { toast } = useStore();
  const [busy, setBusy] = useState("");
  if (site.domains.length === 0) return null;
  const proxy = async (domain: string, on: boolean) => {
    setBusy(domain);
    try {
      onChange(await api<Site>("PUT", `/api/sites/${site.id}/dns/proxy`, { domain, on }));
      toast(on ? `${domain} goes through Cloudflare now: your server's IP is hidden` : `${domain} points straight at the server now`);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const again = async () => {
    setBusy("again");
    try {
      await api("POST", `/api/sites/${site.id}/dns`);
      toast("Pointing the domains again: this takes a few seconds");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2 text-xs font-medium text-muted">
        <span>DNS of your domains</span>
        {site.mine && <button className="inline-flex items-center gap-1 hover:text-fg" disabled={busy === "again"} onClick={again}><RefreshCw size={11} /> Point again</button>}
      </div>
      <ul className="space-y-1.5">
        {site.domains.map((d) => {
          const st = site.dns?.[d];
          return (
            <li key={d} className="flex flex-wrap items-center gap-2 rounded-xl border border-line px-3 py-2 text-xs">
              <Globe size={12} className="shrink-0 text-muted" />
              <span className="min-w-0 flex-1 truncate font-mono">{d}</span>
              {!st ? (
                <span className="text-muted">No Cloudflare account of yours holds it: connect Cloudflare, or switch to Manual DNS</span>
              ) : st.status === "error" ? (
                <span className="flex items-center gap-1 text-crit" title={st.error}><AlertTriangle size={12} />{providerName(st.provider)}: {st.error}</span>
              ) : (
                <>
                  <Badge tone="ok"><Check size={11} />{providerName(st.provider)}{st.status === "updated" && st.previous ? ` · moved from ${st.previous}` : ""}</Badge>
                  {st.provider === "cloudflare" && (
                    <span className="flex items-center gap-1.5">
                      {!st.proxied && <span className="text-warn" title="Anyone can see which server it runs on">IP visible</span>}
                      <span className="text-muted">Proxy</span>
                      {site.mine ? (busy === d ? <Spinner size={12} /> : <Toggle checked={!!st.proxied} onChange={(on) => proxy(d, on)} />) : <span>{st.proxied ? "on" : "off"}</span>}
                    </span>
                  )}
                </>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

interface Traffic {
  requests: number;
  visits: number;
  bytes: number;
  threats?: number;
  hours: { t: string; requests: number; bytes: number }[];
  countries: { code: string; requests: number }[];
}

const regionName = (code: string) => {
  try {
    return new Intl.DisplayNames(undefined, { type: "region" }).of(code) ?? code;
  } catch {
    return code || "Unknown";
  }
};

/** The site's last 24 hours on Cloudflare, through the account of whoever added it. */
function SiteTraffic({ site }: { site: Site }) {
  const data = useApi<Traffic>(`/api/sites/${site.id}/traffic`);
  if (site.domains.length === 0) return null;
  const t = data.data;
  const top = t?.countries[0]?.requests || 1;
  return (
    <Panel icon={<BarChart3 size={15} />} title="Traffic" sub="The last 24 hours of its domains, from Cloudflare.">
      {data.error ? (
        <p className="flex items-start gap-2 text-xs text-muted">
          <Info size={13} className="mt-px shrink-0" />
          <span>{upper(data.error.replace(/.$/, ""))}. {data.error.includes("sign in") || data.error.includes("account") ? <Link to={site.mine ? "/integrations?tab=cloudflare" : "/settings/account"} className="underline">Cloudflare sign-in</Link> : null}</span>
        </p>
      ) : !t ? (
        <LoadingState label="Asking Cloudflare…" />
      ) : (
        <>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            {[
              ["Requests", t.requests.toLocaleString()],
              ["Visits", t.visits.toLocaleString()],
              ["Bandwidth", fmtBytes(t.bytes)],
              ["Threats stopped", t.threats === undefined ? "–" : t.threats.toLocaleString()],
            ].map(([k, v]) => (
              <div key={k} className="rounded-2xl bg-surface2/60 px-3 py-2.5">
                <div className="text-[11px] text-muted">{k}</div>
                <div className="text-lg font-semibold tabular-nums">{v}</div>
              </div>
            ))}
          </div>
          {t.requests > 0 ? (
            <>
              <div>
                <div className="mb-1 text-xs font-medium text-muted">Requests per hour</div>
                <MetricChart kind="bar" height={120} unit=" requests" decimals={0} data={t.hours.map((h) => ({ t: Date.parse(h.t), v: h.requests }))} />
              </div>
              {t.countries.length > 0 && (
                <div>
                  <div className="mb-1.5 text-xs font-medium text-muted">Top countries</div>
                  <ul className="space-y-1.5">
                    {t.countries.map((c) => (
                      <li key={c.code} className="grid grid-cols-[8rem_1fr_auto] items-center gap-3 text-xs">
                        <span className="truncate">{regionName(c.code)}</span>
                        <span className="h-1.5 overflow-hidden rounded-full bg-surface2"><span className="block h-full rounded-full bg-[var(--chart1)]" style={{ width: `${(c.requests / top) * 100}%` }} /></span>
                        <span className="tabular-nums text-muted">{c.requests.toLocaleString()}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          ) : (
            <p className="text-xs text-muted">No visits in the last 24 hours.</p>
          )}
          <CloudflareControls site={site} traffic={t} />
        </>
      )}
    </Panel>
  );
}

/** How many times its usual level the last hour's traffic is (0 when too little to tell). */
function spike(t: Traffic) {
  const hours = t.hours.map((h) => h.requests);
  const last = hours[hours.length - 1] ?? 0;
  const before = hours.slice(0, -1).filter((v) => v > 0);
  if (last < 500 || before.length < 3) return 0;
  const usual = before.reduce((a, b) => a + b, 0) / before.length;
  return usual > 0 && last / usual >= 5 ? Math.round(last / usual) : 0;
}

/** Cloudflare's cache and "Under attack" for the site's domains. */
function CloudflareControls({ site, traffic }: { site: Site; traffic: Traffic }) {
  const { toast } = useStore();
  const attack = useApi<{ on: boolean }>(`/api/sites/${site.id}/attack`);
  const [busy, setBusy] = useState("");
  const jump = spike(traffic);
  const run = async (what: string, fn: () => Promise<unknown>, done: string) => {
    setBusy(what);
    try {
      await fn();
      toast(done);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const setAttack = (on: boolean) => run("attack", async () => attack.setData(await api<{ on: boolean }>("PUT", `/api/sites/${site.id}/attack`, { on })), on ? "Under attack mode on" : "Under attack mode off");
  return (
    <div className="space-y-3 border-t border-line pt-3">
      {jump > 0 && !attack.data?.on && (
        <div className="flex flex-col gap-2 rounded-2xl bg-warn-soft p-3 text-xs text-warn sm:flex-row sm:items-center">
          <AlertTriangle size={14} className="shrink-0" />
          <span className="flex-1">Traffic in the last hour is about <b>{jump}×</b> its usual level{traffic.threats ? `, with ${traffic.threats.toLocaleString()} threats stopped` : ""}. If you don't expect it (a launch, a campaign), it may be an attack: <b>Under attack</b> mode makes visitors pass a quick browser check first.</span>
          <Button size="sm" variant="secondary" disabled={!!busy} onClick={() => setAttack(true)}>Turn it on</Button>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" variant="secondary" disabled={!!busy} onClick={() => run("purge", () => api("POST", `/api/sites/${site.id}/purge`), "Cloudflare's cache emptied for this site")}>
          {busy === "purge" ? <Spinner size={13} /> : <RefreshCw size={13} />} Purge cache
        </Button>
        <label className="flex items-center gap-2 text-sm" title="Visitors pass a quick browser check before reaching the site.">
          <Toggle checked={!!attack.data?.on} disabled={!attack.data || !!busy} onChange={setAttack} /> Under attack mode
        </label>
        {attack.error && <span className="text-xs text-muted">{attack.error}</span>}
      </div>
    </div>
  );
}

/** One site on a page of its own: overview, domains and DNS, build and deploy, Caddy. */
export function SitePage() {
  const { id = "" } = useParams();
  const nav = useNavigate();
  const [tab, setTab] = useState("overview");
  const { me, getServer, toast } = useStore();
  const data = useApi<{ site: Site; history: Deploy[] }>(`/api/sites/${id}`);
  const rollback = async (ts: number) => {
    try {
      await api("POST", `/api/sites/${id}/rollback`, { ts });
      toast("Rolling back: the earlier version is being deployed");
      data.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const owner = !!data.data?.site.mine; // who added it, or the dashboard owner
  const env = useApi<{ env: string; exampleFile?: string; example?: string }>(owner ? `/api/sites/${id}/env` : `/api/sites/${id}`);
  const [domains, setDomains] = useState<string | null>(null);
  const [auto, setAuto] = useState(true);
  const [manual, setManual] = useState(false);
  const [plan, setPlan] = useState<Plan | null>(null);
  const cloudflare = useApi<{ accounts: unknown[] }>("/api/me/dns");
  const [envText, setEnvText] = useState<string | null>(null);
  const [del, setDel] = useState(false);
  const [log, setLog] = useState<number | null>(null);
  const [caddyText, setCaddyText] = useState<string | null>(null); // editing the Caddy config
  const site = data.data?.site;
  useEffect(() => {
    if (!site) return;
    setDomains(site.domains.join("\n"));
    setAuto(site.autoDeploy);
    setManual(!!site.dnsManual);
    setPlan(site.plan);
  }, [site]);
  useEffect(() => {
    if (env.data && "env" in env.data) setEnvText(env.data.env || env.data.example || "");
  }, [env.data]);
  if (data.error) return <ErrorState message={data.error} onRetry={data.reload} />;
  if (!site || domains === null || !plan) return <LoadingState />;
  const ip = getServer(site.serverId)?.ip;
  const [tone, label] = STATUS[site.status];
  const urls = addresses(site);
  const save = async () => {
    try {
      data.setData({ ...data.data!, site: await api<Site>("PUT", `/api/sites/${id}`, { domains: domains.split(/[\s,]+/).filter(Boolean), autoDeploy: auto, plan, dnsManual: manual }) });
      toast("Saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const saveEnv = async () => {
    try {
      await api("PUT", `/api/sites/${id}/env`, { env: envText });
      toast(data.data?.site.plan.kind === "docker" ? ".env saved: deploying again" : ".env saved: building again");
      env.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const remove = async () => {
    setDel(false);
    try {
      await api("DELETE", `/api/sites/${id}`);
      toast("Site removed");
      nav("/sites");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const dirty = domains !== site.domains.join("\n") || auto !== site.autoDeploy || manual !== !!site.dnsManual || JSON.stringify(plan) !== JSON.stringify(site.plan);
  const envFromExample = env.data && "env" in env.data && !env.data.env && env.data.exampleFile;
  const tabs = [
    { value: "overview", label: "Overview", icon: LayoutDashboard },
    ...(owner ? [{ value: "domains", label: "Domains & DNS", icon: Globe }, { value: "deploy", label: site.plan.kind === "build" ? "Build & deploy" : "Deploy", icon: Hammer }] : []),
    { value: "caddy", label: "Caddy", icon: FileCode2 },
  ];
  const current = tabs.some((t) => t.value === tab) ? tab : "overview";
  return (
    <>
      <PageHeader
        back={{ to: "/sites", label: "All sites" }}
        title={<span className="flex min-w-0 items-center gap-3"><SiteIcon site={site} size={40} /><span className="truncate">{site.name}</span></span>}
        badge={<Badge tone={tone} dot pulse={site.status === "building" || site.status === "deploying"}>{label}</Badge>}
        sub={`${site.plan.framework} · ${site.plan.kind === "build" ? "built by GitHub Actions" : "published as is"} · ${site.repo}`}
        actions={
          <>
            {site.buildUrl && site.plan.kind === "build" && <a href={site.buildUrl} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost"><Terminal size={14} /> Last build</Button></a>}
            {me?.role !== "viewer" && <Button size="sm" variant="secondary" onClick={() => redeploySite(site, toast)} disabled={site.status === "deploying"}><RefreshCw size={14} /> {site.plan.kind === "build" ? "Rebuild" : "Redeploy"}</Button>}
            {me?.role !== "viewer" && data.data!.history.filter((d) => d.ok).length > 1 && <Button size="sm" variant="secondary" onClick={() => rollback(0)} disabled={site.status === "deploying"} title="Deploy the previous working version again"><Undo2 size={14} /> Roll back</Button>}
            {urls[0] && <a href={urls[0].href} target="_blank" rel="noreferrer"><Button size="sm" variant="secondary"><ExternalLink size={14} /> Open site</Button></a>}
            {owner && <Button size="sm" disabled={!dirty} onClick={save}><Check size={14} /> Save</Button>}
          </>
        }
      />
      <div className="mb-5 empty:hidden"><SiteError site={site} onRetry={() => redeploySite(site, toast)} /></div>
      {site.pinned && site.autoDeploy && (
        <p className="mb-5 flex items-center gap-2 rounded-2xl bg-info-soft p-3 text-xs text-info"><Undo2 size={13} className="shrink-0" />Rolled back: new pushes are deployed again once there is a commit newer than the one rolled back from. Redeploy to go back to the latest now.</p>
      )}
      <div className="mb-5 overflow-x-auto">
        <Segmented value={current} onChange={setTab} options={tabs.map((t) => ({ value: t.value, label: <span className="inline-flex items-center gap-1.5"><t.icon size={13} />{t.label}</span> }))} />
      </div>
      <div className="pb-20">
        {current === "overview" && (
          <div className="grid gap-6 xl:grid-cols-3">
            <div className="space-y-6 xl:col-span-2">
              <SiteTraffic site={site} />
              <Panel icon={<History size={15} />} title="Deploys" sub="The last 20.">
          {data.data!.history.length === 0 && <p className="text-sm text-muted">None yet.</p>}
          <ol className="relative space-y-3 before:absolute before:bottom-2 before:left-[5px] before:top-2 before:w-px before:bg-line">
            {data.data!.history.map((d, i) => (
              <li key={i} className="relative pl-6">
                <span className={cn("absolute left-0 top-1.5 h-[11px] w-[11px] rounded-full border-2 border-surface", d.ok ? "bg-ok" : "bg-crit")} />
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  <span className="rounded-md bg-surface2 px-1.5 py-0.5 font-mono text-[11px]">{d.commit.slice(0, 7)}</span>
                  <span className="min-w-0 flex-1 truncate">{d.message || (d.ok ? "Deployed" : "Failed")}</span>
                </div>
                <div className="mt-0.5 flex flex-wrap items-center gap-x-3 text-[11px] text-muted">
                  <span>{d.by}</span>
                  <span>{relTime(d.ts)}</span>
                  {d.ok && i > 0 && (d.deployed || site.plan.kind !== "build") && me?.role !== "viewer" && site.status !== "deploying" && d.commit !== site.commit && (
                    <button className="inline-flex items-center gap-1 hover:text-fg" onClick={() => rollback(d.ts)}><Undo2 size={11} /> Roll back to this</button>
                  )}
                  {d.log && <button className="inline-flex items-center gap-1 hover:text-fg" onClick={() => setLog(log === i ? null : i)}><Terminal size={11} /> {log === i ? "Hide log" : "Log"}</button>}
                </div>
                {d.error && <p className="mt-1 text-xs text-crit">{upper(d.error)}</p>}
                {log === i && <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded-xl bg-surface2 p-3 font-mono text-[11px]">{d.log}</pre>}
              </li>
            ))}
          </ol>
        </Panel>
            </div>
            <div className="space-y-6">
              <Panel icon={<Info size={15} />} title="Details">
                <dl className="space-y-2.5 text-xs">
                  <div className="flex items-center justify-between gap-3"><dt className="flex items-center gap-2 text-muted"><Github size={13} />Repository</dt><dd className="min-w-0 truncate"><a href={`https://github.com/${site.repo}`} target="_blank" rel="noreferrer" className="hover:underline">{site.repo}</a></dd></div>
                  <div className="flex items-center justify-between gap-3"><dt className="flex items-center gap-2 text-muted"><GitBranch size={13} />Branch</dt><dd className="font-mono">{site.branch}</dd></div>
                  <div className="flex items-center justify-between gap-3"><dt className="flex items-center gap-2 text-muted"><ServerIcon size={13} />Server</dt><dd className="min-w-0 truncate font-mono">{getServer(site.serverId)?.name ?? "removed server"}</dd></div>
                  <div className="flex items-center justify-between gap-3"><dt className="flex items-center gap-2 text-muted"><FileCode2 size={13} />Folder</dt><dd className="min-w-0 truncate font-mono">/var/www/{site.name}</dd></div>
                  {site.commit && <div className="flex items-center justify-between gap-3"><dt className="flex items-center gap-2 text-muted"><GitCommitHorizontal size={13} />Live commit</dt><dd className="font-mono">{site.commit.slice(0, 7)} · {relTime(site.deployedAt)}</dd></div>}
                  {site.createdBy && <div className="flex items-center justify-between gap-3"><dt className="flex items-center gap-2 text-muted"><UserRound size={13} />Added by</dt><dd>{site.createdBy}</dd></div>}
                  <div className="flex items-center justify-between gap-3"><dt className="flex items-center gap-2 text-muted"><Hammer size={13} />Deploys</dt><dd>{site.autoDeploy ? "On every push" : "By hand"}</dd></div>
                </dl>
                {!site.mine && <p className="text-[11px] text-muted">Only the person who added it and the owner can change it.</p>}
              </Panel>
              <Panel icon={<Link2 size={15} />} title="Addresses">
                {urls.map((u) => (
                  <a key={u.label} href={u.href} target="_blank" rel="noreferrer" className="flex items-center gap-3 rounded-xl border border-line px-3 py-2 hover:bg-surface2">
                    <Globe size={13} className="shrink-0 text-muted" />
                    <span className="min-w-0 flex-1 truncate font-mono text-[12px]">{u.label}</span>
                    <ExternalLink size={12} className="text-muted" />
                  </a>
                ))}
                {owner && <button className="text-xs font-medium text-muted hover:text-fg" onClick={() => setTab("domains")}>Manage domains and DNS →</button>}
              </Panel>
              {owner && (
          <Panel icon={<Trash2 size={15} />} title="Remove site" danger
            sub={`Deletes its files and Caddy site from the server${site.plan.kind === "build" ? ", and the build workflow, build branch and .env secret from the repository" : ""}.`}>
            <div className="flex justify-end"><Button variant="danger" size="sm" onClick={() => setDel(true)}><Trash2 size={13} /> Remove {site.name}</Button></div>
          </Panel>
        )}
            </div>
          </div>
        )}
        {current === "domains" && <div className="max-w-3xl">{owner && (
          <Panel icon={<Globe size={15} />} title="Addresses" sub="Where the site answers.">
            {urls.filter((u) => u.auto).map((u) => (
              <div key={u.label} className="flex items-center gap-3 rounded-xl border border-line px-3 py-2">
                <Link2 size={13} className="shrink-0 text-muted" />
                <a href={u.href} target="_blank" rel="noreferrer" className="min-w-0 flex-1 truncate font-mono text-[12px] hover:underline">{u.label}</a>
                <Badge tone="ok">Automatic</Badge>
              </div>
            ))}
            {site.atPath && <p className="flex gap-2 text-[11px] text-muted"><Info size={13} className="shrink-0" />Sandboxed under the dashboard's domain: storage lasts until the page reloads. Add your own domain for full browser features.</p>}
            <Field label="Your own domains" hint="One per line. Caddy gets the HTTPS certificate for each.">
              <div className="relative">
                <Globe size={14} className="pointer-events-none absolute left-4 top-3.5 text-muted" />
                <Textarea rows={3} value={domains} onChange={(e) => setDomains(e.target.value)} placeholder="www.example.com" className="pl-10 font-mono text-[12px]" />
              </div>
            </Field>
            {(site.domains.length > 0 || domains.trim()) && <DnsMode manual={manual} onChange={setManual} connected={(cloudflare.data?.accounts?.length ?? 0) > 0} />}
            {manual !== !!site.dnsManual ? <p className="text-[11px] text-muted">Save to switch.</p>
              : site.dnsManual ? <ManualDNS site={site} ip={ip} /> : <SiteDNS site={site} onChange={(st) => data.setData({ ...data.data!, site: st })} />}
          </Panel>
        )}</div>}
        {current === "deploy" && (
          <div className="grid items-start gap-6 xl:grid-cols-2">
            {owner && (
          <Panel icon={<Hammer size={15} />} title="Deploying" sub={site.plan.kind === "docker" ? "How the app is reached." : site.plan.kind === "build" ? "How GitHub Actions builds it." : "Which files are published."}>
            <ToggleRow label="Deploy every push" desc={site.plan.kind === "build" ? "Each build GitHub Actions finishes goes live." : `Each push to ${site.branch} goes live.`} checked={auto} onChange={setAuto} />
            <PlanFields plan={plan} onChange={setPlan} />
          </Panel>
        )}
            {owner && site.plan.kind !== "static" && envText !== null && (
          <Panel icon={<KeyRound size={15} />} title=".env" sub={envFromExample ? `Filled from ${env.data && "exampleFile" in env.data ? env.data.exampleFile : ""}: put in your values and save.` : site.plan.kind === "docker" ? "Given to the app's container; saving redeploys it." : "Used when building, so it ends up in the site's JavaScript."}>
            <Textarea rows={5} value={envText} onChange={(e) => setEnvText(e.target.value)} className="font-mono text-[12px]" placeholder="VITE_API_URL=https://api.example.com" />
            <div className="flex flex-wrap justify-end gap-2">
              {env.data && "env" in env.data && env.data.example && envText !== env.data.example && (
                <Button size="sm" variant="ghost" onClick={() => setEnvText(env.data && "example" in env.data ? env.data.example ?? "" : "")}><FileCode2 size={13} /> Load {env.data.exampleFile}</Button>
              )}
              <Button size="sm" variant="secondary" disabled={envText === (env.data && "env" in env.data ? env.data.env : "")} onClick={saveEnv}><RefreshCw size={13} /> {site.plan.kind === "docker" ? "Save and redeploy" : "Save and rebuild"}</Button>
            </div>
          </Panel>
        )}
          </div>
        )}
        {current === "caddy" && <div className="max-w-4xl"><SiteCaddy site={site} editing={caddyText} setEditing={setCaddyText}
          onSaved={(st) => { data.setData({ ...data.data!, site: st }); setCaddyText(null); }} /></div>}
      </div>
      <ConfirmDialog open={del} title={`Remove ${site.name}?`} confirmLabel="Remove" danger onConfirm={remove} onCancel={() => setDel(false)}
        message={site.plan.kind === "docker" ? "Its containers, images, code and Caddy site are deleted from the server. Named volumes (its data) stay." : `Its files in /var/www and its Caddy site are deleted from the server${site.plan.kind === "build" ? "; its build workflow, the fleetwatch-build branch and the .env secret are removed from the repository (unless another site builds from it)" : ""}.`} />
    </>
  );
}
