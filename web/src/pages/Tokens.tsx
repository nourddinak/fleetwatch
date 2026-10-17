import { useState } from "react";
import { KeyRound, KeySquare, Terminal, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Button, Card, ConfirmDialog, CopyButton, EmptyState, ErrorState, Field, Input, LoadingState, Modal, SectionTitle, Spinner, Table, Td, Tr } from "../components/ui";
import { dateTime, relTime } from "../mock/util";

interface Token {
  id: string;
  name: string;
  prefix: string;
  createdAt: number;
  lastUsedAt: number;
  lastUsedIp: string;
}

function Snippet({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-2 rounded-2xl bg-surface2 px-4 py-3">
      <pre className="flex-1 overflow-x-auto whitespace-pre font-mono text-xs">{text}</pre>
      <CopyButton text={text} />
    </div>
  );
}

/** Settings → API tokens: read-only tokens for scripts and Prometheus. */
export function TokenSettings() {
  const { toast } = useStore();
  const list = useApi<Token[]>("/api/tokens");
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [created, setCreated] = useState<string | null>(null);
  const [revoke, setRevoke] = useState<Token | null>(null);
  const origin = location.origin;

  const create = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ token: string }>("POST", "/api/tokens", { name, password });
      setCreated(r.token);
      setCreating(false);
      setName("");
      setPassword("");
      list.reload();
    } catch (e) {
      setErr((e as ApiError).message);
    }
    setBusy(false);
  };
  const doRevoke = async () => {
    if (!revoke) return;
    try {
      await api("DELETE", `/api/tokens/${revoke.id}`);
      toast(`Token "${revoke.name}" revoked`);
      list.reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setRevoke(null);
  };

  return (
    <div className="space-y-6">
      <Card pad={false}>
        <div className="p-5 pb-0">
          <SectionTitle icon={<KeySquare size={15} />}
            title="API tokens"
            sub="Read-only access for scripts, Grafana and Prometheus. A token can read what the dashboard shows; it cannot run commands, change settings, or see sessions and the audit log."
            action={<Button size="sm" onClick={() => setCreating(true)}><KeyRound size={14} /> New token</Button>}
          />
        </div>
        {list.error ? (
          <div className="p-5"><ErrorState message={list.error} onRetry={list.reload} /></div>
        ) : !list.data ? (
          <LoadingState />
        ) : list.data.length === 0 ? (
          <div className="p-5"><EmptyState title="No API tokens" desc="Create one to read the API or scrape /metrics." /></div>
        ) : (
          <Table head={["Name", "Token", "Created", "Last used", ""]}>
            {list.data.map((t) => (
              <Tr key={t.id}>
                <Td className="font-medium">{t.name}</Td>
                <Td className="font-mono text-xs">{t.prefix}…</Td>
                <Td className="whitespace-nowrap text-xs">{dateTime(t.createdAt)}</Td>
                <Td className="whitespace-nowrap text-xs text-muted">{t.lastUsedAt ? `${relTime(t.lastUsedAt)} from ${t.lastUsedIp}` : "never"}</Td>
                <Td><Button size="sm" variant="ghost" onClick={() => setRevoke(t)}><Trash2 size={13} /> Revoke</Button></Td>
              </Tr>
            ))}
          </Table>
        )}
      </Card>

      <Card>
        <SectionTitle icon={<Terminal size={15} />} title="Using a token" sub="Send it as a bearer token." />
        <div className="space-y-3 text-sm">
          <Snippet text={`curl -H "Authorization: Bearer fwt_…" ${origin}/api/servers`} />
          <p className="text-muted">Prometheus (prometheus.yml):</p>
          <Snippet text={`scrape_configs:\n  - job_name: fleetwatch\n    scheme: ${origin.startsWith("https") ? "https" : "http"}\n    metrics_path: /metrics\n    authorization:\n      credentials: fwt_…\n    static_configs:\n      - targets: ["${location.host}"]`} />
        </div>
      </Card>

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="New API token"
        footer={<><Button variant="secondary" onClick={() => setCreating(false)}>Cancel</Button><Button disabled={busy || !name.trim() || !password} onClick={create}>{busy && <Spinner size={14} />} Create token</Button></>}
      >
        <div className="space-y-4">
          <Field label="Name" hint="What uses it, e.g. Grafana">
            <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <Field label="Your password" hint="Creating a token grants access, so it needs your password">
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" />
          </Field>
          {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>}
        </div>
      </Modal>

      <Modal open={!!created} onClose={() => setCreated(null)} title="Copy your token now" footer={<Button onClick={() => setCreated(null)}>Done</Button>}>
        <div className="space-y-3 text-sm">
          <p>This is the only time it is shown. Store it somewhere safe; if it is lost, revoke it and create a new one.</p>
          {created && <Snippet text={created} />}
        </div>
      </Modal>

      <ConfirmDialog
        open={!!revoke}
        danger
        title={`Revoke "${revoke?.name}"?`}
        confirmLabel="Revoke"
        message="Anything using this token stops working immediately."
        onCancel={() => setRevoke(null)}
        onConfirm={doRevoke}
      />
    </div>
  );
}
