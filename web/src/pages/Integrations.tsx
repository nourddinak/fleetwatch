import { useEffect, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Activity, ArrowLeft, Container, ChevronRight, Globe, Mail, ShieldAlert, ExternalLink, FolderGit2, GitBranch, GitCommitHorizontal, Lock, RefreshCw, TriangleAlert, Users, Webhook } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { CloudflareSetup, DnsAccounts } from "../components/DnsAccounts";
import { MyWhatsApp, WhatsAppSetup } from "../components/WhatsApp";
import { goGitHub } from "./Sites";
import { AISetup } from "../components/AISetup";
import { MobileCard } from "../components/MobileCard";
import { CHANNEL_FIELDS, ChannelCard, type Channel } from "./Settings";
import { DockerRegistries } from "../components/DockerRegistries";
import { EmailSenders, useSenders } from "../components/EmailSenders";
import { TokenSettings } from "./Tokens";
import { WidgetCard } from "./StatusEditorTabs";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, Input, LoadingState, SectionTitle, Spinner, Table, Td, Toggle, Tr, Github, Cloudflare, WhatsApp, Gemini, Telegram, Discord, CopyButton, PageHeader, SearchInput } from "../components/ui";

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

/** Integrations: one card per service, or the settings of the one opened. */
export function IntegrationsSettings() {
  const { subscribeLive, toast } = useStore();
  const data = useApi<{ github: GitHubStatus; publicUrl: string; webhooks: boolean; links: Record<string, { login: string; installations: number[]; linkedAt: number }>;
    cloudflare?: { clientId: string; redirectUri: string; scopes: string[]; noAnalytics?: boolean } }>("/api/integrations");
  const { setData } = data;
  const [params, setParams] = useSearchParams();
  useEffect(() => {
    const gh = params.get("github");
    if (!gh) return;
    if (gh === "installed") toast("GitHub is connected");
    else toast(params.get("message") || "GitHub could not be connected", "err");
    params.delete("github");
    params.delete("message");
    setParams(params, { replace: true });
  }, [params, setParams, toast]);
  useEffect(() => subscribeLive((m) => {
    if (m.type === "github") setData((d) => (d ? { ...d, github: m.data } : d));
    if (m.type === "github.push") setData((d) => (d ? { ...d, github: { ...d.github, pushes: [m.data, ...d.github.pushes].slice(0, 30) } } : d));
  }), [subscribeLive, setData]);
  if (data.error) return <ErrorState message={data.error} onRetry={data.reload} />;
  if (!data.data) return <LoadingState />;
  const cf = data.data.cloudflare;
  const asked = params.get("tab") ?? "";
  const tab = asked === "cloudflare" && !cf ? "" : ["cloudflare", "whatsapp", "ai", "github", ...ALERT_TABS].includes(asked) ? asked : "";
  if (!tab) return <IntegrationCards github={data.data.github} cf={cf} open={(t) => setParams({ tab: t })} />;
  return (
    <div className="space-y-6">
      <button className="inline-flex items-center gap-1.5 text-xs text-muted hover:text-fg" onClick={() => setParams({})}><ArrowLeft size={14} /> All integrations</button>
      {ALERT_TABS.includes(tab) ? (
        <AlertChannel tab={tab} />
      ) : tab === "whatsapp" ? (
        <WhatsAppSetup />
      ) : tab === "ai" ? (
        <AISetup />
      ) : tab === "cloudflare" && cf ? (
        <>
          <CloudflareSetup clientId={cf.clientId} redirectUri={cf.redirectUri} scopes={cf.scopes} noAnalytics={cf.noAnalytics} onChange={data.reload} />
          <DnsAccounts key={cf.clientId} />
        </>
      ) : (
        <>
          <GitHubCard status={data.data.github} publicUrl={data.data.publicUrl} webhooks={data.data.webhooks} onChange={data.reload} />
          <GitHubSignIn />
          {data.data.github.connected && data.data.github.app && <TeamGitHub app={data.data.github.app} installations={data.data.github.installations} publicUrl={data.data.publicUrl} links={data.data.links ?? {}} />}
        </>
      )}
    </div>
  );
}

