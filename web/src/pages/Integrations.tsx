import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ExternalLink, FolderGit2, GitBranch, GitCommitHorizontal, Info, Lock, RefreshCw, TriangleAlert, Users } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { DnsAccounts } from "../components/DnsAccounts";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, Input, LoadingState, SectionTitle, Spinner, Table, Td, Tr, Github, CopyButton } from "../components/ui";

interface App {
  id: number;
  slug: string;
  name: string;
  owner: string;
  htmlUrl: string;
  createdAt: number;
  createdBy: string;
}
interface Installation {
  id: number;
  account: string;
  accountType: string;
  repoSelection: "all" | "selected";
  suspended: boolean;
  missing?: string[];
}
interface Push {
  ts: number;
  repo: string;
  branch: string;
  commit: string;
  message: string;
  pusher: string;
}
interface GitHubStatus {
  connected: boolean;
  app?: App;
  installations: Installation[];
  pushes: Push[];
  installUrl?: string;
}
interface RepoItem {
  repo: { fullName: string; private: boolean; defaultBranch: string; htmlUrl: string; pushedAt: string };
  apps: { id: string; name: string; serverId: string; branch: string; commit: string }[];
}

/** Settings → Integrations: connect services. GitHub first. */
export function IntegrationsSettings() {
  const { subscribeLive, toast } = useStore();
  const data = useApi<{ github: GitHubStatus; publicUrl: string; webhooks: boolean; links: Record<string, { login: string; installations: number[]; linkedAt: number }> }>("/api/integrations");
  const { setData } = data;
  const [params, setParams] = useSearchParams();
  useEffect(() => {
    const gh = params.get("github");
    if (!gh) return;
    if (gh === "installed") toast("GitHub is connected");
    else toast(params.get("message") || "GitHub could not be connected", "err");
    setParams({}, { replace: true });
  }, [params, setParams, toast]);
  useEffect(() => subscribeLive((m) => {
    if (m.type === "github") setData((d) => (d ? { ...d, github: m.data } : d));
    if (m.type === "github.push") setData((d) => (d ? { ...d, github: { ...d.github, pushes: [m.data, ...d.github.pushes].slice(0, 30) } } : d));
  }), [subscribeLive, setData]);
  if (data.error) return <ErrorState message={data.error} onRetry={data.reload} />;
  if (!data.data) return <LoadingState />;
  return (
    <div className="space-y-6">
      <GitHubCard status={data.data.github} publicUrl={data.data.publicUrl} webhooks={data.data.webhooks} onChange={data.reload} />
      <DnsAccounts />
      {data.data.github.connected && data.data.github.app && <TeamGitHub app={data.data.github.app} installations={data.data.github.installations} publicUrl={data.data.publicUrl} links={data.data.links ?? {}} />}
      <Card>
        <SectionTitle icon={<Info size={15} />} title="What it is for" sub="Sites: deploy static sites from these repositories onto your servers, again on every push." action={<Link to="/sites"><Button size="sm" variant="secondary">Open Sites</Button></Link>} />
      </Card>
    </div>
  );
}

