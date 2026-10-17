import { useState } from "react";
import { Plus, Trash2, Users } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ConfirmDialog, EmptyState, ErrorState, Field, IconButton, Input, LoadingState, Modal, SectionTitle, Segmented, Spinner, Table, Td, Toggle, ToggleRow, Tr } from "../components/ui";
import { relTime } from "../mock/util";

interface Member {
  id: string;
  username: string;
  displayName: string;
  email: string;
  role: "operator" | "viewer";
  servers: string[] | null; // null = every server
  disabled: boolean;
  createdAt: number;
  lastLoginAt: number;
  twofaEnabled: boolean;
}
type Form = Omit<Member, "id" | "createdAt" | "lastLoginAt" | "twofaEnabled"> & { id?: string; password: string; twofaEnabled?: boolean };

const ROLE_INFO = {
  operator: "Sees the servers below, restarts apps, runs their commands, handles incidents, turns on maintenance and cleans disks. Cannot change settings, servers or the team.",
  viewer: "Sees the servers below and everything about them, but cannot change anything.",
};

/** Settings → Team: people besides you, each with a role and, if you like, only some servers. */
export function TeamSettings() {
  const { servers, getServer, toast } = useStore();
  const list = useApi<Member[]>("/api/members");
  const [form, setForm] = useState<Form | null>(null);
  const [busy, setBusy] = useState(false);
  const [del, setDel] = useState<Member | null>(null);

  const save = async () => {
    if (!form) return;
    setBusy(true);
    try {
      const body = { username: form.username, displayName: form.displayName, email: form.email, role: form.role, servers: form.servers, disabled: form.disabled, password: form.password };
      await api(form.id ? "PUT" : "POST", form.id ? `/api/members/${form.id}` : "/api/members", body);
      toast(form.id ? "Saved" : `${form.username} can now sign in`);
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
        action={<Button size="sm" onClick={() => setForm({ username: "", displayName: "", email: "", role: "operator", servers: null, disabled: false, password: "" })}><Plus size={14} /> Member</Button>}
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
                <Badge tone={m.role === "operator" ? "info" : "neutral"} className="capitalize">{m.role}</Badge>
                {m.disabled && <Badge tone="warn" className="ml-1">disabled</Badge>}
                {m.twofaEnabled && <Badge tone="ok" className="ml-1">2FA</Badge>}
              </Td>
              <Td className="text-xs">{m.servers === null ? "All servers" : m.servers.map((id) => getServer(id)?.name ?? id).join(", ") || "None"}</Td>
              <Td className="text-xs text-muted">{m.lastLoginAt ? relTime(m.lastLoginAt) : "never"}</Td>
              <Td className="whitespace-nowrap text-right">
                <Button size="sm" variant="ghost" onClick={() => setForm({ ...m, password: "" })}>Edit</Button>
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
              <Button disabled={busy || !form.username || (!form.id && form.password.length < 12)} onClick={save}>{busy && <Spinner size={14} />} {form.id ? "Save" : "Add"}</Button>
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
              <Segmented value={form.role} onChange={(v) => setForm({ ...form, role: v })} options={[{ value: "operator", label: "Operator" }, { value: "viewer", label: "Viewer" }]} />
            </Field>
            <p className="-mt-2 text-xs text-muted">{ROLE_INFO[form.role]}</p>
            <ToggleRow label="All servers" desc="Including servers added later." checked={form.servers === null} onChange={(v) => setForm({ ...form, servers: v ? null : [] })} />
            {form.servers !== null && (
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