/** Settings-free home of the integrations: a page of its own, reached from the sidebar. */
export function IntegrationsPage() {
  const { me } = useStore();
  const owner = !me || me.role === "owner";
  return (
    <div>
      <PageHeader title="Integrations" sub={owner ? "Connect the services your sites and servers use." : "Your own accounts: the GitHub you deploy from, your Cloudflare, and where WhatsApp alerts reach you."} />
      {owner ? <IntegrationsSettings /> : <MemberIntegrations />}
    </div>
  );
}

interface MyGitHub {
  connected: boolean;
  canDeploy: boolean;
  owner: boolean;
  app?: string;
  link?: { login: string; installations: number[]; linkedAt: number };
}

/** A team member's own integrations: one card each, opening its settings. */
function MemberIntegrations() {
  const { toast } = useStore();
  const [params, setParams] = useSearchParams();
  const gh = useApi<MyGitHub>("/api/me/github");
  const dns = useApi<{ accounts: unknown[]; ready: boolean }>("/api/me/dns");
  const wa = useApi<{ available: boolean; person: { phone: string } }>("/api/me/whatsapp");
  const mine = useApi<Channel[]>("/api/me/channels");
  const tab = params.get("tab") ?? "";
  const back = <button className="mb-6 inline-flex items-center gap-1.5 text-xs text-muted hover:text-fg" onClick={() => setParams({})}><ArrowLeft size={14} /> All integrations</button>;
  if (tab === "github") {
    const g = gh.data;
    return (
      <div>
        {back}
        <Card>
          <SectionTitle icon={<Github size={17} />} title="Your GitHub" sub="Link your own GitHub account to deploy your sites from your repositories, and to sign in with GitHub." />
          {!g ? <Spinner /> : !g.connected ? (
            <p className="text-sm text-muted">The dashboard owner has not connected GitHub yet.</p>
          ) : !g.canDeploy ? (
            <p className="text-sm text-muted">Viewers do not deploy sites, so there is nothing to link.</p>
          ) : g.link ? (
            <div className="flex flex-wrap items-center gap-3">
              <span className="grid h-11 w-11 place-items-center rounded-2xl bg-surface2"><Github size={20} /></span>
              <div className="min-w-0 flex-1">
                <div className="font-medium">{g.link.login}</div>
                <div className="text-xs text-muted">{g.link.installations.length} account{g.link.installations.length === 1 ? "" : "s"} with the {g.app} app · linked {relTime(g.link.linkedAt)}</div>
              </div>
              <Button size="sm" variant="secondary" onClick={() => goGitHub("install", toast)}>Give access to more repositories</Button>
              <Button size="sm" variant="ghost" className="text-crit" onClick={async () => { try { await api("DELETE", "/api/me/github"); gh.reload(); toast("GitHub unlinked"); } catch (e) { toast((e as ApiError).message, "err"); } }}>Unlink</Button>
            </div>
          ) : (
            <Button onClick={() => goGitHub("connect", toast)}><Github size={14} /> Connect your GitHub</Button>
          )}
        </Card>
      </div>
    );
  }
  if (tab === "cloudflare") return <div>{back}<DnsAccounts /></div>;
  if (tab === "whatsapp") return <div>{back}<MyWhatsApp explain /></div>;
  if (MY_CHANNELS.includes(tab)) return <div>{back}<MyChannel provider={tab} /></div>;
  const items = [
    { key: "github", logo: <Github size={28} />, name: "GitHub", on: !!gh.data?.link,
      desc: "Your own GitHub account: deploy your sites from your repositories, and sign in with it.",
      status: gh.data?.link ? `@${gh.data.link.login}` : gh.data?.connected ? "Not linked yet" : "Not set up by the owner yet" },
    { key: "cloudflare", logo: <Cloudflare size={34} />, name: "Cloudflare", on: (dns.data?.accounts.length ?? 0) > 0,
      desc: "Sign in with your Cloudflare: your sites' domains point at their server by themselves, and you see their traffic.",
      status: dns.data?.accounts.length ? `${dns.data.accounts.length} account${dns.data.accounts.length === 1 ? "" : "s"} signed in` : dns.data?.ready ? "Not signed in yet" : "Not set up by the owner yet" },
    { key: "whatsapp", logo: <WhatsApp size={32} />, name: "WhatsApp", on: !!wa.data?.person.phone,
      desc: "Where on-call alerts and calls reach you, in your language, and your quiet hours.",
      status: wa.data?.person.phone ? `+${wa.data.person.phone}` : wa.data?.available ? "No number yet" : "Not set up by the owner yet" },
    ...MY_CHANNELS.map((p) => {
      const c = mine.data?.find((x) => x.provider === p);
      const on = !!c?.enabled && !!c.configured;
      return { key: p, logo: MY_LOGO[p], name: `My ${p === "email" ? "email" : CHANNEL_FIELDS[p as "telegram"].title} alerts`, on,
        desc: MY_DESC[p], status: on ? "Sending your alerts" : c?.configured ? "Set up, turned off" : "Not set up" };
    }),
  ];
  return (
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      {items.map((i) => (
        <button key={i.key} onClick={() => setParams({ tab: i.key })} className="group flex flex-col gap-4 rounded-3xl border border-line bg-surface p-5 text-left transition hover:-translate-y-0.5 hover:border-fg/20 hover:shadow-lg">
          <div className="flex items-start justify-between gap-3">
            <span className="grid h-14 w-14 place-items-center rounded-2xl bg-surface2">{i.logo}</span>
            <Badge tone={i.on ? "ok" : undefined}>{i.on ? "Connected" : "Not connected"}</Badge>
          </div>
          <div>
            <div className="text-base font-semibold">{i.name}</div>
            <p className="mt-1 text-sm text-muted">{i.desc}</p>
          </div>
          <div className="mt-auto flex items-center justify-between border-t border-line pt-3 text-xs text-muted">
            <span className="truncate">{i.status}</span>
            <span className="inline-flex items-center gap-1 font-medium text-fg">{i.on ? "Manage" : "Set up"} <ChevronRight size={14} className="transition group-hover:translate-x-0.5" /></span>
          </div>
        </button>
      ))}
    </div>
  );
}

