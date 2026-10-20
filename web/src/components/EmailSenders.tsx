import { AlertTriangle, Check, Copy, Globe, Mail, Pencil, Plus, RefreshCw, Send, Star, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ConfirmDialog, Field, IconButton, Input, SectionTitle, Select, Spinner } from "./ui";

export interface Sender {
  id: string;
  label: string;
  default: boolean;
  host: string;
  port: number;
  security: "starttls" | "tls" | "none";
  username: string;
  password?: string;
  hasPassword: boolean;
  from: string;
  fromName: string;
  usedBy: string[];
  domainId?: string;
  direct?: boolean;
}

const PRESETS: { name: string; host: string; port: number; security: Sender["security"]; note: string }[] = [
  { name: "Gmail", host: "smtp.gmail.com", port: 587, security: "starttls", note: "Use an app password (Google account → Security → App passwords)." },
  { name: "Brevo", host: "smtp-relay.brevo.com", port: 587, security: "starttls", note: "Username and SMTP key from Brevo → SMTP & API." },
  { name: "Resend", host: "smtp.resend.com", port: 465, security: "tls", note: "Username “resend”, password: your API key." },
  { name: "Mailgun", host: "smtp.mailgun.org", port: 587, security: "starttls", note: "SMTP credentials from your Mailgun domain." },
  { name: "Outlook", host: "smtp.office365.com", port: 587, security: "starttls", note: "Your Microsoft 365 address and password (or an app password)." },
];

const EMPTY: Sender = { id: "", label: "", default: false, host: "", port: 587, security: "starttls", username: "", hasPassword: false, from: "", fromName: "", usedBy: [] };

