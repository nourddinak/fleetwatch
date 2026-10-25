import { useState } from "react";
import { Link } from "react-router-dom";
import { ExternalLink, Plus, Trash2, Users } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, IconButton, Input, LoadingState, Modal, SectionTitle, Segmented, Spinner, Table, Td, Toggle, ToggleRow, Tr, WhatsApp } from "../components/ui";
import { relTime } from "../mock/util";

interface Member {
  id: string;
  username: string;
  displayName: string;
  email: string;
  role: "operator" | "viewer" | "client";
  servers: string[] | null; // null = every server
  portal: Portal;
  disabled: boolean;
  createdAt: number;
  lastLoginAt: number;
  twofaEnabled: boolean;
}
interface Portal {
  apps: string[];
  checks: string[];
  pages: string[];
}
type Form = Omit<Member, "id" | "createdAt" | "lastLoginAt" | "twofaEnabled"> & { id?: string; password: string; twofaEnabled?: boolean };

const ROLE_INFO = {
  operator: "Sees the servers below, restarts apps, runs their commands, handles incidents, turns on maintenance and cleans disks. Cannot change settings, servers or the team.",
  viewer: "Sees the servers below and everything about them, but cannot change anything.",
  client: "A customer: signs in to a portal of their own, branded like their status page, with only the apps, uptime checks and status pages picked below. They see nothing of the dashboard, your servers or other clients.",
};

