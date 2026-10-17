import { useEffect, useState, type InputHTMLAttributes, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Check, ExternalLink, FileCode2, FolderGit2, GitBranch, Globe, Hammer, Info, KeyRound, Link2, Lock, Plus, RefreshCw, Rocket, Server as ServerIcon, Settings2, Trash2, Workflow } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, Drawer, EmptyState, ErrorState, Field, Gate, Github, Input, LoadingState, Modal, PageHeader, SearchInput, SectionTitle, Spinner, Textarea, ToggleRow, type Tone } from "../components/ui";
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

export function Sites() {
  const { me, subscribeLive } = useStore();
  const owner = !me || me.role === "owner";
  const list = useApi<Site[]>("/api/sites");
  const gh = useApi<{ github: { connected: boolean } }>(owner ? "/api/integrations" : "/api/sites");
  const { setData } = list;
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  useEffect(() => subscribeLive((m) => {
    if (m.type === "site") setData((l) => (l ? (l.some((x) => x.id === m.data.id) ? l.map((x) => (x.id === m.data.id ? m.data : x)) : [...l, m.data]) : l));
    if (m.type === "site.removed") setData((l) => l?.filter((x) => x.id !== m.data.id) ?? l);
  }), [subscribeLive, setData]);
  const connected = !owner || (gh.data && "github" in gh.data ? gh.data.github.connected : true);
  return (
    <>
      <PageHeader title="Sites" sub="Static sites from your GitHub repositories, served by Caddy from /var/www on the server you pick. Plain HTML is published as is; React, Vite, Next and the like are built by GitHub Actions. Every push deploys again."
        actions={owner && connected && <Button onClick={() => setAdding(true)}><Plus size={15} /> New site</Button>} />
      <Gate>
        {list.error ? <ErrorState message={list.error} onRetry={list.reload} /> : !list.data ? <LoadingState /> : !connected ? (
          <EmptyState icon={<Rocket size={20} />} title="Connect GitHub first" desc="Sites are deployed from GitHub repositories." action={<Link to="/settings/integrations"><Button>Open Integrations</Button></Link>} />
        ) : list.data.length === 0 ? (
          <EmptyState icon={<Rocket size={20} />} title="No sites yet" desc="Pick a repository and a server: the site is online in a minute, at an address of its own." action={owner && <Button onClick={() => setAdding(true)}><Plus size={15} /> New site</Button>} />
        ) : (
          <div className="space-y-4">{list.data.map((s) => <SiteCard key={s.id} site={s} onOpen={() => setOpen(s.id)} />)}</div>
        )}
      </Gate>
      {adding && <NewSite onClose={() => setAdding(false)} onCreated={(s) => { setAdding(false); list.setData((l) => [...(l ?? []).filter((x) => x.id !== s.id), s]); }} />}
      {open && <SiteDrawer id={open} onClose={() => setOpen(null)} />}
    </>
  );
}

