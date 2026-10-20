import { Check, Mail, Pencil, Plus, Send, Star, Trash2 } from "lucide-react";
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
          action={!edit && <Button size="sm" onClick={() => setEdit({ ...EMPTY, default: senders.length === 0 })}><Plus size={14} /> Add a sender</Button>} />
        {!list.data ? <Spinner /> : senders.length === 0 && !edit ? (
          <p className="text-sm text-muted">None yet. Add one to send status page emails and on-call emails.</p>
        ) : (
          <ul className="space-y-2">
            {senders.map((s) => (
              <li key={s.id} className="flex flex-wrap items-center gap-3 rounded-2xl border border-line px-3 py-2.5">
                <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-surface2"><Mail size={16} /></span>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2 text-sm font-medium">{s.label} {s.default && <Badge tone="ok">default</Badge>}</div>
                  <div className="truncate text-xs text-muted">{s.fromName ? `${s.fromName} <${s.from}>` : s.from} · {s.host}</div>
                  <div className="text-xs text-muted">{s.usedBy.length ? `Status pages: ${s.usedBy.join(", ")}` : s.default ? "On-call emails, and pages without their own sender" : "Not used by a status page yet"}</div>
                </div>
                {!s.default && <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => run("def", () => api("PUT", `/api/integrations/email/${s.id}`, { ...s, password: "", default: true }), `${s.label} is the default now`)}><Star size={13} /> Make default</Button>}
                <IconButton aria-label="Send a test email" title={`Send a test to ${testTo || "you"}`} disabled={!testTo || !!busy} onClick={() => run("test" + s.id, () => api("POST", `/api/integrations/email/${s.id}/test`, { to: testTo }), `Test email sent to ${testTo}`)}>
                  {busy === "test" + s.id ? <Spinner size={13} /> : <Send size={14} />}
                </IconButton>
                <IconButton aria-label="Edit" onClick={() => setEdit({ ...s, password: "" })}><Pencil size={14} /></IconButton>
                <IconButton aria-label="Delete" onClick={() => setDel(s)}><Trash2 size={14} /></IconButton>
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