/** Settings → Team: people besides you, each with a role and, if you like, only some servers. */
export function TeamSettings() {
  const { servers, getServer, toast } = useStore();
  const list = useApi<Member[]>("/api/members");
  const checks = useApi<{ id: string; name: string }[]>("/api/uptime");
  const pages = useApi<{ id: string; title: string }[]>("/api/status-pages");
  const { apps } = useStore();
  const [form, setForm] = useState<Form | null>(null);
  const [busy, setBusy] = useState(false);
  const [del, setDel] = useState<Member | null>(null);
  const wa = useApi<{ status: { linked: boolean }; people: { id: string; phone: string }[] }>("/api/integrations/whatsapp");
  const [invite, setInvite] = useState({ send: false, phone: "", withPassword: false });
  const open = (f: Form) => {
    setForm(f);
    setInvite({ send: false, phone: wa.data?.people.find((p) => p.id === f.id)?.phone ?? "", withPassword: false });
  };

  const save = async () => {
    if (!form) return;
    setBusy(true);
    try {
      const body = { username: form.username, displayName: form.displayName, email: form.email, role: form.role, servers: form.servers, disabled: form.disabled, password: form.password, portal: form.portal };
      const m = await api<Member>(form.id ? "PUT" : "POST", form.id ? `/api/members/${form.id}` : "/api/members", body);
      toast(form.id ? "Saved" : `${form.username} can now sign in`);
      if (invite.send) {
        try {
          await api("POST", `/api/members/${m.id}/invite`, { phone: invite.phone, password: invite.withPassword ? form.password : "" });
          toast("Sign-in details sent on WhatsApp");
        } catch (e) {
          toast(`Not sent on WhatsApp: ${(e as ApiError).message}`, "err");
        }
      }
      setForm(null);
      list.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const remove = async (m: Member) => {
    setDel(null);
    try {
      await api("DELETE", `/api/members/${m.id}`);
      toast(`${m.username} removed`);
      list.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const toggleServer = (id: string) => {
    if (!form) return;
    const cur = form.servers ?? [];
    setForm({ ...form, servers: cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id] });
  };

  return (
    <Card>
      <SectionTitle icon={<Users size={15} />}
        title="Team"
        sub="Give others their own sign-in. Every action they take is in the audit log under their name. You stay the owner: only you change settings, servers and the team."
        action={<Button size="sm" onClick={() => open({ username: "", displayName: "", email: "", role: "operator", servers: null, disabled: false, password: "", portal: { apps: [], checks: [], pages: [] } })}><Plus size={14} /> Member</Button>}
      />
      {list.error ? (
        <ErrorState message={list.error} onRetry={list.reload} />
      ) : !list.data ? (
        <LoadingState />
      ) : list.data.length === 0 ? (
        <EmptyState icon={<Users size={20} />} title="Only you so far" desc="Add an operator who can restart apps and handle incidents, or a viewer who can only look." />
      ) : (
        <Table head={["Member", "Role", "Servers", "Last sign-in", ""]}>
          {list.data.map((m) => (
            <Tr key={m.id}>
              <Td>
                <div className="font-medium">{m.displayName || m.username}</div>
                <div className="text-xs text-muted">{m.username}{m.email ? ` · ${m.email}` : ""}</div>
              </Td>
              <Td>
                <Badge tone={m.role === "operator" ? "info" : m.role === "client" ? "ok" : "neutral"} className="capitalize">{m.role}</Badge>
                {m.disabled && <Badge tone="warn" className="ml-1">disabled</Badge>}
                {m.twofaEnabled && <Badge tone="ok" className="ml-1">2FA</Badge>}
              </Td>
              <Td className="text-xs">{m.role === "client" ? `Portal: ${m.portal.apps.length} apps, ${m.portal.checks.length} checks, ${m.portal.pages.length} status pages` : m.servers === null ? "All servers" : m.servers.map((id) => getServer(id)?.name ?? id).join(", ") || "None"}</Td>
              <Td className="text-xs text-muted">{m.lastLoginAt ? relTime(m.lastLoginAt) : "never"}</Td>
              <Td className="whitespace-nowrap text-right">
                {m.role === "client" && <Link to={`/portal?member=${m.id}`}><Button size="sm" variant="ghost"><ExternalLink size={13} /> Portal</Button></Link>}
                <Button size="sm" variant="ghost" onClick={() => open({ ...m, password: "" })}>Edit</Button>
                <IconButton aria-label="Remove member" onClick={() => setDel(m)}><Trash2 size={14} /></IconButton>
              </Td>
            </Tr>
          ))}
        </Table>
      )}

      {form && (
        <Modal
          open
          wide
          onClose={() => setForm(null)}
          title={form.id ? `Edit ${form.username}` : "New team member"}
          footer={
            <>
              <Button variant="ghost" onClick={() => setForm(null)}>Cancel</Button>
              <Button disabled={busy || !form.username || (!form.id && form.password.length < 12) || (invite.send && !invite.phone.trim())} onClick={save}>{busy && <Spinner size={14} />} {form.id ? "Save" : "Add"}</Button>
            </>
          }
        >
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label="Username">
                <Input value={form.username} disabled={!!form.id} autoComplete="off" onChange={(e) => setForm({ ...form, username: e.target.value })} />
              </Field>
              <Field label="Name">
                <Input value={form.displayName} onChange={(e) => setForm({ ...form, displayName: e.target.value })} />
              </Field>
              <Field label="Email" hint="Used for on-call alerts.">
                <Input value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
              </Field>
            </div>
            <Field label="Role">
              <Segmented value={form.role} onChange={(v) => setForm({ ...form, role: v })} options={[{ value: "operator", label: "Operator" }, { value: "viewer", label: "Viewer" }, { value: "client", label: "Client" }]} />
            </Field>
            <p className="-mt-2 text-xs text-muted">{ROLE_INFO[form.role]}</p>
            {form.role === "client" && (
              <div className="grid gap-4 sm:grid-cols-3">
                {([
                  ["apps", "Apps", apps.map((a) => ({ id: a.id, name: a.name }))],
                  ["checks", "Uptime checks", (checks.data ?? []).map((c) => ({ id: c.id, name: c.name }))],
                  ["pages", "Status pages", (pages.data ?? []).map((p) => ({ id: p.id, name: p.title }))],
                ] as const).map(([key, label, items]) => (
                  <Field key={key} label={label}>
                    <div className="max-h-48 space-y-1 overflow-auto rounded-xl bg-surface2 p-2">
                      {items.length === 0 && <p className="px-1 text-xs text-muted">None yet.</p>}
                      {items.map((it) => (
                        <label key={it.id} className="flex items-center gap-2 px-1 py-0.5 text-sm">
                          <input type="checkbox" checked={form.portal[key].includes(it.id)}
                            onChange={(e) => setForm({ ...form, portal: { ...form.portal, [key]: e.target.checked ? [...form.portal[key], it.id] : form.portal[key].filter((x) => x !== it.id) } })} />
                          <span className="truncate">{it.name}</span>
                        </label>
                      ))}
                    </div>
                  </Field>
                ))}
              </div>
            )}
            {form.role !== "client" && <ToggleRow label="All servers" desc="Including servers added later." checked={form.servers === null} onChange={(v) => setForm({ ...form, servers: v ? null : [] })} />}
            {form.role !== "client" && form.servers !== null && (
              <div className="grid gap-2 sm:grid-cols-2">
                {servers.map((s) => (
                  <label key={s.id} className="flex items-center gap-2 rounded-xl bg-surface2 px-3 py-2 text-sm">
                    <Toggle checked={form.servers!.includes(s.id)} onChange={() => toggleServer(s.id)} /> {s.name}
                  </label>
                ))}
              </div>
            )}
            <Field label={form.id ? "New password (optional)" : "Password"} hint="At least 12 characters. Changing it signs them out everywhere.">
              <Input type="password" autoComplete="new-password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
            </Field>
            {wa.data?.status.linked && (
              <div className="space-y-3 rounded-xl bg-surface2 px-3 py-3">
                <label className="flex items-center gap-2 text-sm font-medium">
                  <Toggle checked={invite.send} onChange={(v) => setInvite({ ...invite, send: v })} /> <WhatsApp size={14} /> Send them the sign-in details on WhatsApp
                </label>
                {invite.send && (
                  <>
                    <Field label="Their WhatsApp number" hint="With the country code. Saved as their number for alerts too.">
                      <Input value={invite.phone} onChange={(e) => setInvite({ ...invite, phone: e.target.value })} placeholder="+212 6 12 34 56 78" autoComplete="off" />
                    </Field>
                    {form.password && (
                      <label className="flex items-center gap-2 text-xs">
                        <input type="checkbox" checked={invite.withPassword} onChange={(e) => setInvite({ ...invite, withPassword: e.target.checked })} />
                        Include the password (they are asked to change it); without it, the message says to ask you
                      </label>
                    )}
                  </>
                )}
              </div>
            )}
            {form.id && <ToggleRow label="Disabled" desc="Cannot sign in; signed out right away." checked={form.disabled} onChange={(v) => setForm({ ...form, disabled: v })} />}
            {form.id && form.twofaEnabled && (
              <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl bg-surface2 px-3 py-2 text-sm">
                <span>Two-factor authentication is on. Lost their phone? Reset it; they set it up again at their next sign-in.</span>
                <Button size="sm" variant="secondary" onClick={async () => {
                  try {
                    await api("POST", `/api/members/${form.id}/reset-2fa`);
                    toast("2FA reset and signed out");
                    setForm({ ...form, twofaEnabled: false });
                    list.reload();
                  } catch (e) {
                    toast((e as ApiError).message, "err");
                  }
                }}>Reset 2FA</Button>
              </div>
            )}
          </div>
        </Modal>
      )}
      <ConfirmDialog open={!!del} title={`Remove ${del?.username}?`} message="They are signed out and can no longer sign in. Their past actions stay in the audit log." confirmLabel="Remove" danger onConfirm={() => del && remove(del)} onCancel={() => setDel(null)} />
    </Card>
  );
}
