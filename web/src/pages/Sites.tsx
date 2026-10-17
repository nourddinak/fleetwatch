import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ExternalLink, GitBranch, Plus, RefreshCw, Rocket, Settings2, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, Drawer, EmptyState, ErrorState, Field, Gate, Input, LoadingState, Modal, PageHeader, SectionTitle, Select, Spinner, Textarea, ToggleRow, type Tone } from "../components/ui";

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

function NewSite({ onClose, onCreated }: { onClose: () => void; onCreated: (s: Site) => void }) {
  const { servers, toast } = useStore();
  const repos = useApi<{ repo: { fullName: string; defaultBranch: string } }[]>("/api/integrations/github/repos");
  const [f, setF] = useState({ repo: "", serverId: "", branch: "", name: "", domains: "", env: "" });
  const [plan, setPlan] = useState<Plan | null>(null);
  const [exampleFile, setExampleFile] = useState("");
  const [busy, setBusy] = useState("");
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
  return (
    <Modal open wide onClose={onClose} title="New site"
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button disabled={!plan || !f.serverId || !!busy} onClick={create}>{busy === "create" && <Spinner size={14} />} Create site</Button></>}>
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Repository">
            {repos.error ? <p className="text-sm text-crit">{repos.error}</p> : (
              <Select value={f.repo} onChange={(e) => { setF({ ...f, repo: e.target.value, branch: "" }); detect(e.target.value, ""); }}>
                <option value="">{repos.data ? "Pick a repository…" : "Loading…"}</option>
                {repos.data?.map(({ repo }) => <option key={repo.fullName} value={repo.fullName}>{repo.fullName}</option>)}
              </Select>
            )}
          </Field>
          <Field label="Server">
            <Select value={f.serverId} onChange={(e) => setF({ ...f, serverId: e.target.value })}>
              {enrolled.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </Select>
          </Field>
          <Field label="Branch" hint="Pushes to it are deployed.">
            <Input value={f.branch} onChange={(e) => setF({ ...f, branch: e.target.value })} onBlur={() => f.repo && detect(f.repo, f.branch)} placeholder="main" />
          </Field>
          <Field label="Name" hint="Folder /var/www/<name>. Empty: the repository's name.">
            <Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} />
          </Field>
        </div>
        {busy === "detect" && <LoadingState label="Looking at the repository…" />}
        {plan && (
          <div className="space-y-3 rounded-2xl bg-surface2 p-4">
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone="info">{plan.framework}</Badge>
              <span className="text-sm text-muted">{plan.kind === "build" ? "Built by GitHub Actions on every push, then deployed." : "Published as is on every push."}</span>
            </div>
            {plan.note && <p className="text-xs text-warn">{plan.note}</p>}
            <PlanFields plan={plan} onChange={setPlan} />
            {plan.kind === "build" && (
              <Field label=".env for the build" hint={`${exampleFile ? `Filled from ${exampleFile}: put in your values. ` : ""}Stored encrypted here and as the FLEETWATCH_ENV secret on GitHub; written to .env before building. Anything it holds ends up in the public JavaScript, so no server secrets.`}>
                <Textarea rows={4} value={f.env} onChange={(e) => setF({ ...f, env: e.target.value })} placeholder="VITE_API_URL=https://api.example.com" />
              </Field>
            )}
          </div>
        )}
        <Field label="Your domains (optional)" hint="Point each at the server (DNS A record); Caddy gets the HTTPS certificate. A preview address is added on its own.">
          <Input value={f.domains} onChange={(e) => setF({ ...f, domains: e.target.value })} placeholder="www.example.com, example.com" />
        </Field>
        <p className="text-xs text-muted">If the repository has a Caddyfile at its top, Caddy uses it for this site instead of the generated one.</p>
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
