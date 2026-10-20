import { useState } from "react";
import { BellRing, Pencil, Plus, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, Field, IconButton, Input, Modal, SectionTitle, Select, Spinner } from "./ui";
import { ApplicationPicker, ServerPicker } from "./domain";

interface LogAlert {
  id: string;
  name: string;
  match: string;
  regex: boolean;
  count: number;
  windowMin: number;
  serverId: string;
  appId: string;
  severity: "warning" | "critical";
  enabled: boolean;
}

const EMPTY: LogAlert = { id: "", name: "", match: "", regex: false, count: 5, windowMin: 10, serverId: "", appId: "", severity: "warning", enabled: true };

/** Logs → Log alerts: “alert me when payment failed shows up 5 times in 10 minutes”. */
export function LogAlerts() {
  const { toast, getServer, apps } = useStore();
  const list = useApi<LogAlert[]>("/api/settings/log-alerts");
  const [edit, setEdit] = useState<{ a: LogAlert; i: number } | null>(null);
  const [busy, setBusy] = useState(false);
  const rules = list.data ?? [];
  const save = async (next: LogAlert[], done: string) => {
    setBusy(true);
    try {
      list.setData(await api<LogAlert[]>("PUT", "/api/settings/log-alerts", next));
      setEdit(null);
      toast(done);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <Card className="mb-4">
      <SectionTitle icon={<BellRing size={15} />} title="Log alerts"
        sub="Get an alert when some text shows up in the logs too often, like “payment failed” 5 times in 10 minutes. It opens an incident, so it reaches your notifications, WhatsApp and whoever is on call."
        action={<Button size="sm" onClick={() => setEdit({ a: EMPTY, i: -1 })}><Plus size={14} /> Add alert</Button>} />
      {!list.data ? <Spinner /> : rules.length === 0 ? (
        <p className="text-sm text-muted">None yet.</p>
      ) : (
        <ul className="divide-y divide-line">
          {rules.map((a, i) => (
            <li key={a.id} className="flex flex-wrap items-center justify-between gap-3 py-2.5">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
                  {a.name}
                  {a.severity === "critical" && <Badge tone="crit">critical</Badge>}
                  {!a.enabled && <Badge>off</Badge>}
                </div>
                <div className="text-xs text-muted">
                  <code className="font-mono">{a.regex ? `/${a.match}/` : `“${a.match}”`}</code> {a.count}+ time{a.count === 1 ? "" : "s"} in {a.windowMin} min
                  {" · "}{a.appId ? apps.find((x) => x.id === a.appId)?.name ?? "deleted app" : a.serverId ? getServer(a.serverId)?.name ?? "deleted server" : "all servers"}
                </div>
              </div>
              <div className="flex items-center gap-1">
                <Button size="sm" variant="ghost" disabled={busy} onClick={() => save(rules.map((x, j) => (j === i ? { ...x, enabled: !x.enabled } : x)), a.enabled ? "Alert turned off" : "Alert turned on")}>{a.enabled ? "Turn off" : "Turn on"}</Button>
                <IconButton aria-label="Edit" onClick={() => setEdit({ a, i })}><Pencil size={14} /></IconButton>
                <IconButton aria-label="Delete" onClick={() => save(rules.filter((_, j) => j !== i), "Alert deleted")}><Trash2 size={14} /></IconButton>
              </div>
            </li>
          ))}
        </ul>
      )}
      {edit && (
        <AlertForm value={edit.a} busy={busy} onClose={() => setEdit(null)}
          onSave={(a) => save(edit.i < 0 ? [...rules, a] : rules.map((x, j) => (j === edit.i ? a : x)), "Log alert saved")} />
      )}
    </Card>
  );
}

function AlertForm({ value, busy, onSave, onClose }: { value: LogAlert; busy: boolean; onSave: (a: LogAlert) => void; onClose: () => void }) {
  const [f, setF] = useState(value);
  const upd = (p: Partial<LogAlert>) => setF({ ...f, ...p });
  return (
    <Modal open onClose={onClose} title={value.id ? `Edit ${value.name}` : "New log alert"}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button disabled={busy || !f.name.trim() || !f.match.trim()} onClick={() => onSave(f)}>{busy && <Spinner size={14} />} Save</Button></>}>
      <div className="space-y-4">
        <Field label="Name" hint="Like “Payments failing”.">
          <Input value={f.name} maxLength={80} onChange={(e) => upd({ name: e.target.value })} />
        </Field>
        <Field label="Text to look for" hint={f.regex ? "A regular expression (any case)." : "Any case; anywhere in the line."}>
          <Input value={f.match} maxLength={300} placeholder="payment failed" className="font-mono" onChange={(e) => upd({ match: e.target.value })} />
        </Field>
        <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={f.regex} onChange={(e) => upd({ regex: e.target.checked })} /> It is a regular expression</label>
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="At least (times)">
            <Input type="number" min={1} value={f.count} onChange={(e) => upd({ count: Number(e.target.value) })} />
          </Field>
          <Field label="Within (minutes)">
            <Input type="number" min={1} value={f.windowMin} onChange={(e) => upd({ windowMin: Number(e.target.value) })} />
          </Field>
          <Field label="Severity">
            <Select value={f.severity} onChange={(e) => upd({ severity: e.target.value as LogAlert["severity"] })}>
              <option value="warning">Warning</option>
              <option value="critical">Critical (calls on-call)</option>
            </Select>
          </Field>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Server">
            <ServerPicker value={f.serverId || "all"} onChange={(v) => upd({ serverId: v === "all" ? "" : v, appId: "" })} />
          </Field>
          <Field label="Application">
            <ApplicationPicker value={f.appId || "all"} serverId={f.serverId || "all"} onChange={(v) => upd({ appId: v === "all" ? "" : v })} />
          </Field>
        </div>
      </div>
    </Modal>
  );
}