/** Integrations → Email: the addresses emails go out from, any provider's SMTP. */
export function EmailSenders() {
  const { toast, me } = useStore();
  const list = useApi<Sender[]>("/api/integrations/email");
  const [edit, setEdit] = useState<Sender | null>(null);
  const [ownDomain, setOwnDomain] = useState(false);
  const [direct, setDirect] = useState(false);
  const [checking, setChecking] = useState<string | null>(null);
  const [del, setDel] = useState<Sender | null>(null);
  const [testTo, setTestTo] = useState(me?.email ?? "");
  const [busy, setBusy] = useState("");
  const run = async (what: string, fn: () => Promise<Sender[] | unknown>, done: string) => {
    setBusy(what);
    try {
      const out = await fn();
      if (Array.isArray(out)) list.setData(out as Sender[]);
      toast(done);
      return true;
    } catch (e) {
      toast((e as ApiError).message, "err");
      return false;
    } finally {
      setBusy("");
    }
  };
  const senders = list.data ?? [];
  return (
    <div className="space-y-6">
      <Card>
        <SectionTitle icon={<Mail size={15} />} title="Email senders" sub="The addresses emails go out from: status page updates, subscriptions and reports (each page picks its sender), and on-call emails (the default one). Passwords are stored encrypted."
          action={!edit && !ownDomain && !direct && (
            <div className="flex flex-wrap gap-2">
              <Button size="sm" onClick={() => setDirect(true)}><Globe size={14} /> Send it yourself (free)</Button>
              <Button size="sm" variant="secondary" onClick={() => setOwnDomain(true)}>Through Resend</Button>
              <Button size="sm" variant="secondary" onClick={() => setEdit({ ...EMPTY, default: senders.length === 0 })}><Plus size={14} /> Other SMTP</Button>
            </div>
          )} />
        {!list.data ? <Spinner /> : senders.length === 0 && !edit ? (
          <p className="text-sm text-muted">None yet. Add one to send status page emails and on-call emails.</p>
        ) : (
          <ul className="space-y-2">
            {senders.map((s) => (
              <li key={s.id} className="flex flex-wrap items-center gap-3 rounded-2xl border border-line px-3 py-2.5">
                <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-surface2"><Mail size={16} /></span>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 text-sm font-medium">{s.label} {s.default && <Badge tone="ok">default</Badge>}{s.domainId && <Badge tone="info">your domain · Resend</Badge>}{s.direct && <Badge tone="info">sends itself</Badge>}</div>
                  <div className="truncate text-xs text-muted">{s.fromName ? `${s.fromName} <${s.from}>` : s.from}{s.direct ? " · from this server" : ` · ${s.host}`}</div>
                  <div className="text-xs text-muted">{s.usedBy.length ? `Status pages: ${s.usedBy.join(", ")}` : s.default ? "On-call emails, and pages without their own sender" : "Not used by a status page yet"}</div>
                </div>
                {!s.default && <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => run("def", () => api("PUT", `/api/integrations/email/${s.id}`, { ...s, password: "", default: true }), `${s.label} is the default now`)}><Star size={13} /> Make default</Button>}
                <IconButton aria-label="Send a test email" title={`Send a test to ${testTo || "you"}`} disabled={!testTo || !!busy} onClick={() => run("test" + s.id, () => api("POST", `/api/integrations/email/${s.id}/test`, { to: testTo }), `Test email sent to ${testTo}`)}>
                  {busy === "test" + s.id ? <Spinner size={13} /> : <Send size={14} />}
                </IconButton>
                {(s.domainId || s.direct) && <Button size="sm" variant="ghost" onClick={() => setChecking(checking === s.id ? null : s.id)}>{checking === s.id ? "Hide" : "Domain"}</Button>}
                <IconButton aria-label="Edit" onClick={() => setEdit({ ...s, password: "" })}><Pencil size={14} /></IconButton>
                <IconButton aria-label="Delete" onClick={() => setDel(s)}><Trash2 size={14} /></IconButton>
                {checking === s.id && <div className="basis-full"><DomainStatus senderId={s.id} /></div>}
              </li>
            ))}
          </ul>
        )}
        {senders.length > 0 && (
          <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted">
            Test emails go to <Input className="max-w-[240px]" value={testTo} placeholder="you@example.com" onChange={(e) => setTestTo(e.target.value)} />
          </div>
        )}
      </Card>
      {ownDomain && <DomainForm onClose={() => setOwnDomain(false)} onDone={() => list.reload()} />}
      {direct && <DirectForm onClose={() => setDirect(false)} onDone={() => list.reload()} />}
      {edit && <SenderForm value={edit} busy={busy === "save"} onCancel={() => setEdit(null)}
        onSave={async (v) => { if (await run("save", () => api(v.id ? "PUT" : "POST", v.id ? `/api/integrations/email/${v.id}` : "/api/integrations/email", v), "Sender saved")) setEdit(null); }} />}
      <ConfirmDialog open={!!del} title={`Delete ${del?.label}?`} danger confirmLabel="Delete"
        message={del?.usedBy.length ? `Its status pages (${del.usedBy.join(", ")}) send through the default sender after this.` : "Emails no longer go out through it."}
        onCancel={() => setDel(null)} onConfirm={() => { const d = del!; setDel(null); run("del", () => api("DELETE", `/api/integrations/email/${d.id}`), "Sender deleted"); }} />
    </div>
  );
}