/** One card per service, with how it stands; opening one shows its settings. */
function IntegrationCards({ github, cf, open }: { github: GitHubStatus; cf?: { clientId: string }; open: (tab: string) => void }) {
  const dns = useApi<{ everyone?: unknown[] }>("/api/me/dns");
  const aiSt = useApi<{ status: { configured: boolean; usedSeconds: number; config: { model: string; monthlyMinutes: number } } }>("/api/integrations/ai");
  const wa = useApi<{ status: { linked: boolean; number?: string; paused?: boolean }; people: { phone: string }[] }>("/api/integrations/whatsapp");
  const { toast } = useStore();
  const pause = async (paused: boolean) => {
    try {
      await api("PUT", "/api/integrations/whatsapp/paused", { paused });
      toast(paused ? "WhatsApp paused: nothing is sent, called or answered" : "WhatsApp is on again");
      wa.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const people = dns.data?.everyone?.length ?? 0;
  const chans = useApi<Channel[]>("/api/notifications/channels");
  const ch = (p: string) => chans.data?.find((c) => c.provider === p);
  const senders = useSenders().data ?? [];
  const tokens = useApi<unknown[]>("/api/tokens");
  const pages = useApi<StatusSummary[]>("/api/status-pages").data ?? [];
  const vulns = useApi<VulnSummary>("/api/integrations/vulns");
  const registries = useApi<unknown[]>("/api/integrations/docker-registries");
  const alert = (p: "telegram" | "discord" | "webhook", logo: ReactNode, desc: string) => {
    const c = ch(p);
    return { key: p, logo, name: CHANNEL_FIELDS[p].title, on: !!c?.enabled && !!c.configured, desc,
      status: c?.configured ? (c.enabled ? "Sending alerts" : "Set up, turned off") : "Not set up" };
  };
  const items = [
    { key: "github", logo: <Github size={28} />, name: "GitHub", on: github.connected,
      desc: "Deploy sites from your repositories, again on every push, and see which apps run which repository.",
      status: github.connected ? `${github.app?.name ?? "App"} · ${github.installations.length} account${github.installations.length === 1 ? "" : "s"}` : "One click creates the GitHub app" },
    ...(cf ? [{ key: "cloudflare", logo: <Cloudflare size={34} />, name: "Cloudflare", on: !!cf.clientId,
      desc: "Sign in with Cloudflare: site domains point at their server by themselves, the cache clears after deploys, proxy and Under attack switches.",
      status: cf.clientId ? `${people} account${people === 1 ? "" : "s"} signed in` : "Set up once, then everyone signs in" }] : []),
    { key: "whatsapp", logo: <WhatsApp size={32} />, name: "WhatsApp", on: !!wa.data?.status.linked,
      desc: "Link your own WhatsApp: on-call alerts reach the team there, each in their language and outside their quiet hours.",
      status: !wa.data?.status.linked ? "Scan a QR code once" : wa.data.status.paused ? "Paused: no alerts, calls or answers" : `+${wa.data.status.number} · ${wa.data.people.filter((p) => p.phone).length} people reached`,
      toggle: wa.data?.status.linked ? { checked: !wa.data.status.paused, onChange: (v: boolean) => pause(!v), label: "WhatsApp on" } : undefined },
    { key: "ai", logo: <Gemini size={30} />, name: "AI", on: !!aiSt.data?.status.configured,
      desc: "Gemini Live answers on-call calls: it explains the incident, answers questions from the dashboard's data, and acknowledges it for you.",
      status: aiSt.data?.status.configured ? `${aiSt.data.status.config.model} · ${Math.round(aiSt.data.status.usedSeconds / 60)} of ${aiSt.data.status.config.monthlyMinutes} minutes this month` : "Paste a Gemini API key once" },
    { key: "phone", logo: <span className="grid h-10 w-10 place-items-center rounded-xl bg-accent text-accent-fg"><Activity size={22} /></span>, name: "Phone app", on: false,
      desc: "The Fleetwatch app on Android: alerts that ring like a call, one tap to acknowledge, your servers at a glance.",
      status: "Pair a phone with a QR code" },
    { key: "email", logo: <span className="grid h-10 w-10 place-items-center rounded-xl bg-[#EA4335]/10 text-[#EA4335]"><Mail size={22} /></span>, name: "Email", on: senders.length > 0,
      desc: "Your email senders (Gmail, Brevo, Resend, any SMTP): status page subscriptions, updates and reports, and on-call emails. Each status page picks its own.",
      status: senders.length ? `${senders.length} sender${senders.length === 1 ? "" : "s"} · default ${senders.find((x) => x.default)?.from ?? ""}` : "Add an address to send from" },
    { key: "api", logo: <span className="grid h-10 w-10 place-items-center rounded-xl bg-surface2 font-mono text-sm font-bold">{"{}"}</span>, name: "Fleetwatch API", on: (tokens.data?.length ?? 0) > 0,
      desc: "Tokens for your own scripts and tools (Grafana, Prometheus, n8n…), with example calls.",
      status: tokens.data?.length ? `${tokens.data.length} token${tokens.data.length === 1 ? "" : "s"}` : "No token yet" },
    { key: "widget", logo: <span className="grid h-10 w-10 place-items-center rounded-xl bg-[#16a34a]/10 text-[#16a34a]"><Globe size={22} /></span>, name: "Website widget", on: pages.some((p) => p.enabled),
      desc: "A status pill and incident notices on your own websites, and the same news in Crisp or Intercom, for each status page.",
      status: pages.length ? `${pages.length} status page${pages.length === 1 ? "" : "s"}` : "Make a status page first" },
    { key: "vulns", logo: <span className="grid h-10 w-10 place-items-center rounded-xl bg-[#dc2626]/10 text-[#dc2626]"><ShieldAlert size={22} /></span>, name: "Vulnerability database", on: !!vulns.data?.enabled,
      desc: "Your apps' packages checked every day against OSV.dev (npm, PyPI, Go, crates, Packagist…); serious findings alert you.",
      status: vulns.data ? (vulns.data.enabled ? `${vulns.data.apps} apps · ${vulns.data.deps} packages` : "Turned off") : "" },
    { key: "registries", logo: <span className="grid h-10 w-10 place-items-center rounded-xl bg-[#1D63ED]/10 text-[#1D63ED]"><Container size={22} /></span>, name: "Docker registries", on: (registries.data?.length ?? 0) > 0,
      desc: "Logins for private images on GHCR, Docker Hub, GitLab or any registry: Docker apps and updates pull images with them.",
      status: registries.data?.length ? `${registries.data.length} login${registries.data.length === 1 ? "" : "s"}` : "Public images need none" },
    alert("telegram", <Telegram size={30} />, "Alerts in a Telegram chat or group, through your own bot."),
    alert("discord", <Discord size={30} />, "Alerts in a Discord channel, through a channel webhook."),
    alert("webhook", <Webhook size={28} color="#C73A63" strokeWidth={2.2} />, "Alerts as JSON to your own URL: n8n, Zapier, Slack workflows, Home Assistant, your code."),
  ];
  return (
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
      {items.map((i: (typeof items)[number] & { toggle?: { checked: boolean; onChange: (v: boolean) => void; label: string } }) => (
        <div key={i.key} className="relative flex">
        <button onClick={() => open(i.key)} className="group flex flex-1 flex-col gap-4 rounded-3xl border border-line bg-surface p-5 text-left transition hover:-translate-y-0.5 hover:border-fg/20 hover:shadow-lg">
          <div className="flex items-start justify-between gap-3">
            <span className="grid h-14 w-14 place-items-center rounded-2xl bg-surface2">{i.logo}</span>
            {!i.toggle && <Badge tone={i.on ? "ok" : undefined}>{i.on ? "Connected" : "Not connected"}</Badge>}
          </div>
          <div>
            <div className="text-base font-semibold">{i.name}</div>
            <p className="mt-1 text-sm text-muted">{i.desc}</p>
          </div>
          <div className="mt-auto flex items-center justify-between border-t border-line pt-3 text-xs text-muted">
            <span className="truncate">{i.status}</span>
            <span className="inline-flex items-center gap-1 font-medium text-fg">{i.on ? "Manage" : "Set up"} <ChevronRight size={14} className="transition group-hover:translate-x-0.5" /></span>
          </div>
        </button>
        {i.toggle && ( // beside the card's button, not in it: a switch inside a button is not allowed
          <label className="absolute right-5 top-5 flex items-center gap-2 text-xs font-medium text-muted">
            {i.toggle.checked ? "On" : "Paused"}
            <span aria-label={i.toggle.label}><Toggle checked={i.toggle.checked} onChange={i.toggle.onChange} /></span>
          </label>
        )}
        </div>
      ))}
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
  const [allPushes, setAllPushes] = useState(false);
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
            {status.pushes.slice(0, allPushes ? undefined : 6).map((p, i) => (
              <li key={i} className="flex flex-wrap items-baseline gap-2 py-2">
                <span className="font-mono text-xs">{p.repo}</span>
                <Badge><GitBranch size={11} /> {p.branch}</Badge>
                <span className="font-mono text-xs text-muted">{p.commit.slice(0, 7)}</span>
                <span className="min-w-0 flex-1 truncate">{p.message}</span>
                <span className="text-xs text-muted">{p.pusher} · {relTime(p.ts)}</span>
              </li>
            ))}
          </ul>
          {status.pushes.length > 6 && (
            <Button size="sm" variant="ghost" className="mt-2" onClick={() => setAllPushes(!allPushes)}>{allPushes ? "Show fewer" : `Show all ${status.pushes.length}`}</Button>
          )}
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
  const [q, setQ] = useState("");
  const [all, setAll] = useState(false);
  if (list.error) return <ErrorState message={list.error} onRetry={list.reload} />;
  if (!list.data) return <Card><LoadingState label="Asking GitHub for the repositories…" /></Card>;
  const term = q.trim().toLowerCase();
  const sorted = [...list.data]
    .filter(({ repo }) => !term || repo.fullName.toLowerCase().includes(term))
    .sort((a, b) => b.apps.length - a.apps.length || Date.parse(b.repo.pushedAt || "0") - Date.parse(a.repo.pushedAt || "0"));
  const shown = all || term ? sorted : sorted.slice(0, 8);
  return (
    <Card pad={false}>
      <div className="p-5 pb-2">
        <SectionTitle icon={<FolderGit2 size={15} />} title={`Repositories (${list.data.length})`} sub="The ones linked to your apps first (by the git remote of their folder), then the latest pushed." action={<Button size="sm" variant="ghost" onClick={list.reload}><RefreshCw size={14} /></Button>} />
        {list.data.length > 8 && <SearchInput value={q} onChange={setQ} placeholder="Search repositories…" className="mb-2 sm:w-72" />}
      </div>
      {list.data.length === 0 ? (
        <div className="p-5 pt-0"><EmptyState title="No repositories" desc="Choose repositories on GitHub for the app." /></div>
      ) : (
        <Table head={["Repository", "Default branch", "Last push", "Apps here"]}>
          {shown.map(({ repo, apps }) => (
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
      {!term && sorted.length > 8 && (
        <div className="p-3 pt-1">
          <Button size="sm" variant="ghost" onClick={() => setAll(!all)}>{all ? "Show fewer" : `Show all ${sorted.length}`}</Button>
        </div>
      )}
    </Card>
  );
}

const ALERT_TABS = ["telegram", "discord", "webhook", "phone", "email", "api", "widget", "vulns", "registries"];

/** One place alerts can go: its settings, opened from its card. */
function AlertChannel({ tab }: { tab: string }) {
  const chans = useApi<Channel[]>("/api/notifications/channels");
  if (tab === "phone") return <MobileCard />;
  if (tab === "email") return <EmailSenders />;
  if (tab === "api") return <TokenSettings />;
  if (tab === "widget") return <Widgets />;
  if (tab === "vulns") return <VulnDatabase />;
  if (tab === "registries") return <DockerRegistries />;
  if (chans.error) return <ErrorState message={chans.error} onRetry={chans.reload} />;
  const ch = chans.data?.find((c) => c.provider === tab);
  if (!ch) return <LoadingState />;
  return (
    <>
      <ChannelCard key={JSON.stringify(ch)} ch={ch} onSaved={chans.setData} />
      {tab === "webhook" && <StatusWebhooks />}
      <p className="text-xs text-muted">Which events send alerts: <Link to="/settings/notifications" className="underline">Settings → Notifications</Link>.</p>
    </>
  );
}

interface StatusSummary {
  id: string;
  slug: string;
  title: string;
  enabled: boolean;
  domain: string;
  access: string;
}

/** Website widget: each status page's code, in one place. */
function Widgets() {
  const pages = useApi<StatusSummary[]>("/api/status-pages");
  if (pages.error) return <ErrorState message={pages.error} onRetry={pages.reload} />;
  if (!pages.data) return <LoadingState />;
  if (pages.data.length === 0) return <EmptyState title="No status page yet" desc="Make one in Status pages, then put its widget on your websites." action={<Link to="/status-page"><Button>Status pages</Button></Link>} />;
  return (
    <div className="space-y-6">
      {pages.data.map((p) => (
        <WidgetCard key={p.id} c={p} title={p.title} savedPublic={p.enabled && (p.access === "" || p.access === "public")} />
      ))}
    </div>
  );
}

/** Status pages' webhook subscribers: the other outgoing webhooks. */
function StatusWebhooks() {
  const pages = useApi<StatusSummary[]>("/api/status-pages");
  const [subs, setSubs] = useState<{ page: string; id: string; target: string; confirmed: boolean; lastError: string }[] | null>(null);
  useEffect(() => {
    if (!pages.data) return;
    Promise.all(pages.data.map((p) => api<{ id: string; kind: string; target: string; confirmed: boolean; lastError: string }[]>("GET", `/api/status-pages/${p.id}/subscribers`)
      .then((l) => l.filter((x) => x.kind === "webhook").map((x) => ({ ...x, page: p.title }))).catch(() => [])))
      .then((all) => setSubs(all.flat()));
  }, [pages.data]);
  return (
    <Card>
      <SectionTitle title="Status page webhooks" sub="Systems that subscribed to your status pages by webhook: they get a JSON POST on each update. Managed on each page (Subscribers)." />
      {!subs ? <Spinner /> : subs.length === 0 ? <p className="text-sm text-muted">None.</p> : (
        <ul className="space-y-2 text-sm">
          {subs.map((x) => (
            <li key={x.page + x.id} className="flex flex-wrap items-center gap-2 rounded-2xl border border-line px-3 py-2">
              <span className="min-w-0 flex-1 truncate font-mono text-xs">{x.target}</span>
              <Badge>{x.page}</Badge>
              {x.lastError ? <Badge tone="crit">{x.lastError}</Badge> : <Badge tone={x.confirmed ? "ok" : "warn"}>{x.confirmed ? "active" : "not confirmed"}</Badge>}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

interface VulnSummary {
  enabled: boolean;
  source: string;
  lastScan: number;
  apps: number;
  deps: number;
  found: Record<string, number>;
}

/** The vulnerability database: what is checked, when, and the switch. */
function VulnDatabase() {
  const { toast } = useStore();
  const v = useApi<VulnSummary>("/api/integrations/vulns");
  const [busy, setBusy] = useState(false);
  if (v.error) return <ErrorState message={v.error} onRetry={v.reload} />;
  if (!v.data) return <LoadingState />;
  const d = v.data;
  const run = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true);
    try {
      await fn();
      toast(done);
      v.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <Card>
      <SectionTitle title="Vulnerability database" sub={`Every day, your apps' packages (from their lock files) are checked against ${d.source.replace("https://api.", "")}, the open database of known vulnerabilities for npm, PyPI, Go, crates.io, Packagist, RubyGems and more. New critical or high findings alert you.`}
        action={<Toggle checked={d.enabled} disabled={busy} onChange={(on) => run(() => api("PUT", "/api/integrations/vulns", { enabled: on }), on ? "Daily checks on" : "Daily checks off")} />} />
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        {[["Last check", d.lastScan ? relTime(d.lastScan) : "never"], ["Apps", String(d.apps)], ["Packages", String(d.deps)], ["Found", String(Object.values(d.found).reduce((a, b) => a + b, 0))]].map(([k, val]) => (
          <div key={k} className="rounded-2xl bg-surface2/60 px-3 py-2.5"><div className="text-[11px] text-muted">{k}</div><div className="text-lg font-semibold">{val}</div></div>
        ))}
      </div>
      {Object.keys(d.found).length > 0 && (
        <div className="mt-3 flex flex-wrap gap-2">{["critical", "high", "moderate", "low", "unknown"].filter((k) => d.found[k]).map((k) => <Badge key={k} tone={k === "critical" ? "crit" : k === "high" ? "warn" : undefined}>{d.found[k]} {k}</Badge>)}</div>
      )}
      <div className="mt-4 flex flex-wrap gap-2">
        <Button size="sm" variant="secondary" disabled={busy} onClick={() => run(() => api("POST", "/api/vulns/scan"), "Checking all apps now")}>{busy ? <Spinner size={13} /> : <RefreshCw size={13} />} Check now</Button>
        <Link to="/security"><Button size="sm" variant="ghost">See the findings</Button></Link>
      </div>
    </Card>
  );
}

/** "Continue with GitHub" on the sign-in page: the owner's switch. */
function GitHubSignIn() {
  const { toast } = useStore();
  const sec = useApi<Record<string, unknown> & { noGitHubLogin?: boolean }>("/api/settings/security");
  if (!sec.data) return null;
  const set = async (on: boolean) => {
    try {
      sec.setData(await api("PUT", "/api/settings/security", { ...sec.data, noGitHubLogin: !on }));
      toast(on ? "Signing in with GitHub is on" : "Signing in with GitHub is off");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  return (
    <Card>
      <SectionTitle icon={<Github size={15} />} title="Sign in with GitHub" sub="The sign-in page offers “Continue with GitHub” to everyone who linked their GitHub account. Turned off, only passwords work."
        action={<Toggle checked={!sec.data.noGitHubLogin} onChange={set} />} />
    </Card>
  );
}

const MY_CHANNELS = ["telegram", "discord", "webhook", "email"];
const MY_LOGO: Record<string, ReactNode> = {
  telegram: <Telegram size={30} />, discord: <Discord size={30} />,
  webhook: <Webhook size={28} color="#C73A63" strokeWidth={2.2} />,
  email: <span className="grid h-10 w-10 place-items-center rounded-xl bg-[#EA4335]/10 text-[#EA4335]"><Mail size={22} /></span>,
};
const MY_DESC: Record<string, string> = {
  telegram: "Your alerts in your own Telegram chat, through your own bot.",
  discord: "Your alerts in a Discord channel of yours, through its webhook.",
  webhook: "Your alerts as JSON to your own URL (n8n, Zapier, Home Assistant…).",
  email: "Your alerts to your own email address.",
};

/** A team member's own channel: their alerts, for the servers they see, by their rules. */
function MyChannel({ provider }: { provider: string }) {
  const list = useApi<Channel[]>("/api/me/channels");
  const ch = list.data?.find((c) => c.provider === provider);
  if (list.error) return <ErrorState message={list.error} onRetry={list.reload} />;
  if (!ch) return <LoadingState />;
  return (
    <div className="space-y-3">
      <ChannelCard key={JSON.stringify(ch)} ch={ch} base="/api/me/channels" onSaved={list.setData} />
      <p className="text-xs text-muted">You get the alerts of the servers you can see, filtered by <Link to="/settings/account" className="underline">My phone alerts</Link> (severity, servers, quiet hours).</p>
    </div>
  );
}