function SiteCard({ site, onOpen }: { site: Site; onOpen: () => void }) {
  const { getServer, me, toast } = useStore();
  const [tone, label] = STATUS[site.status];
  const urls = [site.atPath ? `${location.host}/sites/${site.name}/` : site.preview, ...site.domains.filter((d) => d !== site.preview)].filter(Boolean);
  const redeploy = async () => {
    try {
      await api("POST", `/api/sites/${site.id}/redeploy`);
      toast(site.plan.kind === "build" ? "Building again on GitHub" : "Deploying");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <button onClick={onOpen} className="min-w-0 flex-1 text-left">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-semibold hover:underline">{site.name}</span>
            <Badge tone={tone} dot pulse={site.status === "building" || site.status === "deploying"}>{label}</Badge>
            <Badge>{site.plan.framework}</Badge>
          </div>
          <div className="mt-0.5 text-xs text-muted">{site.repo} <GitBranch size={11} className="inline" /> {site.branch} → {getServer(site.serverId)?.name ?? "a removed server"}:/var/www/{site.name}</div>
        </button>
        <div className="flex items-center gap-1">
          {me?.role !== "viewer" && <Button size="sm" variant="ghost" onClick={redeploy} title={site.plan.kind === "build" ? "Build and deploy again" : "Deploy again"} disabled={site.status === "deploying"}>{site.status === "deploying" ? <Spinner size={14} /> : <RefreshCw size={14} />}</Button>}
          <Button size="sm" variant="ghost" onClick={onOpen} title="Settings and history"><Settings2 size={14} /></Button>
        </div>
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        {urls.map((u) => (
          <a key={u} href={`https://${u}`} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 rounded-full border border-line px-3 py-1 text-xs hover:border-fg/40">
            {u} <ExternalLink size={11} />
          </a>
        ))}
        {urls.length === 0 && <span className="text-xs text-muted">No address yet: add your domain in its settings (the server has no public IPv4 for a preview address).</span>}
        {site.atPath && <span className="self-center text-[11px] text-muted">Sandboxed under the dashboard's domain: no cookies or localStorage. Add your own domain for those.</span>}
      </div>
      <div className="mt-3 text-xs text-muted">
        {site.commit ? <>Deployed <span className="font-mono">{site.commit.slice(0, 7)}</span> {site.message ? `“${site.message}”` : ""} {relTime(site.deployedAt)}</> : "Not deployed yet"}
        {site.buildUrl && site.plan.kind === "build" && <> · <a className="underline" href={site.buildUrl} target="_blank" rel="noreferrer">last build</a></>}
        {!site.autoDeploy && " · automatic deploys off"}
      </div>
      {site.error && (
        <p className="mt-2 text-sm text-crit">
          {site.error}
          {site.error.includes("permissions") && me?.role !== "viewer" && (
            <> · <Link to="/settings/integrations" className="underline">How to grant them</Link> · <button className="underline" onClick={redeploy}>Retry</button></>
          )}
        </p>
      )}
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

function NewSite({ onClose, onCreated }: { onClose: () => void; onCreated: (s: Site) => void }) {
  const { servers, toast } = useStore();
  const repos = useApi<{ repo: { fullName: string; defaultBranch: string; private: boolean } }[]>("/api/integrations/github/repos");
  const [f, setF] = useState({ repo: "", serverId: "", branch: "", name: "", domains: "", env: "" });
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
      toast("Site removed from the server");
      onClose();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const dirty = domains !== site.domains.join("\n") || auto !== site.autoDeploy || JSON.stringify(plan) !== JSON.stringify(site.plan);
  return (
    <Drawer open onClose={onClose} title={site.name}>
      <div className="space-y-6">
        <p className="text-sm text-muted">{site.repo} · {site.branch} → {getServer(site.serverId)?.name}:/var/www/{site.name}</p>
        {owner && (
          <section className="space-y-3">
            <SectionTitle title="Addresses" />
            {site.atPath ? (
              <p className="text-xs text-muted">Always at <a className="underline" href={`/sites/${site.name}/`} target="_blank" rel="noreferrer">{location.host}/sites/{site.name}/</a> (this dashboard's domain).</p>
            ) : site.preview && <p className="text-xs text-muted">Preview address (always on): <a className="underline" href={`https://${site.preview}`} target="_blank" rel="noreferrer">{site.preview}</a></p>}
            <Textarea rows={3} value={domains} onChange={(e) => setDomains(e.target.value)} placeholder="www.example.com" />
            <p className="text-xs text-muted">One domain per line. In your DNS, add an A record for each pointing to {ip || "the server's IP"}.</p>
            <ToggleRow label="Deploy every push" desc={site.plan.kind === "build" ? "Each build GitHub Actions finishes goes live." : `Each push to ${site.branch} goes live.`} checked={auto} onChange={setAuto} />
            <PlanFields plan={plan} onChange={setPlan} />
            <Button size="sm" disabled={!dirty} onClick={save}>Save</Button>
          </section>
        )}
        {owner && site.plan.kind === "build" && envText !== null && (
          <section className="space-y-2">
            <SectionTitle title=".env" sub={`Used when building (and so visible in the site's JavaScript).${env.data && "env" in env.data && !env.data.env && env.data.exampleFile ? ` Filled from ${env.data.exampleFile}: put in your values and save.` : ""}`} />
            <Textarea rows={5} value={envText} onChange={(e) => setEnvText(e.target.value)} className="font-mono text-xs" />
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="secondary" disabled={envText === (env.data && "env" in env.data ? env.data.env : "")} onClick={saveEnv}>Save and rebuild</Button>
              {env.data && "env" in env.data && env.data.example && envText !== env.data.example && (
                <Button size="sm" variant="ghost" onClick={() => setEnvText(env.data && "example" in env.data ? env.data.example ?? "" : "")}>Load {env.data.exampleFile}</Button>
              )}
            </div>
          </section>
        )}
        <section>
          <SectionTitle title="Deploys" />
          {data.data!.history.length === 0 && <p className="text-sm text-muted">None yet.</p>}
          <ul className="divide-y divide-line">
            {data.data!.history.map((d, i) => (
              <li key={i} className="py-2 text-sm">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone={d.ok ? "ok" : "crit"}>{d.ok ? "live" : "failed"}</Badge>
                  <span className="font-mono text-xs">{d.commit.slice(0, 7)}</span>
                  <span className="min-w-0 flex-1 truncate">{d.message}</span>
                  <span className="text-xs text-muted">{d.by} · {relTime(d.ts)}</span>
                </div>
                {d.error && <p className="mt-1 text-xs text-crit">{d.error}</p>}
                {d.log && <button className="mt-1 text-xs text-muted underline" onClick={() => setLog(log === i ? null : i)}>{log === i ? "Hide" : "Show"} log</button>}
                {log === i && <pre className="mt-1 whitespace-pre-wrap rounded-xl bg-surface2 p-2 font-mono text-[11px]">{d.log}</pre>}
              </li>
            ))}
          </ul>
        </section>
        {owner && (
          <Button variant="danger" size="sm" onClick={() => setDel(true)}><Trash2 size={14} /> Remove site</Button>
        )}
      </div>
      <ConfirmDialog open={del} title={`Remove ${site.name}?`} confirmLabel="Remove" danger onConfirm={remove} onCancel={() => setDel(false)}
        message={`Its files in /var/www and its Caddy site are deleted from the server. The repository keeps its build workflow and the ${"fleetwatch-build"} branch; delete them on GitHub if no server uses them.`} />
    </Drawer>
  );
}
