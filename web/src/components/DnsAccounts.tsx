import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { AlertTriangle, Globe, RefreshCw, ShieldAlert, Trash2, Users } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { Badge, Button, Card, Cloudflare, ConfirmDialog, CopyButton, Field, Input, SectionTitle, Spinner, Toggle } from "./ui";

interface Zone {
  id: string;
  name: string;
}
export interface DnsAccount {
  id: string;
  owner: string;
  provider: "cloudflare";
  label: string;
  zones: Zone[];
  syncBlocks?: boolean;
  createdAt: number;
  error?: string;
}

/** One person's Cloudflare accounts: sign in, see their domains and Cloudflare's switches. */
export function DnsAccounts() {
  const { me, toast } = useStore();
  const data = useApi<{ accounts: DnsAccount[]; everyone?: DnsAccount[]; ready: boolean }>("/api/me/dns");
  const members = useApi<{ id: string; username: string; displayName: string }[]>(!me || me.role === "owner" ? "/api/members" : "/api/me/dns");
  const [params, setParams] = useSearchParams();
  useEffect(() => {
    const cf = params.get("cloudflare");
    if (!cf) return;
    if (cf === "connected") toast("Cloudflare is connected: new site domains point at their server by themselves");
    else toast(params.get("message") || "Cloudflare could not be connected", "err");
    params.delete("cloudflare");
    params.delete("message");
    setParams(params, { replace: true });
  }, [params, setParams, toast]);
  const [busy, setBusy] = useState("");
  const [del, setDel] = useState<DnsAccount | null>(null);
  const canConnect = !me || me.role !== "viewer";
  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try {
      await fn();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const signIn = () => run("add", async () => {
    window.location.href = (await api<{ url: string }>("POST", "/api/me/dns/connect")).url;
    await new Promise(() => {}); // keep the spinner until the page goes
  });
  const mine = data.data?.accounts ?? [];
  const team = (data.data?.everyone ?? []).filter((a) => a.owner !== "owner");
  const who = (key: string) => (Array.isArray(members.data) ? members.data.find((m) => m.id === key) : undefined);
  return (
    <Card>
      <SectionTitle icon={<Cloudflare size={17} />} title="Your Cloudflare"
        sub="Sign in with your Cloudflare account: the domains you add to your sites then point at their server by themselves, the cache clears after each deploy, and you get the proxy and Under attack switches. Only you can use your account."
        action={canConnect && data.data?.ready && (
          <Button size="sm" disabled={busy === "add"} onClick={signIn}>{busy === "add" ? <Spinner size={13} /> : <Cloudflare size={14} mono />} {mine.length ? "Sign in again" : "Sign in with Cloudflare"}</Button>
        )} />
      {data.data && !data.data.ready && (
        <p className="mb-3 flex gap-2 rounded-xl bg-surface2/60 p-3 text-xs text-muted"><AlertTriangle size={13} className="shrink-0" />
          {me?.role === "owner" ? "Set up Cloudflare above first (once), then sign in here." : "The dashboard owner has not set up Cloudflare yet (Integrations)."}</p>
      )}
      {!data.data ? <Spinner /> : mine.length === 0 ? (
        <p className="text-sm text-muted">{canConnect ? "No Cloudflare account connected yet." : "Viewers cannot connect accounts."}</p>
      ) : (
        <ul className="space-y-3">
          {mine.map((a) => <AccountRow key={a.id} a={a} busy={busy} run={run} onRemove={() => setDel(a)} onChange={data.reload} />)}
        </ul>
      )}
      {team.length > 0 && (
        <div className="mt-5 border-t border-line pt-4">
          <div className="mb-2 flex items-center gap-2 text-sm font-medium"><Users size={14} /> The team's accounts</div>
          <ul className="space-y-1.5 text-sm">
            {team.map((a) => {
              const m = who(a.owner);
              return (
                <li key={a.id} className="flex flex-wrap items-center gap-2">
                  <Cloudflare size={14} />
                  <span className="font-medium">{a.label}</span>
                  <span className="text-muted">· {m?.displayName || m?.username || "a removed member"} · {a.zones.length} domain{a.zones.length === 1 ? "" : "s"}</span>
                </li>
              );
            })}
          </ul>
        </div>
      )}
      <ConfirmDialog open={!!del} title={`Disconnect ${del?.label}?`} confirmLabel="Disconnect" danger
        message="The dashboard signs out of it on Cloudflare. Records it made stay there; new site domains are no longer pointed through it."
        onCancel={() => setDel(null)} onConfirm={() => { const a = del!; setDel(null); run("del", async () => { await api("DELETE", `/api/me/dns/${a.id}`); data.reload(); toast("Disconnected"); }); }} />
    </Card>
  );
}

function AccountRow({ a, busy, run, onRemove, onChange }: { a: DnsAccount; busy: string; run: (k: string, fn: () => Promise<void>) => void; onRemove: () => void; onChange: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <li className="rounded-2xl border border-line p-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-surface2"><Cloudflare size={20} /></span>
        <button className="min-w-0 flex-1 text-left" onClick={() => setOpen(!open)}>
          <div className="text-sm font-medium">{a.label}</div>
          <div className="text-xs text-muted">{a.zones.length} domain{a.zones.length === 1 ? "" : "s"} · connected {relTime(a.createdAt)} · {open ? "hide" : "show"} domains</div>
        </button>
        <button className="grid h-8 w-8 place-items-center rounded-full text-muted hover:bg-surface2 hover:text-fg" title="Reload the domains" aria-label="Reload the domains"
          onClick={() => run("ref" + a.id, async () => { await api("POST", `/api/me/dns/${a.id}/refresh`); onChange(); })}>
          {busy === "ref" + a.id ? <Spinner size={13} /> : <RefreshCw size={14} />}
        </button>
        <button className="grid h-8 w-8 place-items-center rounded-full text-muted hover:bg-crit-soft hover:text-crit" title="Disconnect" aria-label="Disconnect" onClick={onRemove}><Trash2 size={14} /></button>
      </div>
      {a.error && <p className="mt-2 flex gap-2 text-xs text-crit"><AlertTriangle size={13} className="shrink-0" />{a.error}</p>}
      {a.provider === "cloudflare" && (
        <label className="mt-3 flex items-center justify-between gap-3 text-xs">
          <span><b className="text-fg">Block the dashboard's blocked addresses on Cloudflare too</b><br /><span className="text-muted">The IPs Security → Blocked IPs lists are refused before they reach your servers, on every domain of this account.</span></span>
          <Toggle checked={!!a.syncBlocks} onChange={(on) => run("sync" + a.id, async () => { await api("PUT", `/api/me/dns/${a.id}/sync-blocks`, { on }); onChange(); })} />
        </label>
      )}
      {open && (
        <ul className="mt-3 space-y-1 border-t border-line pt-2">
          {a.zones.length === 0 && <li className="text-xs text-muted">No domains in this account (or you did not let the dashboard see them).</li>}
          {a.zones.map((z) => <ZoneRow key={z.id} a={a} z={z} />)}
        </ul>
      )}
    </li>
  );
}

/** A domain of an account; for Cloudflare, its "Under attack" switch. */
function ZoneRow({ a, z }: { a: DnsAccount; z: Zone }) {
  const { toast } = useStore();
  const attack = useApi<{ on: boolean }>(a.provider === "cloudflare" ? `/api/me/dns/${a.id}/zones/${z.name}/attack` : "/api/me/dns");
  const [busy, setBusy] = useState(false);
  const on = a.provider === "cloudflare" && attack.data && "on" in attack.data ? attack.data.on : undefined;
  const set = async (v: boolean) => {
    setBusy(true);
    try {
      attack.setData(await api<{ on: boolean }>("PUT", `/api/me/dns/${a.id}/zones/${z.name}/attack`, { on: v }));
      toast(v ? `Under attack mode is on for ${z.name}: visitors get a short browser check first` : `Under attack mode is off for ${z.name}`);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <li className="flex flex-wrap items-center gap-2 py-1 text-sm">
      <Globe size={13} className="text-muted" />
      <span className="min-w-0 flex-1 truncate font-mono text-[12px]">{z.name}</span>
      {on !== undefined && (
        <span className="flex items-center gap-2 text-xs">
          {on ? <Badge tone="crit"><ShieldAlert size={11} /> Under attack</Badge> : <span className="text-muted">Under attack mode</span>}
          {busy ? <Spinner size={13} /> : <Toggle checked={on} onChange={set} />}
        </span>
      )}
    </li>
  );
}

export const providerName = (p: string) => (p === "cloudflare" ? "Cloudflare" : p);

/** The owner's one-time setup: the OAuth client made in their Cloudflare account. */
export function CloudflareSetup({ clientId, redirectUri, scopes, onChange }: { clientId: string; redirectUri: string; scopes: string[]; onChange: () => void }) {
  const { toast } = useStore();
  const [editing, setEditing] = useState(!clientId);
  const [id, setId] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [del, setDel] = useState(false);
  const save = async () => {
    setBusy(true);
    try {
      await api("PUT", "/api/integrations/cloudflare", { clientId: id, clientSecret: secret });
      setEditing(false);
      setId("");
      setSecret("");
      onChange();
      toast("Cloudflare is set up: sign in with it below");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const perms = ["Zone Read", "DNS Write", "Cache Purge", "Zone Settings Write", "Firewall Services Write"];
  return (
    <Card>
      <SectionTitle icon={<Cloudflare size={17} />} title="Sign in with Cloudflare"
        sub="Once: create a sign-in app in your Cloudflare account, so you and your team can sign in with Cloudflare instead of pasting API keys."
        action={clientId ? <Badge tone="ok">Set up</Badge> : undefined} />
      {clientId && !editing ? (
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <span className="text-muted">Client ID</span>
          <code className="rounded-lg bg-surface2 px-2 py-1 font-mono text-xs">{clientId}</code>
          <span className="flex-1" />
          <Button size="sm" variant="ghost" onClick={() => setEditing(true)}>Change</Button>
          <Button size="sm" variant="ghost" className="text-crit" onClick={() => setDel(true)}><Trash2 size={13} /> Remove</Button>
        </div>
      ) : (
        <div className="space-y-4">
          <ol className="list-decimal space-y-2.5 pl-5 text-sm text-muted">
            <li>Open <a className="font-medium text-fg underline" href="https://dash.cloudflare.com/?to=/:account/oauth-clients" target="_blank" rel="noreferrer">Cloudflare → Manage account → OAuth clients</a> and select <b className="text-fg">Create client</b>.</li>
            <li>
              Fill in the form:
              <ul className="mt-1.5 space-y-1 text-xs">
                <li><b className="text-fg">Client Name</b>: Fleetwatch</li>
                <li><b className="text-fg">Response Type</b>: code</li>
                <li><b className="text-fg">Grant Type</b>: Authorization Code <i>and</i> Refresh Token</li>
                <li><b className="text-fg">Token Authentication Method</b>: Client Secret Basic</li>
                <li>
                  <b className="text-fg">Redirect (Callback) URLs</b>:
                  <span className="mt-1 flex items-center gap-2">
                    <code className="min-w-0 flex-1 truncate rounded-lg bg-surface2 px-2 py-1 font-mono text-xs text-fg">{redirectUri}</code>
                    <CopyButton text={redirectUri} />
                  </span>
                </li>
              </ul>
            </li>
            <li>
              Select <b className="text-fg">Continue</b> and pick these scopes (leave them Required): {perms.map((p) => <Badge key={p} className="mr-1 mt-1">{p}</Badge>)}
              <span className="mt-1 block text-[11px]">If an <b>offline access</b> scope is listed, pick it too. The sign-in asks for: <code className="font-mono">{scopes.join(" ")}</code></span>
            </li>
            <li>Select <b className="text-fg">Create client</b>, then paste the <b className="text-fg">Client ID</b> and <b className="text-fg">Client Secret</b> here (Cloudflare shows the secret only once).</li>
            <li>Teammates who are not members of your Cloudflare account can only sign in once the client is <b className="text-fg">Public</b>: that needs a logo, a Client URL and a TXT record on your domain.</li>
          </ol>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label="Client ID"><Input value={id} onChange={(e) => setId(e.target.value)} className="font-mono" autoComplete="off" /></Field>
            <Field label="Client secret"><Input type="password" value={secret} onChange={(e) => setSecret(e.target.value)} className="font-mono" autoComplete="off" /></Field>
          </div>
          <div className="flex justify-end gap-2">
            {clientId && <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>Cancel</Button>}
            <Button size="sm" disabled={busy || !id.trim() || !secret.trim()} onClick={save}>{busy && <Spinner size={13} />} Save</Button>
          </div>
        </div>
      )}
      <ConfirmDialog open={del} title="Remove the Cloudflare sign-in app?" confirmLabel="Remove" danger
        message="Accounts already connected stop working until it is set up again."
        onCancel={() => setDel(false)} onConfirm={async () => { setDel(false); try { await api("DELETE", "/api/integrations/cloudflare"); onChange(); setEditing(true); } catch (e) { toast((e as ApiError).message, "err"); } }} />
    </Card>
  );
}