/** Team members link their own GitHub accounts: who did, and the app settings that allows. */
function TeamGitHub({ app, installations, publicUrl, links }: { app: App; installations: Installation[]; publicUrl: string; links: Record<string, { login: string; installations: number[]; linkedAt: number }> }) {
  const members = useApi<{ id: string; username: string; displayName: string }[]>("/api/members");
  const org = installations.find((i) => i.account === app.owner && i.accountType === "Organization");
  const settings = org ? `https://github.com/organizations/${app.owner}/settings/apps/${app.slug}` : `https://github.com/settings/apps/${app.slug}`;
  const callback = `${publicUrl}/api/integrations/github/oauth`;
  const who = (key: string) => (key === "owner" ? "You" : members.data?.find((m) => m.id === key)?.displayName || members.data?.find((m) => m.id === key)?.username || "A removed member");
  const list = Object.entries(links);
  return (
    <Card>
      <SectionTitle icon={<Users size={15} />} title="Team members' GitHub" sub="Each person links their own GitHub account on the Sites page and deploys from their repositories. They manage the sites they add; you can manage all of them." />
      <div className="mb-4 space-y-2 rounded-2xl bg-surface2/60 p-4 text-sm">
        <div className="font-medium">Once, if this app was connected before team GitHub existed:</div>
        <ol className="list-decimal space-y-1.5 pl-5 text-muted">
          <li>In <a className="underline" href={`${settings}/advanced`} target="_blank" rel="noreferrer">the app's advanced settings</a>, press <b>Make public</b>, so others can install it on their own accounts. Only people who sign in through this dashboard can use it.</li>
          <li>
            In <a className="underline" href={settings} target="_blank" rel="noreferrer">the app's general settings</a>, add this <b>Callback URL</b> and save:
            <span className="mt-1 flex items-center gap-2"><code className="min-w-0 truncate rounded-lg bg-surface px-2 py-1 font-mono text-xs text-fg">{callback}</code><CopyButton text={callback} /></span>
          </li>
        </ol>
      </div>
      {list.length === 0 ? <p className="text-sm text-muted">Nobody has linked a GitHub account yet.</p> : (
        <ul className="divide-y divide-line">
          {list.map(([key, l]) => (
            <li key={key} className="flex flex-wrap items-center gap-3 py-2.5 text-sm">
              <span className="grid h-8 w-8 place-items-center rounded-full bg-surface2"><Github size={14} /></span>
              <span className="min-w-0 flex-1"><span className="font-medium">{l.login}</span> <span className="text-muted">· {who(key)}</span></span>
              <span className="text-xs text-muted">{l.installations.length} installation{l.installations.length === 1 ? "" : "s"} · linked {relTime(l.linkedAt)}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function GitHubCard({ status, publicUrl, webhooks, onChange }: { status: GitHubStatus; publicUrl: string; webhooks: boolean; onChange: () => void }) {
  const { toast } = useStore();
  const [org, setOrg] = useState("");
  const [busy, setBusy] = useState(false);
  const [disconnect, setDisconnect] = useState(false);
  const connect = async () => {
    setBusy(true);
    try {
      const r = await api<{ action: string; manifest: string }>("POST", "/api/integrations/github/manifest", { org });
      // GitHub's app manifest flow takes the manifest as a form POST.
      const form = document.createElement("form");
      form.method = "POST";
      form.action = r.action;
      const input = document.createElement("input");
      input.type = "hidden";
      input.name = "manifest";
      input.value = r.manifest;
      form.appendChild(input);
      document.body.appendChild(form);
      form.submit();
    } catch (e) {
      toast((e as ApiError).message, "err");
      setBusy(false);
    }
  };
  const refresh = async () => {
    try {
      await api("POST", "/api/integrations/github/refresh");
      onChange();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const remove = async () => {
    setDisconnect(false);
    try {
      await api("DELETE", "/api/integrations/github");
      toast("GitHub disconnected");
      onChange();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  if (!status.connected || !status.app) {
    return (
      <Card>
        <SectionTitle icon={<Github size={15} />} title="GitHub"
          sub="Creates a private GitHub App for this dashboard in one click, then you pick the repositories it may read. No password or long-lived token is stored: the dashboard asks GitHub for one-hour tokens." />
        <ul className="mb-4 space-y-1 text-sm text-muted">
          <li>• Reads the repositories you choose</li>
          <li>• For sites that need a build: adds a GitHub Actions workflow and its .env secret, and follows the builds</li>
          <li>• Gets told about pushes, so sites deploy again by themselves</li>
        </ul>
        {!webhooks && (
          <div className="mb-4 flex gap-2 rounded-2xl bg-warn-soft px-4 py-3 text-sm text-warn">
            <TriangleAlert size={16} className="mt-0.5 shrink-0" />
            <span>This dashboard's address is {publicUrl}. GitHub can only send push events to a public https address: connecting works, but pushes will not arrive until OPS_PUBLIC_URL is your https domain.</span>
          </div>
        )}
        <div className="flex flex-wrap items-end gap-3">
          <Field label="Organization (optional)" hint="Empty: the app is created on your own GitHub account." className="min-w-[14rem] flex-1">
            <Input value={org} onChange={(e) => setOrg(e.target.value)} placeholder="my-company" />
          </Field>
          <Button onClick={connect} disabled={busy}>{busy ? <Spinner size={14} /> : <Github size={15} />} Connect GitHub</Button>
        </div>
      </Card>
    );
  }
  const app = status.app;
  return (
    <>
      <Card>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2 font-semibold"><Github size={18} /> GitHub <Badge tone="ok" dot>connected</Badge></div>
            <div className="mt-1 text-sm text-muted">
              App <a href={app.htmlUrl} target="_blank" rel="noreferrer" className="underline">{app.name}</a> on <b>{app.owner}</b> · connected {relTime(app.createdAt)}{app.createdBy ? ` by ${app.createdBy}` : ""}
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="ghost" onClick={refresh} title="Reload from GitHub"><RefreshCw size={14} /></Button>
            <a href={status.installUrl} target="_blank" rel="noreferrer"><Button size="sm" variant="secondary"><ExternalLink size={14} /> Choose repositories</Button></a>
            <Button size="sm" variant="danger" onClick={() => setDisconnect(true)}>Disconnect</Button>
          </div>
        </div>
        {!webhooks && <p className="mt-3 text-xs text-warn">Push events cannot reach {publicUrl}: sites still deploy, checked every minute. Set OPS_PUBLIC_URL to your https domain for instant deploys.</p>}
        {status.installations.some((i) => i.missing?.length) && <GrantPermissions app={app} installations={status.installations} />}
        <div className="mt-4">
          {status.installations.length === 0 ? (
            <div className="rounded-2xl bg-surface2 p-4 text-sm">
              Not installed on any account yet. <a href={status.installUrl} target="_blank" rel="noreferrer" className="underline">Install it and pick repositories</a>, then come back here.
            </div>
          ) : (
            <div className="flex flex-wrap gap-2">
              {status.installations.map((i) => (
                <Badge key={i.id} tone={i.suspended ? "warn" : "neutral"}>{i.account} · {i.repoSelection === "all" ? "all repositories" : "selected repositories"}{i.suspended ? " · suspended" : ""}</Badge>
              ))}
            </div>
          )}
        </div>
      </Card>
      {status.installations.length > 0 && <Repos />}
      {status.pushes.length > 0 && (
        <Card>
          <SectionTitle icon={<GitCommitHorizontal size={15} />} title="Recent pushes" sub="As GitHub reports them." />
          <ul className="divide-y divide-line text-sm">
            {status.pushes.map((p, i) => (
              <li key={i} className="flex flex-wrap items-baseline gap-2 py-2">
                <span className="font-mono text-xs">{p.repo}</span>
                <Badge><GitBranch size={11} /> {p.branch}</Badge>
                <span className="font-mono text-xs text-muted">{p.commit.slice(0, 7)}</span>
                <span className="min-w-0 flex-1 truncate">{p.message}</span>
                <span className="text-xs text-muted">{p.pusher} · {relTime(p.ts)}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}
      <ConfirmDialog open={disconnect} title="Disconnect GitHub?" confirmLabel="Disconnect" danger onConfirm={remove} onCancel={() => setDisconnect(false)}
        message={<>The app is uninstalled from your accounts and its keys are deleted here. To remove the app itself from GitHub, delete it at <a className="underline" href={`${app.htmlUrl.replace("/apps/", "/settings/apps/")}/advanced`} target="_blank" rel="noreferrer">its settings</a>.</>} />
    </>
  );
}

/** The app was created before sites existed: the steps to give it the permissions they need. */
function GrantPermissions({ app, installations }: { app: App; installations: Installation[] }) {
  const org = installations.find((i) => i.account === app.owner && i.accountType === "Organization");
  const settings = org ? `https://github.com/organizations/${app.owner}/settings/apps/${app.slug}` : `https://github.com/settings/apps/${app.slug}`;
  const missing = [...new Set(installations.flatMap((i) => i.missing ?? []))];
  return (
    <div className="mt-4 space-y-2 rounded-2xl bg-warn-soft px-4 py-3 text-sm text-warn">
      <div className="font-semibold">Sites need more permissions than this app has ({missing.join(", ")})</div>
      <ol className="list-decimal space-y-1 pl-5 text-fg">
        <li>Open <a className="underline" href={`${settings}/permissions`} target="_blank" rel="noreferrer">the app's permissions on GitHub</a>.</li>
        <li>Under <b>Repository permissions</b> set <b>Contents</b>, <b>Workflows</b>, <b>Secrets</b> and <b>Actions</b> to <b>Read and write</b>. Press <b>Save changes</b>.</li>
        <li>
          Accept them for your account:{" "}
          {installations.map((i) => (
            <a key={i.id} className="mr-2 underline" target="_blank" rel="noreferrer"
              href={i.accountType === "Organization" ? `https://github.com/organizations/${i.account}/settings/installations/${i.id}` : `https://github.com/settings/installations/${i.id}`}>
              {i.account}
            </a>
          ))}
          → <b>Review request</b> → <b>Accept new permissions</b>.
        </li>
        <li>Come back here (this message goes away), then press <b>Retry</b> on the site.</li>
      </ol>
    </div>
  );
}

function Repos() {
  const { getServer } = useStore();
  const list = useApi<RepoItem[]>("/api/integrations/github/repos");
  if (list.error) return <ErrorState message={list.error} onRetry={list.reload} />;
  if (!list.data) return <Card><LoadingState label="Asking GitHub for the repositories…" /></Card>;
  return (
    <Card pad={false}>
      <div className="p-5 pb-2">
        <SectionTitle icon={<FolderGit2 size={15} />} title={`Repositories (${list.data.length})`} sub="Linked to your apps by the git remote of their folder." action={<Button size="sm" variant="ghost" onClick={list.reload}><RefreshCw size={14} /></Button>} />
      </div>
      {list.data.length === 0 ? (
        <div className="p-5 pt-0"><EmptyState title="No repositories" desc="Choose repositories on GitHub for the app." /></div>
      ) : (
        <Table head={["Repository", "Default branch", "Last push", "Apps here"]}>
          {list.data.map(({ repo, apps }) => (
            <Tr key={repo.fullName}>
              <Td>
                <a href={repo.htmlUrl} target="_blank" rel="noreferrer" className="flex items-center gap-1.5 font-medium hover:underline">
                  {repo.private && <Lock size={12} className="text-muted" />} {repo.fullName}
                </a>
              </Td>
              <Td className="font-mono text-xs">{repo.defaultBranch}</Td>
              <Td className="text-xs text-muted">{repo.pushedAt ? relTime(Date.parse(repo.pushedAt)) : "—"}</Td>
              <Td>
                {apps.length === 0 ? <span className="text-xs text-muted">—</span> : (
                  <div className="flex flex-wrap gap-1.5">
                    {apps.map((a) => (
                      <Link key={a.id} to={`/applications/${a.id}`} className="rounded-full border border-line px-2 py-0.5 text-xs hover:border-fg/40">
                        {a.name} <span className="text-muted">· {getServer(a.serverId)?.name} · {a.branch}@{a.commit.slice(0, 7)}</span>
                      </Link>
                    ))}
                  </div>
                )}
              </Td>
            </Tr>
          ))}
        </Table>
      )}
    </Card>
  );
}
