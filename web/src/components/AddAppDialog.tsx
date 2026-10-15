import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useStore } from "../store/store";
import { api, type ApiError } from "../api/client";
import type { App } from "../mock/types";
import { Button, Field, Input, Modal, Select, Spinner, Textarea } from "./ui";

type ManagerKey = "pm2" | "systemd" | "standalone";

export function AddAppDialog() {
  const { addAppOpen, closeAddApp, servers, upsertApp, toast } = useStore();
  const nav = useNavigate();
  const empty = { name: "", serverId: "", manager: "pm2" as ManagerKey, processKey: "", path: "", domain: "", notes: "" };
  const [f, setF] = useState(empty);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (addAppOpen.open) {
      setF({ ...empty, serverId: addAppOpen.serverId ?? servers[0]?.id ?? "" });
      setErr("");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [addAppOpen.open]);

  const set = (k: keyof typeof f, v: string) => setF((s) => ({ ...s, [k]: v }));

  const save = async () => {
    setErr("");
    setBusy(true);
    try {
      const app = await api<App>("POST", "/api/apps", f);
      upsertApp(app);
      toast(`${app.name} added to monitoring`);
      closeAddApp();
      nav(`/applications/${app.id}`);
    } catch (e) {
      setErr((e as ApiError).message);
    }
    setBusy(false);
  };

  return (
    <Modal
      open={addAppOpen.open}
      onClose={closeAddApp}
      title="Add application manually"
      footer={
        <>
          <Button variant="secondary" onClick={closeAddApp}>Cancel</Button>
          <Button onClick={save} disabled={busy || !servers.length}>{busy && <Spinner size={14} />} Add application</Button>
        </>
      }
    >
      {!servers.length ? (
        <p className="text-sm text-muted">Add a server first. Applications belong to a server.</p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Application name" className="sm:col-span-2">
            <Input value={f.name} maxLength={80} onChange={(e) => set("name", e.target.value)} placeholder="e.g. Billing API" autoFocus />
          </Field>
          <Field label="Server">
            <Select value={f.serverId} onChange={(e) => set("serverId", e.target.value)}>
              {servers.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </Select>
          </Field>
          <Field label="Process manager">
            <Select value={f.manager} onChange={(e) => set("manager", e.target.value)}>
              <option value="pm2">PM2</option>
              <option value="systemd">systemd</option>
              <option value="standalone">Standalone (folder only)</option>
            </Select>
          </Field>
          {f.manager !== "standalone" && (
            <Field label={f.manager === "systemd" ? "systemd unit" : "PM2 process name"} className="sm:col-span-2" hint={f.manager === "systemd" ? "The .service suffix is added if missing" : "As shown by pm2 list"}>
              <Input value={f.processKey} onChange={(e) => set("processKey", e.target.value)} placeholder={f.manager === "systemd" ? "billing-api.service" : "billing-api"} className="font-mono" />
            </Field>
          )}
          <Field label={f.manager === "standalone" ? "Project directory" : "Working directory (optional)"} className="sm:col-span-2" hint={f.manager === "standalone" ? undefined : "Detected from the running process when empty"}>
            <Input value={f.path} onChange={(e) => set("path", e.target.value)} placeholder="/var/www/billing-api" className="font-mono" />
          </Field>
          <Field label="Domain (optional)" className="sm:col-span-2">
            <Input value={f.domain} onChange={(e) => set("domain", e.target.value)} placeholder="billing.example.com" />
          </Field>
          <Field label="Notes" className="sm:col-span-2">
            <Textarea rows={3} value={f.notes} onChange={(e) => set("notes", e.target.value)} />
          </Field>
          {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit sm:col-span-2">{err}</div>}
        </div>
      )}
    </Modal>
  );
}
