import { useState } from "react";
import { AlertTriangle, Cloud, Globe, Plus, RefreshCw, ShieldAlert, ShieldCheck, Trash2, Users } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, Field, Input, SectionTitle, Select, Spinner, Toggle } from "./ui";

interface Zone {
  id: string;
  name: string;
}
export interface DnsAccount {
  id: string;
  owner: string;
  provider: "cloudflare" | "porkbun" | "desec";
  label: string;
  zones: Zone[];
  syncBlocks?: boolean;
  createdAt: number;
  error?: string;
}

const PROVIDERS = {
  cloudflare: {
    name: "Cloudflare",
    icon: Cloud,
    fields: [["token", "API token"]],
    help: (
      <>Create a token at <a className="underline" href="https://dash.cloudflare.com/profile/api-tokens" target="_blank" rel="noreferrer">dash.cloudflare.com → API tokens</a> with <b>Zone: Read</b>, <b>DNS: Edit</b>, <b>Cache Purge: Purge</b>, <b>Zone Settings: Edit</b> and <b>Firewall Services: Edit</b>, for the zones you want.</>
    ),
  },
  porkbun: {
    name: "Porkbun",
    icon: Globe,
    fields: [["apiKey", "API key"], ["secretKey", "Secret API key"]],
    help: (
      <>Create the keys at <a className="underline" href="https://porkbun.com/account/api" target="_blank" rel="noreferrer">porkbun.com → API access</a>, then turn on <b>API access</b> for each domain in its Details.</>
    ),
  },
  desec: {
    name: "deSEC",
    icon: ShieldCheck,
    fields: [["token", "Token"]],
    help: <>Create a token at <a className="underline" href="https://desec.io/tokens" target="_blank" rel="noreferrer">desec.io → Token management</a>.</>,
  },
} as const;

/** One person's DNS and CDN accounts: connect them, see their domains, Cloudflare's switches. */
export function DnsAccounts() {
  const { me, toast } = useStore();
  const data = useApi<{ accounts: DnsAccount[]; everyone?: DnsAccount[] }>("/api/me/dns");
  const members = useApi<{ id: string; username: string; displayName: string }[]>(!me || me.role === "owner" ? "/api/members" : "/api/me/dns");
  const [adding, setAdding] = useState(false);
  const [provider, setProvider] = useState<keyof typeof PROVIDERS>("cloudflare");
  const [creds, setCreds] = useState<Record<string, string>>({});
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
  const add = () => run("add", async () => {
    await api("POST", "/api/me/dns", { provider, creds });
    setAdding(false);
    setCreds({});
    data.reload();
    toast(`${PROVIDERS[provider].name} connected: new site domains point at their server by themselves`);
  });
  const mine = data.data?.accounts ?? [];
  const team = (data.data?.everyone ?? []).filter((a) => a.owner !== "owner");
  const who = (key: string) => (Array.isArray(members.data) ? members.data.find((m) => m.id === key) : undefined);
  return (
    <Card>
      <SectionTitle icon={<Cloud size={15} />} title="DNS and CDN accounts"
        sub="Connect your own Cloudflare, Porkbun or deSEC account: the domains you add to your sites then point at their server by themselves, and are removed with them. Only you can use your accounts."
        action={canConnect && !adding && <Button size="sm" onClick={() => setAdding(true)}><Plus size={13} /> Connect</Button>} />
      {adding && (
        <div className="mb-4 space-y-3 rounded-2xl border border-line p-4">
          <Field label="Provider">
            <Select value={provider} onChange={(e) => { setProvider(e.target.value as keyof typeof PROVIDERS); setCreds({}); }}>
              {Object.entries(PROVIDERS).map(([k, p]) => <option key={k} value={k}>{p.name}</option>)}
            </Select>
          </Field>
          <p className="text-xs text-muted">{PROVIDERS[provider].help}</p>
          {PROVIDERS[provider].fields.map(([k, label]) => (
            <Field key={k} label={label}>
              <Input type="password" autoComplete="off" value={creds[k] ?? ""} onChange={(e) => setCreds({ ...creds, [k]: e.target.value })} className="font-mono" />
            </Field>
          ))}
          <p className="text-[11px] text-muted">The keys are checked by listing your domains, then stored encrypted. They are never shown again.</p>
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => { setAdding(false); setCreds({}); }}>Cancel</Button>
            <Button size="sm" disabled={busy === "add" || PROVIDERS[provider].fields.some(([k]) => !creds[k])} onClick={add}>{busy === "add" && <Spinner size={13} />} Connect {PROVIDERS[provider].name}</Button>
          </div>
        </div>
      )}
      {!data.data ? <Spinner /> : mine.length === 0 ? (
        <p className="text-sm text-muted">{canConnect ? "No account connected yet." : "Viewers cannot connect accounts."}</p>
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
              const Icon = PROVIDERS[a.provider].icon;
              const m = who(a.owner);
              return (
                <li key={a.id} className="flex flex-wrap items-center gap-2">
                  <Icon size={13} className="text-muted" />
                  <span className="font-medium">{a.label}</span>
                  <span className="text-muted">· {m?.displayName || m?.username || "a removed member"} · {a.zones.length} domain{a.zones.length === 1 ? "" : "s"}</span>
                </li>
              );
            })}
          </ul>
        </div>
      )}
      <ConfirmDialog open={!!del} title={`Disconnect ${del?.label}?`} confirmLabel="Disconnect" danger
        message="Its keys are deleted from the dashboard. Records it made stay at the provider; new site domains are no longer pointed through it."
        onCancel={() => setDel(null)} onConfirm={() => { const a = del!; setDel(null); run("del", async () => { await api("DELETE", `/api/me/dns/${a.id}`); data.reload(); toast("Disconnected"); }); }} />
    </Card>
  );
}

function AccountRow({ a, busy, run, onRemove, onChange }: { a: DnsAccount; busy: string; run: (k: string, fn: () => Promise<void>) => void; onRemove: () => void; onChange: () => void }) {
  const p = PROVIDERS[a.provider];
  const [open, setOpen] = useState(false);
  return (
    <li className="rounded-2xl border border-line p-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-surface2"><p.icon size={16} /></span>
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
          {a.zones.length === 0 && <li className="text-xs text-muted">No domains in this account (or the keys cannot see them).</li>}
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

export const providerName = (p: string) => (p in PROVIDERS ? PROVIDERS[p as keyof typeof PROVIDERS].name : p);
