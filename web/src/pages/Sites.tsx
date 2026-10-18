import { useEffect, useState, type InputHTMLAttributes, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { AlertTriangle, Check, CheckCircle2, Clock, ExternalLink, FileCode2, FolderGit2, GitBranch, GitCommitHorizontal, Globe, Hammer, History, Info, KeyRound, Link2, Lock, PauseCircle, Plus, RefreshCw, Rocket, Server as ServerIcon, Settings2, Terminal, Trash2, Workflow } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, Drawer, EmptyState, ErrorState, Field, Gate, Github, Input, LoadingState, Modal, PageHeader, SearchInput, SectionTitle, Spinner, Textarea, ToggleRow, toneDot, type Tone } from "../components/ui";
import { cn } from "../utils/cn";

interface Plan {
  kind: "static" | "build";
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
  preview: string;
  autoDeploy: boolean;
  envKeys: string[];
  status: "building" | "deploying" | "live" | "failed" | "waiting";
  commit: string;
  message: string;
  deployedAt: number;
  error?: string;
  buildUrl?: string;
  createdAt: number;
  atPath: boolean;
}
interface Deploy {
  ts: number;
  commit: string;
  message: string;
  ok: boolean;
  error?: string;
  log?: string;
  by: string;
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

export function Sites({ serverId }: { serverId?: string } = {}) {
  const { me, subscribeLive } = useStore();
  const owner = !me || me.role === "owner";
  const all = useApi<Site[]>("/api/sites");
  const list = { ...all, data: serverId ? all.data?.filter((s) => s.serverId === serverId) : all.data };
  const gh = useApi<{ github: { connected: boolean } }>(owner ? "/api/integrations" : "/api/sites");
  const { setData } = all;
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => subscribeLive((m) => {
    if (m.type === "site") setData((l) => (l ? (l.some((x) => x.id === m.data.id) ? l.map((x) => (x.id === m.data.id ? m.data : x)) : [...l, m.data]) : l));
    if (m.type === "site.removed") setData((l) => l?.filter((x) => x.id !== m.data.id) ?? l);
  }), [subscribeLive, setData]);
  const connected = !owner || (gh.data && "github" in gh.data ? gh.data.github.connected : true);
  return (
    <>
      {serverId ? (
        <SectionTitle icon={<Rocket size={15} />} title="Sites on this server" sub="Static sites deployed here from GitHub, in /var/www."
          action={owner && connected && <Button size="sm" onClick={() => setAdding(true)}><Plus size={13} /> New site</Button>} />
      ) : (
        <PageHeader title="Sites" sub="Static sites from your GitHub repositories, served by Caddy from /var/www on the server you pick. Plain HTML is published as is; React, Vite, Next and the like are built by GitHub Actions. Every push deploys again."
          actions={owner && connected && <Button onClick={() => setAdding(true)}><Plus size={15} /> New site</Button>} />
      )}
      <Gate>
        {list.error ? <ErrorState message={list.error} onRetry={list.reload} /> : !list.data ? <LoadingState /> : !connected ? (
          <EmptyState icon={<Rocket size={20} />} title="Connect GitHub first" desc="Sites are deployed from GitHub repositories." action={<Link to="/settings/integrations"><Button>Open Integrations</Button></Link>} />
        ) : list.data.length === 0 ? (
          <EmptyState icon={<Rocket size={20} />} title="No sites yet" desc={serverId ? "Deploy a GitHub repository to this server: it is online in a minute." : "Pick a repository and a server: the site is online in a minute, at an address of its own."} action={owner && <Button onClick={() => setAdding(true)}><Plus size={15} /> New site</Button>} />
        ) : (
          <div className="space-y-4">
            <div className="flex flex-wrap gap-2 text-xs">
              {([["live", "live", CheckCircle2], ["building", "building", Workflow], ["failed", "failed", AlertTriangle]] as const).map(([k, word, Icon]) => {
                const n = list.data!.filter((x) => (k === "building" ? x.status === "building" || x.status === "deploying" : x.status === k)).length;
                return n > 0 && <span key={k} className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-muted"><Icon size={13} /> {n} {word}</span>;
              })}
              <span className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1.5 text-muted"><Rocket size={13} /> {list.data.length} {list.data.length === 1 ? "site" : "sites"}</span>
            </div>
            <div className="grid gap-4 xl:grid-cols-2">{list.data.map((s) => <SiteCard key={s.id} site={s} onOpen={() => setOpen(s.id)} />)}</div>
          </div>
        )}
      </Gate>
      {adding && <NewSite serverId={serverId} onClose={() => setAdding(false)} onCreated={(s) => { setAdding(false); all.setData((l) => [...(l ?? []).filter((x) => x.id !== s.id), s]); }} />}
      {open && <SiteDrawer id={open} onClose={() => setOpen(null)} />}
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
      {site.plan.kind === "build" ? <Workflow size={size * 0.4} /> : <FileCode2 size={size * 0.4} />}
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
            {site.error.includes("permissions") && <Link to="/settings/integrations" className="inline-flex items-center gap-1 underline"><KeyRound size={12} /> How to grant them</Link>}
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

function NewSite({ onClose, onCreated, serverId }: { onClose: () => void; onCreated: (s: Site) => void; serverId?: string }) {
  const { servers, toast } = useStore();
  const repos = useApi<{ repo: { fullName: string; defaultBranch: string; private: boolean } }[]>("/api/integrations/github/repos");
  const [f, setF] = useState({ repo: "", serverId: serverId ?? "", branch: "", name: "", domains: "", env: "" });
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
      const r = await api<{ plan: Plan; branch: string; exampleFile: string; example: string }>("POST", "/api/sites/detect", { repo, branch });
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
        domains: f.domains.split(/[\s,]+/).filter(Boolean), env: plan?.kind === "build" ? f.env : "" });
      toast(s.plan.kind === "build" ? "Site added: GitHub Actions is building it" : "Site added: deploying");
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
          <span className="grid h-9 w-9 place-items-center rounded-xl bg-fg text-bg"><Rocket size={16} /></span>
          <span>
            <span className="block">New site</span>
            <span className="block text-xs font-normal text-muted">Deploy a GitHub repository to one of your servers</span>
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
                  <p className="px-3 py-3 text-sm text-muted">No repository matches. <Link to="/settings/integrations" className="underline">Give the app access to more</Link></p>
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
                  <span className="grid h-9 w-9 place-items-center rounded-xl border border-line bg-surface text-fg">{plan.kind === "build" ? <Workflow size={16} /> : <FileCode2 size={16} />}</span>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 text-sm font-medium">{plan.framework} <Badge tone={plan.kind === "build" ? "info" : "ok"}>{plan.kind === "build" ? "Build" : "Static"}</Badge></div>
                    <p className="text-xs text-muted">{plan.kind === "build" ? "GitHub Actions builds every push, then the output goes live." : "The files are published as they are on every push."}</p>
                  </div>
                </div>
                {plan.note && <p className="flex gap-2 rounded-xl bg-warn-soft px-3 py-2 text-xs text-warn"><Info size={14} className="mt-px shrink-0" />{plan.note}</p>}
                <PlanFields plan={plan} onChange={setPlan} />
                {plan.kind === "build" && (
                  <Field label={exampleFile ? `.env for the build (from ${exampleFile})` : ".env for the build"}
                    hint="Encrypted here and stored as a GitHub Actions secret. It ends up in the public JavaScript, so no server secrets.">
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
          <Field label="Your own domains (optional)" hint="Point each at the server with a DNS A record; Caddy gets the HTTPS certificate.">
            <IconInput icon={<Globe size={14} />} value={f.domains} onChange={(e) => setF({ ...f, domains: e.target.value })} placeholder="example.com, www.example.com" />
          </Field>
          <p className="flex items-center gap-2 text-[11px] text-muted"><FileCode2 size={12} className="shrink-0" />A Caddyfile at the top of the repository is used for the site instead of the generated one.</p>
        </Step>
      </div>
    </Modal>
  );
}

/** A block of the site drawer: icon, title, then its content. */
function Panel({ icon, title, sub, danger, children }: { icon: ReactNode; title: string; sub?: ReactNode; danger?: boolean; children: ReactNode }) {
  return (
    <section className={cn("rounded-2xl border p-4", danger ? "border-crit/30" : "border-line")}>
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

function SiteDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { me, getServer, toast } = useStore();
  const owner = !me || me.role === "owner";
  const data = useApi<{ site: Site; history: Deploy[] }>(`/api/sites/${id}`);
  const env = useApi<{ env: string; exampleFile?: string; example?: string }>(owner ? `/api/sites/${id}/env` : `/api/sites/${id}`);
  const [domains, setDomains] = useState<string | null>(null);
  const [auto, setAuto] = useState(true);
  const [plan, setPlan] = useState<Plan | null>(null);
  const [envText, setEnvText] = useState<string | null>(null);
  const [del, setDel] = useState(false);
  const [log, setLog] = useState<number | null>(null);
  const site = data.data?.site;
  useEffect(() => {
    if (!site) return;
    setDomains(site.domains.join("\n"));
    setAuto(site.autoDeploy);
    setPlan(site.plan);
  }, [site]);
  useEffect(() => {
    if (env.data && "env" in env.data) setEnvText(env.data.env || env.data.example || "");
  }, [env.data]);
  if (!site || domains === null || !plan) return <Drawer open onClose={onClose} title="Site"><LoadingState /></Drawer>;
  const ip = getServer(site.serverId)?.ip;
  const [tone, label] = STATUS[site.status];
  const urls = addresses(site);
  const save = async () => {
    try {
      data.setData({ ...data.data!, site: await api<Site>("PUT", `/api/sites/${id}`, { domains: domains.split(/[\s,]+/).filter(Boolean), autoDeploy: auto, plan }) });
      toast("Saved");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const saveEnv = async () => {
    try {
      await api("PUT", `/api/sites/${id}/env`, { env: envText });
      toast(".env saved: building again");
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
      onClose();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const dirty = domains !== site.domains.join("\n") || auto !== site.autoDeploy || JSON.stringify(plan) !== JSON.stringify(site.plan);
  const envFromExample = env.data && "env" in env.data && !env.data.env && env.data.exampleFile;
  return (
    <Drawer open onClose={onClose}
      title={
        <span className="flex items-center gap-3">
          <SiteIcon site={site} size={36} />
          <span className="min-w-0">
            <span className="flex items-center gap-2"><span className="truncate">{site.name}</span><Badge tone={tone} dot pulse={site.status === "building" || site.status === "deploying"}>{label}</Badge></span>
            <span className="block truncate text-xs font-normal text-muted">{site.plan.framework} · {site.plan.kind === "build" ? "built by GitHub Actions" : "published as is"}</span>
          </span>
        </span>
      }>
      <div className="space-y-4">
        <div className="grid gap-2 rounded-2xl bg-surface2/60 p-4 text-xs">
          <a href={`https://github.com/${site.repo}`} target="_blank" rel="noreferrer" className="flex min-w-0 items-center gap-2 hover:underline"><Github size={13} /><span className="truncate">{site.repo}</span><ExternalLink size={11} className="text-muted" /></a>
          <span className="flex items-center gap-2"><GitBranch size={13} /><span className="font-mono">{site.branch}</span></span>
          <span className="flex min-w-0 items-center gap-2"><ServerIcon size={13} /><span className="truncate font-mono">{getServer(site.serverId)?.name ?? "removed server"}:/var/www/{site.name}</span></span>
          {site.commit && <span className="flex min-w-0 items-center gap-2"><GitCommitHorizontal size={13} /><span className="font-mono">{site.commit.slice(0, 7)}</span><span className="truncate text-muted">{site.message}</span><span className="ml-auto shrink-0 text-muted">{relTime(site.deployedAt)}</span></span>}
          <div className="mt-1 flex flex-wrap gap-2">
            {urls[0] && <a href={urls[0].href} target="_blank" rel="noreferrer"><Button size="sm"><ExternalLink size={13} /> Open site</Button></a>}
            {me?.role !== "viewer" && <Button size="sm" variant="secondary" onClick={() => redeploySite(site, toast)} disabled={site.status === "deploying"}><RefreshCw size={13} /> {site.plan.kind === "build" ? "Rebuild" : "Redeploy"}</Button>}
            {site.buildUrl && site.plan.kind === "build" && <a href={site.buildUrl} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost"><Terminal size={13} /> Last build</Button></a>}
          </div>
        </div>

        <SiteError site={site} onRetry={() => redeploySite(site, toast)} />

        {owner && (
          <Panel icon={<Globe size={15} />} title="Addresses" sub="Where the site answers.">
            {urls.filter((u) => u.auto).map((u) => (
              <div key={u.label} className="flex items-center gap-3 rounded-xl border border-line px-3 py-2">
                <Link2 size={13} className="shrink-0 text-muted" />
                <a href={u.href} target="_blank" rel="noreferrer" className="min-w-0 flex-1 truncate font-mono text-[12px] hover:underline">{u.label}</a>
                <Badge tone="ok">Automatic</Badge>
              </div>
            ))}
            {site.atPath && <p className="flex gap-2 text-[11px] text-muted"><Info size={13} className="shrink-0" />Sandboxed under the dashboard's domain: storage lasts until the page reloads. Add your own domain for full browser features.</p>}
            <Field label="Your own domains" hint={<>One per line. In your DNS, add an A record for each pointing to <span className="font-mono">{ip || "the server's IP"}</span>.</>}>
              <div className="relative">
                <Globe size={14} className="pointer-events-none absolute left-4 top-3.5 text-muted" />
                <Textarea rows={3} value={domains} onChange={(e) => setDomains(e.target.value)} placeholder="www.example.com" className="pl-10 font-mono text-[12px]" />
              </div>
            </Field>
          </Panel>
        )}

        {owner && (
          <Panel icon={<Hammer size={15} />} title="Deploying" sub={site.plan.kind === "build" ? "How GitHub Actions builds it." : "Which files are published."}>
            <ToggleRow label="Deploy every push" desc={site.plan.kind === "build" ? "Each build GitHub Actions finishes goes live." : `Each push to ${site.branch} goes live.`} checked={auto} onChange={setAuto} />
            <PlanFields plan={plan} onChange={setPlan} />
            <div className="flex justify-end"><Button size="sm" disabled={!dirty} onClick={save}><Check size={13} /> Save changes</Button></div>
          </Panel>
        )}

        {owner && site.plan.kind === "build" && envText !== null && (
          <Panel icon={<KeyRound size={15} />} title=".env" sub={envFromExample ? `Filled from ${env.data && "exampleFile" in env.data ? env.data.exampleFile : ""}: put in your values and save.` : "Used when building, so it ends up in the site's JavaScript."}>
            <Textarea rows={5} value={envText} onChange={(e) => setEnvText(e.target.value)} className="font-mono text-[12px]" placeholder="VITE_API_URL=https://api.example.com" />
            <div className="flex flex-wrap justify-end gap-2">
              {env.data && "env" in env.data && env.data.example && envText !== env.data.example && (
                <Button size="sm" variant="ghost" onClick={() => setEnvText(env.data && "example" in env.data ? env.data.example ?? "" : "")}><FileCode2 size={13} /> Load {env.data.exampleFile}</Button>
              )}
              <Button size="sm" variant="secondary" disabled={envText === (env.data && "env" in env.data ? env.data.env : "")} onClick={saveEnv}><RefreshCw size={13} /> Save and rebuild</Button>
            </div>
          </Panel>
        )}

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
                  {d.log && <button className="inline-flex items-center gap-1 hover:text-fg" onClick={() => setLog(log === i ? null : i)}><Terminal size={11} /> {log === i ? "Hide log" : "Log"}</button>}
                </div>
                {d.error && <p className="mt-1 text-xs text-crit">{upper(d.error)}</p>}
                {log === i && <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded-xl bg-surface2 p-3 font-mono text-[11px]">{d.log}</pre>}
              </li>
            ))}
          </ol>
        </Panel>

        {owner && (
          <Panel icon={<Trash2 size={15} />} title="Remove site" danger
            sub={`Deletes its files and Caddy site from the server${site.plan.kind === "build" ? ", and the build workflow, build branch and .env secret from the repository" : ""}.`}>
            <div className="flex justify-end"><Button variant="danger" size="sm" onClick={() => setDel(true)}><Trash2 size={13} /> Remove {site.name}</Button></div>
          </Panel>
        )}
      </div>
      <ConfirmDialog open={del} title={`Remove ${site.name}?`} confirmLabel="Remove" danger onConfirm={remove} onCancel={() => setDel(false)}
        message={`Its files in /var/www and its Caddy site are deleted from the server${site.plan.kind === "build" ? "; its build workflow, the fleetwatch-build branch and the .env secret are removed from the repository (unless another site builds from it)" : ""}.`} />
    </Drawer>
  );
}