function SenderForm({ value, busy, onSave, onCancel }: { value: Sender; busy: boolean; onSave: (v: Sender) => void; onCancel: () => void }) {
  const [v, setV] = useState(value);
  const upd = (p: Partial<Sender>) => setV({ ...v, ...p });
  const preset = PRESETS.find((p) => p.host === v.host);
  return (
    <Card>
      <SectionTitle icon={<Mail size={15} />} title={value.id ? `Edit ${value.label}` : "New sender"} sub="Any email provider's SMTP. Pick a provider to fill in its server." />
      <div className="space-y-4">
        <div className="flex flex-wrap gap-2">
          {PRESETS.map((p) => (
            <Button key={p.name} size="sm" variant={v.host === p.host ? "primary" : "secondary"} onClick={() => upd({ host: p.host, port: p.port, security: p.security })}>{p.name}</Button>
          ))}
        </div>
        {preset && <p className="text-xs text-muted">{preset.note}</p>}
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="Name" hint="How you will know it, e.g. “Acme support”.">
            <Input value={v.label} maxLength={60} onChange={(e) => upd({ label: e.target.value })} />
          </Field>
          <Field label="Sender address">
            <Input value={v.from} placeholder="status@example.com" onChange={(e) => upd({ from: e.target.value })} />
          </Field>
          <Field label="Sender name">
            <Input value={v.fromName} placeholder="Acme Status" onChange={(e) => upd({ fromName: e.target.value })} />
          </Field>
          <Field label="SMTP server" className="sm:col-span-2">
            <Input value={v.host} placeholder="smtp.example.com" onChange={(e) => upd({ host: e.target.value })} />
          </Field>
          <Field label="Port">
            <Input type="number" value={v.port || ""} placeholder="587" onChange={(e) => upd({ port: Number(e.target.value) })} />
          </Field>
          <Field label="Security">
            <Select value={v.security || "starttls"} onChange={(e) => upd({ security: e.target.value as Sender["security"] })}>
              <option value="starttls">STARTTLS (port 587)</option>
              <option value="tls">TLS (port 465)</option>
              <option value="none">None (local relay only)</option>
            </Select>
          </Field>
          <Field label="Username">
            <Input value={v.username} autoComplete="off" onChange={(e) => upd({ username: e.target.value })} />
          </Field>
          <Field label="Password" hint={v.hasPassword && !v.password ? "Saved. Type to replace it." : undefined}>
            <Input type="password" value={v.password ?? ""} autoComplete="new-password" placeholder={v.hasPassword ? "••••••••" : ""} onChange={(e) => upd({ password: e.target.value })} />
          </Field>
        </div>
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={v.default} onChange={(e) => upd({ default: e.target.checked })} /> The default sender (on-call emails, and pages without their own)</label>
        <div className="flex gap-2 border-t border-line pt-4">
          <Button disabled={busy || !v.host || !v.from} onClick={() => onSave(v)}>{busy ? <Spinner size={14} /> : <Check size={14} />} Save</Button>
          <Button variant="ghost" onClick={onCancel}>Cancel</Button>
        </div>
      </div>
    </Card>
  );
}

/** A status page's sender: which of the senders its emails go out from. */
export function SenderPicker({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const list = useApi<Sender[]>("/api/integrations/email");
  const senders = list.data ?? [];
  const def = senders.find((s) => s.default);
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)} disabled={senders.length === 0}>
      <option value="">{def ? `Default (${def.label} · ${def.from})` : "No sender yet"}</option>
      {senders.map((s) => <option key={s.id} value={s.id}>{s.label} · {s.from}</option>)}
    </Select>
  );
}

export const useSenders = () => useApi<Sender[]>("/api/integrations/email");

interface Domain {
  id: string;
  name: string;
  status: string;
  records: { record: string; name: string; type: string; value: string; priority?: number; status: string }[];
}

const DOMAIN_TONE: Record<string, "ok" | "warn" | "crit" | undefined> = { verified: "ok", pending: "warn", not_started: "warn", failed: "crit", temporary_failure: "warn" };

