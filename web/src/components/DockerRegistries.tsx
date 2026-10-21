import { useState } from "react";
import { Container, KeyRound, Plus, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, ConfirmDialog, Field, IconButton, Input, SectionTitle, Spinner } from "./ui";
import { relTime } from "../mock/util";

interface Registry {
  id: string;
  host: string;
  username: string;
  hasPassword: boolean;
  createdAt: number;
}

const PRESETS = [
  { host: "ghcr.io", name: "GitHub (GHCR)", note: "A GitHub token: Settings → Developer settings → Personal access tokens → classic, with read:packages. User: your GitHub name." },
  { host: "docker.io", name: "Docker Hub", note: "Docker Hub → Account settings → Personal access tokens (Read-only). User: your Docker Hub name." },
  { host: "registry.gitlab.com", name: "GitLab", note: "GitLab → a deploy token with read_registry (or a personal token). User: the token's user name." },
];

/** Integrations → Docker registries: logins for private images, used when servers pull
 * or build them (Docker apps, updates) and to check private images for updates. */
export function DockerRegistries() {
  const { toast } = useStore();
  const list = useApi<Registry[]>("/api/integrations/docker-registries");
  const [f, setF] = useState<{ host: string; username: string; password: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [del, setDel] = useState<Registry | null>(null);
  const preset = PRESETS.find((p) => p.host === f?.host);
  const save = async () => {
    setBusy(true);
    try {
      await api("POST", "/api/integrations/docker-registries", f);
      setF(null);
      list.reload();
      toast("Login saved: servers use it for private images");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <Card>
      <SectionTitle icon={<Container size={15} />} title="Docker registries"
        sub="Logins for private images (GHCR, Docker Hub, GitLab, any registry). Servers get them only while pulling or building, in a Docker config deleted right after; the update check uses them for private images too. Stored encrypted."
        action={!f && <Button size="sm" onClick={() => setF({ host: "ghcr.io", username: "", password: "" })}><Plus size={14} /> Add a login</Button>} />
      {f && (
        <div className="mb-4 space-y-3 rounded-2xl bg-surface2 p-4">
          <div className="flex flex-wrap gap-2">
            {PRESETS.map((p) => <Button key={p.host} size="sm" variant={f.host === p.host ? "primary" : "secondary"} onClick={() => setF({ ...f, host: p.host })}>{p.name}</Button>)}
          </div>
          {preset && <p className="text-xs text-muted">{preset.note}</p>}
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Registry"><Input value={f.host} className="font-mono" placeholder="ghcr.io" onChange={(e) => setF({ ...f, host: e.target.value.trim() })} /></Field>
            <Field label="User"><Input value={f.username} autoComplete="off" onChange={(e) => setF({ ...f, username: e.target.value.trim() })} /></Field>
            <Field label="Token or password"><Input type="password" value={f.password} autoComplete="new-password" onChange={(e) => setF({ ...f, password: e.target.value })} /></Field>
          </div>
          <div className="flex gap-2">
            <Button disabled={busy || !f.host || !f.username || !f.password} onClick={save}>{busy ? <Spinner size={14} /> : <KeyRound size={14} />} Save</Button>
            <Button variant="ghost" onClick={() => setF(null)}>Cancel</Button>
          </div>
        </div>
      )}
      {!list.data ? <Spinner /> : list.data.length === 0 ? (
        <p className="text-sm text-muted">None yet. Public images need no login.</p>
      ) : (
        <ul className="divide-y divide-line">
          {list.data.map((r) => (
            <li key={r.id} className="flex items-center justify-between gap-3 py-2.5">
              <div className="min-w-0">
                <div className="flex items-center gap-2 text-sm font-medium"><span className="font-mono">{r.host}</span> <Badge>{r.username}</Badge></div>
                <div className="text-xs text-muted">added {relTime(r.createdAt)}</div>
              </div>
              <IconButton aria-label="Delete" onClick={() => setDel(r)}><Trash2 size={14} /></IconButton>
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog open={!!del} danger title={`Delete the ${del?.host} login?`} confirmLabel="Delete" message="Servers can no longer pull private images from there through Fleetwatch (a docker login made on a server itself still works)."
        onCancel={() => setDel(null)} onConfirm={async () => {
          const r = del!;
          setDel(null);
          try {
            await api("DELETE", `/api/integrations/docker-registries/${r.id}`);
            list.reload();
            toast("Login deleted");
          } catch (e) {
            toast((e as ApiError).message, "err");
          }
        }} />
    </Card>
  );
}