/** Your own address (noreply@yourdomain.com), through Resend: the domain verified by DNS. */
function DomainForm({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const { toast } = useStore();
  const [f, setF] = useState({ address: "", fromName: "", apiKey: "", label: "" });
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<{ sender: string; domain: Domain; dnsAdded: boolean; note: string } | null>(null);
  const go = async () => {
    setBusy(true);
    try {
      const r = await api<{ sender: string; domain: Domain; dnsAdded: boolean; note: string }>("POST", "/api/integrations/email/domain", f);
      setRes(r);
      onDone();
      toast(r.domain.status === "verified" ? "Your domain is ready" : "Sender added: the domain is being verified");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <Card>
      <SectionTitle icon={<Globe size={15} />} title="Send from your own domain" sub="Like noreply-status@yourdomain.com. Fleetwatch sets it up with Resend (free for 3,000 emails a month): the domain is added there, its DNS records go into Cloudflare by themselves when one of your accounts holds the domain, and Resend verifies them." />
      {!res ? (
        <div className="space-y-4">
          <ol className="list-decimal space-y-1 pl-5 text-xs text-muted">
            <li>Make a free account at <a href="https://resend.com/signup" target="_blank" rel="noreferrer" className="underline">resend.com</a>.</li>
            <li>In Resend: <b className="text-fg">API Keys → Create API key</b> (full access, so Fleetwatch can add your domain), and paste it below.</li>
          </ol>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Address to send from" hint="Any name before the @; the domain must be yours.">
              <Input value={f.address} placeholder="noreply-status@yourdomain.com" onChange={(e) => setF({ ...f, address: e.target.value.trim() })} />
            </Field>
            <Field label="Sender name" hint="What people see as the sender.">
              <Input value={f.fromName} placeholder="Acme Status" onChange={(e) => setF({ ...f, fromName: e.target.value })} />
            </Field>
            <Field label="Resend API key" className="sm:col-span-2">
              <Input type="password" autoComplete="off" value={f.apiKey} placeholder="re_…" onChange={(e) => setF({ ...f, apiKey: e.target.value.trim() })} />
            </Field>
          </div>
          <div className="flex gap-2 border-t border-line pt-4">
            <Button disabled={busy || !f.address.includes("@") || !f.apiKey} onClick={go}>{busy ? <Spinner size={14} /> : <Check size={14} />} Set it up</Button>
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </div>
      ) : (
        <div className="space-y-3">
          <p className={res.dnsAdded ? "text-sm text-ok" : "text-sm"}>{res.note}</p>
          <DomainRecords d={res.domain} />
          <div className="flex gap-2"><DomainCheckButton senderId={res.sender} onChecked={(d) => setRes({ ...res, domain: d })} /><Button variant="ghost" onClick={onClose}>Done</Button></div>
        </div>
      )}
    </Card>
  );
}

function DomainRecords({ d }: { d: Domain }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2 text-sm"><b>{d.name}</b> <Badge tone={DOMAIN_TONE[d.status]}>{d.status.replace("_", " ")}</Badge></div>
      <div className="overflow-x-auto rounded-xl border border-line">
        <table className="w-full text-xs">
          <thead className="text-left text-muted"><tr><th className="px-3 py-2">Type</th><th className="px-3 py-2">Name</th><th className="px-3 py-2">Value</th><th className="px-3 py-2">Priority</th><th className="px-3 py-2">State</th></tr></thead>
          <tbody className="divide-y divide-line">
            {d.records.map((r, i) => (
              <tr key={i}>
                <td className="px-3 py-2 font-mono">{r.type}</td>
                <td className="px-3 py-2 font-mono">{r.name}.{d.name}</td>
                <td className="max-w-[320px] px-3 py-2"><span className="flex items-center gap-1"><span className="truncate font-mono" title={r.value}>{r.value}</span><button aria-label="Copy" onClick={() => navigator.clipboard.writeText(r.value)}><Copy size={11} /></button></span></td>
                <td className="px-3 py-2">{r.priority ?? ""}</td>
                <td className="px-3 py-2"><Badge tone={DOMAIN_TONE[r.status]}>{r.status.replace("_", " ")}</Badge></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function DomainCheckButton({ senderId, onChecked }: { senderId: string; onChecked: (d: Domain) => void }) {
  const { toast } = useStore();
  const [busy, setBusy] = useState(false);
  return (
    <Button size="sm" variant="secondary" disabled={busy} onClick={async () => {
      setBusy(true);
      try {
        const d = await api<Domain>("GET", `/api/integrations/email/${senderId}/domain`);
        onChecked(d);
        toast(d.status === "verified" ? "Verified: emails go out from your domain" : "Not verified yet: DNS changes can take a few minutes");
      } catch (e) {
        toast((e as ApiError).message, "err");
      }
      setBusy(false);
    }}>{busy ? <Spinner size={13} /> : <RefreshCw size={13} />} Check again</Button>
  );
}

/** A domain sender's records and state, checked when opened. */
function DomainStatus({ senderId }: { senderId: string }) {
  const d = useApi<Domain>(`/api/integrations/email/${senderId}/domain`);
  if (d.error) return <p className="text-xs text-crit">{d.error}</p>;
  if (!d.data) return <Spinner />;
  return <div className="space-y-2 pt-2"><DomainRecords d={d.data} /><DomainCheckButton senderId={senderId} onChecked={d.setData} /></div>;
}

interface Ready {
  ip: string;
  port25: boolean;
  ptr: string;
}

/** Free: this server signs and delivers the emails itself; DNS allows it. */
function DirectForm({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const { toast } = useStore();
  const ready = useApi<Ready>("/api/integrations/email/direct/check");
  const [f, setF] = useState({ address: "", fromName: "", label: "" });
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<{ sender: string; domain: Domain; dnsAdded: boolean; note: string } | null>(null);
  const go = async () => {
    setBusy(true);
    try {
      const r = await api<{ sender: string; domain: Domain; dnsAdded: boolean; note: string }>("POST", "/api/integrations/email/direct", f);
      setRes(r);
      onDone();
      toast("Sender added: check its DNS records below");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const r = ready.data;
  return (
    <Card>
      <SectionTitle icon={<Globe size={15} />} title="Send it yourself (free)" sub="No email company: this server signs each email with your domain's own key (DKIM) and delivers it straight to people's mail servers. Your domain's DNS gets two records that allow it, added in Cloudflare by themselves when it is there. Your existing email is not touched." />
      {!r ? <Spinner /> : (
        <ul className="mb-4 space-y-1.5 text-sm">
          <li className="flex items-start gap-2">{r.ip ? <Check size={14} className="mt-0.5 text-ok" /> : <AlertTriangle size={14} className="mt-0.5 text-crit" />}<span>Public address: <b className="font-mono">{r.ip || "not found"}</b></span></li>
          <li className="flex items-start gap-2">{r.port25 ? <Check size={14} className="mt-0.5 text-ok" /> : <AlertTriangle size={14} className="mt-0.5 text-crit" />}
            <span>{r.port25 ? "Outgoing port 25 is open: emails can leave." : "Outgoing port 25 is blocked by your server's provider (DigitalOcean, AWS and new Hetzner accounts do this). Ask them to unblock it, or use Gmail or Brevo's free plan with “Other SMTP” instead."}</span></li>
          <li className="flex items-start gap-2">{r.ptr ? <Check size={14} className="mt-0.5 text-ok" /> : <AlertTriangle size={14} className="mt-0.5 text-warn" />}
            <span>{r.ptr ? <>Reverse DNS: <b className="font-mono">{r.ptr}</b>. Best when it is a name of your domain (set it in your server provider's panel).</> : "No reverse DNS for this address: set one in your server provider's panel (like mail.yourdomain.com), or Gmail may put emails in spam."}</span></li>
        </ul>
      )}
      {!res ? (
        <div className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Address to send from" hint="Any name before the @; the domain must be yours.">
              <Input value={f.address} placeholder="noreply-status@yourdomain.com" onChange={(e) => setF({ ...f, address: e.target.value.trim() })} />
            </Field>
            <Field label="Sender name" hint="What people see as the sender.">
              <Input value={f.fromName} placeholder="Acme Status" onChange={(e) => setF({ ...f, fromName: e.target.value })} />
            </Field>
          </div>
          <div className="flex gap-2 border-t border-line pt-4">
            <Button disabled={busy || !f.address.includes("@") || !r?.ip} onClick={go}>{busy ? <Spinner size={14} /> : <Check size={14} />} Set it up</Button>
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
          </div>
        </div>
      ) : (
        <div className="space-y-3">
          <p className={res.dnsAdded ? "text-sm text-ok" : "text-sm"}>{res.note}</p>
          <DomainRecords d={res.domain} />
          <div className="flex gap-2"><DomainCheckButton senderId={res.sender} onChecked={(d) => setRes({ ...res, domain: d })} /><Button variant="ghost" onClick={onClose}>Done</Button></div>
        </div>
      )}
    </Card>
  );
}
