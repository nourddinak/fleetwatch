import { useEffect, useRef, useState } from "react";
import { DatabaseBackup, Eye, FolderOpen, HardDrive, Pencil, Play, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CodeBlock, ConfirmDialog, EmptyState, ErrorState, Field, Gate, IconButton, Input, LoadingState, Modal, PageHeader, SectionTitle, Select, Spinner, Textarea, ToggleRow } from "../components/ui";
import { fmtBytes } from "../components/DiskRescue";
import { dateTime, relTime } from "../mock/util";

type Kind = "s3" | "gdrive" | "sftp";
interface Dest {
  id: string;
  name: string;
  kind: Kind;
  endpoint?: string;
  bucket?: string;
  region?: string;
  prefix?: string;
  keyId?: string;
  folder?: string;
  host?: string;
  port?: number;
  user?: string;
  path?: string;
  publicKey?: string;
  secret?: string;
  token?: string;
  hasSecret: boolean;
}
interface Item {
  kind: "postgres" | "mysql" | "sqlite" | "folder";
  name: string;
  size?: number;
}
interface Plan {
  id: string;
  name: string;
  folder: string;
  serverId: string;
  enabled: boolean;
  dest: string;
  items: Item[];
  at: string;
  timeZone: string;
  keepDaily: number;
  keepWeekly: number;
  keepMonthly: number;
}
interface Run {
  id: string;
  planId: string;
  serverId: string;
  started: number;
  finished: number;
  status: "running" | "ok" | "partial" | "failed";
  error?: string;
  items: (Item & { error?: string; added: number })[];
  manual: boolean;
}
interface Snapshot {
  id: string;
  time: number;
  paths: string[];
  tags: string[];
  size?: number;
}
interface Data {
  dests: Dest[];
  plans: Plan[];
  runs: Run[];
  snapshots: Record<string, Snapshot[]>;
  found: Record<string, { items: Item[] | null }>;
}

const KIND: Record<Kind, string> = { s3: "S3-style storage", gdrive: "Google Drive", sftp: "Another server (SFTP)" };
const ITEM: Record<Item["kind"], string> = { postgres: "PostgreSQL", mysql: "MySQL", sqlite: "SQLite", folder: "Folder" };
const RUN: Record<Run["status"], ["ok" | "warn" | "crit" | "info", string]> = { running: ["info", "Running"], ok: ["ok", "Done"], partial: ["warn", "Partly failed"], failed: ["crit", "Failed"] };
const S3 = [
  { name: "Cloudflare R2", endpoint: "https://<ACCOUNT_ID>.r2.cloudflarestorage.com", region: "auto", note: "R2 → Manage R2 API tokens → Create API token (Object Read & Write). 10 GB free." },
  { name: "Backblaze B2", endpoint: "https://s3.us-west-004.backblazeb2.com", region: "us-west-004", note: "B2 → Application Keys → Add a key. The endpoint is on the bucket's page. 10 GB free." },
  { name: "Google Cloud Storage", endpoint: "https://storage.googleapis.com", region: "auto", note: "Cloud Storage → Settings → Interoperability → Create a key (HMAC)." },
  { name: "AWS S3", endpoint: "https://s3.eu-central-1.amazonaws.com", region: "eu-central-1", note: "IAM → a user with access to the bucket → Access keys." },
  { name: "Wasabi", endpoint: "https://s3.eu-central-1.wasabisys.com", region: "eu-central-1", note: "Wasabi → Access Keys → Create new access key." },
];

export function Backups() {
  const { servers, toast, subscribeLive } = useStore();
  const data = useApi<Data>("/api/backups");
  const [destEdit, setDestEdit] = useState<Partial<Dest> | null>(null);
  const [destDel, setDestDel] = useState<Dest | null>(null);
  const [pw, setPw] = useState<{ name: string; password: string } | null>(null);
  const [restored, setRestored] = useState<{ folder?: string; files?: string[]; error?: string } | null>(null);
  const restores = useRef(new Set<string>());
  const { reload } = data;
  useEffect(
    () =>
      subscribeLive((m) => {
        if (m.type === "backup.found" || m.type === "backup.run") reload();
        if (m.type === "backup.run" && m.data.status === "failed") toast(`Backup failed on ${servers.find((s) => s.id === m.data.serverId)?.name ?? "a server"}`, "err");
        if (m.type === "backup.restored" && restores.current.has(m.data.runId)) {
          restores.current.delete(m.data.runId);
          setRestored(m.data);
        }
      }),
    [subscribeLive, reload, toast, servers],
  );
  const showPassword = async (d: Dest) => {
    try {
      setPw({ name: d.name, ...(await api<{ password: string }>("GET", `/api/backups/dests/${d.id}/password`)) });
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const d = data.data;
  return (
    <>
      <PageHeader title="Backups" sub="Your databases and folders, backed up every day with restic: encrypted before they leave the server, only new data sent each time, and the copies you choose kept (like 7 daily and 4 weekly)." />
      <Gate>
        {data.error ? (
          <ErrorState message={data.error} onRetry={reload} />
        ) : !d ? (
          <LoadingState />
        ) : (
          <div className="space-y-6">
            <Card>
              <SectionTitle icon={<HardDrive size={15} />} title="Where backups go" sub="Each backup gets its own encrypted folder there, named after it."
                action={
                  <div className="flex flex-wrap gap-2">
                    {(["s3", "gdrive", "sftp"] as Kind[]).map((k) => <Button key={k} size="sm" variant={k === "s3" ? "primary" : "secondary"} onClick={() => setDestEdit({ kind: k, port: 22, region: "auto" })}><Plus size={14} /> {KIND[k]}</Button>)}
                  </div>
                } />
              {d.dests.length === 0 ? (
                <p className="text-sm text-muted">None yet. Add one, then set up each server below.</p>
              ) : (
                <ul className="divide-y divide-line">
                  {d.dests.map((x) => (
                    <li key={x.id} className="space-y-2 py-3">
                      <div className="flex flex-wrap items-center justify-between gap-3">
                        <div className="min-w-0">
                          <div className="flex items-center gap-2 text-sm font-medium">{x.name} <Badge>{KIND[x.kind]}</Badge></div>
                          <div className="truncate text-xs text-muted">
                            {x.kind === "s3" ? `${x.endpoint} · ${x.bucket}${x.prefix ? "/" + x.prefix : ""}` : x.kind === "gdrive" ? `Drive folder “${x.folder}”` : `${x.user}@${x.host}:${x.path}`}
                          </div>
                        </div>
                        <div className="flex items-center gap-1">
                          <Button size="sm" variant="ghost" onClick={() => showPassword(x)}><Eye size={14} /> Password</Button>
                          <IconButton aria-label="Edit" onClick={() => setDestEdit(x)}><Pencil size={14} /></IconButton>
                          <IconButton aria-label="Delete" onClick={() => setDestDel(x)}><Trash2 size={14} /></IconButton>
                        </div>
                      </div>
                      {x.kind === "sftp" && x.publicKey && (
                        <div className="space-y-1 text-xs text-muted">
                          <p>On {x.host}, let this key sign in as {x.user} (once):</p>
                          <CodeBlock>{`mkdir -p ~/.ssh && echo '${x.publicKey}' >> ~/.ssh/authorized_keys`}</CodeBlock>
                        </div>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </Card>
            {servers.length === 0 && <EmptyState icon={<DatabaseBackup size={20} />} title="No servers yet" desc="Add a server first." />}
            {servers.map((s) => (
              <ServerBackups key={s.id} serverId={s.id} name={s.name} data={d} reload={reload} onRestore={(id) => restores.current.add(id)} />
            ))}
          </div>
        )}
      </Gate>
      {destEdit && <DestForm value={destEdit} onClose={() => setDestEdit(null)} onSaved={() => { setDestEdit(null); reload(); }} />}
      <ConfirmDialog open={!!destDel} danger title={`Delete ${destDel?.name}?`} confirmLabel="Delete" message="Fleetwatch forgets this place; the copies already there are not deleted."
        onCancel={() => setDestDel(null)} onConfirm={async () => {
          const x = destDel!;
          setDestDel(null);
          try {
            await api("DELETE", `/api/backups/dests/${x.id}`);
            reload();
            toast("Place deleted");
          } catch (e) {
            toast((e as ApiError).message, "err");
          }
        }} />
      <Modal open={!!pw} onClose={() => setPw(null)} title={`Encryption password · ${pw?.name ?? ""}`} footer={<Button onClick={() => setPw(null)}>Close</Button>}>
        <div className="space-y-3 text-sm">
          <p className="text-muted">Every copy is encrypted with this password. Keep it somewhere safe outside this dashboard: with it, the storage's keys and restic, you can restore without Fleetwatch.</p>
          <CodeBlock>{pw?.password ?? ""}</CodeBlock>
        </div>
      </Modal>
      <Modal open={!!restored} onClose={() => setRestored(null)} title={restored?.error ? "Restore failed" : "Copy restored"} wide footer={<Button onClick={() => setRestored(null)}>Close</Button>}>
        {restored?.error ? <p className="text-sm text-crit">{restored.error}</p> : (
          <div className="space-y-3 text-sm">
            <p>The files are in a folder of their own on the server; nothing live was changed:</p>
            <CodeBlock>{restored?.folder ?? ""}</CodeBlock>
            {restored?.files && <pre className="max-h-40 overflow-auto rounded-xl bg-surface2 p-3 font-mono text-xs">{restored.files.join("\n")}</pre>}
            <div className="space-y-1 text-xs text-muted">
              <p>To put a database back:</p>
              <p>PostgreSQL (.dump): <code>sudo -u postgres pg_restore --clean -d DBNAME FILE</code></p>
              <p>MySQL (.sql): <code>mysql &lt; FILE</code></p>
              <p>SQLite (.sql): <code>sqlite3 new.db &lt; FILE</code>; a copied .db file can replace the old one while the app is stopped.</p>
            </div>
          </div>
        )}
      </Modal>
    </>
  );
}

function ServerBackups({ serverId, name, data, reload, onRestore }: { serverId: string; name: string; data: Data; reload: () => void; onRestore: (runId: string) => void }) {
  const { toast } = useStore();
  const plans = data.plans.filter((p) => p.serverId === serverId);
  const [adding, setAdding] = useState(false);
  const scan = async () => {
    try {
      await api("POST", `/api/backups/servers/${serverId}/scan`);
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  return (
    <Card>
      <SectionTitle icon={<DatabaseBackup size={15} />} title={name}
        sub={plans.length ? `${plans.length} backup${plans.length === 1 ? "" : "s"}` : "Not backed up yet."}
        action={<Button size="sm" variant="secondary" disabled={data.dests.length === 0 || adding} onClick={() => { setAdding(true); if (!data.found[serverId]) scan(); }}><Plus size={14} /> Add backup</Button>} />
      {data.dests.length === 0 && plans.length === 0 && <p className="text-sm text-muted">Add a place for backups above first.</p>}
      <div className="space-y-4">
        {adding && <PlanForm serverId={serverId} data={data} scan={scan} onClose={() => setAdding(false)} onSaved={() => { setAdding(false); reload(); }} />}
        {plans.map((p) => <PlanCard key={p.id} plan={p} server={name} data={data} scan={scan} reload={reload} onRestore={onRestore} />)}
      </div>
    </Card>
  );
}

function PlanCard({ plan, server, data, scan, reload, onRestore }: { plan: Plan; server: string; data: Data; scan: () => void; reload: () => void; onRestore: (runId: string) => void }) {
  const { toast } = useStore();
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState("");
  const [restoreSnap, setRestoreSnap] = useState<Snapshot | null>(null);
  const [del, setDel] = useState(false);
  const runs = data.runs.filter((r) => r.planId === plan.id).slice(0, 5);
  const snaps = data.snapshots[plan.id] ?? [];
  const dest = data.dests.find((x) => x.id === plan.dest);
  const call = async (what: string, fn: () => Promise<unknown>, done: string) => {
    setBusy(what);
    try {
      await fn();
      toast(done);
      reload();
    } catch (e) {
      toast((e as ApiError).message, "err");
    } finally {
      setBusy("");
    }
  };
  return (
    <div className="rounded-2xl border border-line p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2 font-medium">{plan.name} {!plan.enabled && <Badge>paused</Badge>}</div>
          <div className="text-xs text-muted">
            {plan.enabled ? `Every day at ${plan.at} (${plan.timeZone})` : "Not running by itself"} · {plan.items.length} thing{plan.items.length === 1 ? "" : "s"} · keeps {plan.keepDaily} daily, {plan.keepWeekly} weekly, {plan.keepMonthly} monthly
          </div>
          <div className="text-xs text-muted">Saved in <span className="font-mono">{dest?.name ?? "?"} / {plan.folder}</span></div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" disabled={!!busy} onClick={() => call("run", () => api("POST", `/api/backups/plans/${plan.id}/run`), "Backup started")}>{busy === "run" ? <Spinner size={14} /> : <Play size={14} />} Back up now</Button>
          <Button size="sm" variant="secondary" onClick={() => { setEditing(!editing); if (!editing && !data.found[plan.serverId]) scan(); }}><Pencil size={14} /> {editing ? "Close" : "Change"}</Button>
          <IconButton aria-label="Remove backup" onClick={() => setDel(true)}><Trash2 size={14} /></IconButton>
        </div>
      </div>
      {editing && <div className="mt-4"><PlanForm serverId={plan.serverId} plan={plan} data={data} scan={scan} onClose={() => setEditing(false)} onSaved={() => { setEditing(false); reload(); }} /></div>}
      {runs.length > 0 && (
        <div className="mt-3">
          <div className="mb-1 text-xs font-medium text-muted">Recent runs</div>
          <ul className="divide-y divide-line text-sm">
            {runs.map((r) => (
              <li key={r.id} className="py-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="flex items-center gap-2"><Badge tone={RUN[r.status][0]}>{RUN[r.status][1]}</Badge> {dateTime(r.started)} {r.manual && <span className="text-xs text-muted">by hand</span>}</span>
                  <span className="text-xs text-muted">
                    {r.finished ? `${Math.max(1, Math.round((r.finished - r.started) / 1000))} s · ${fmtBytes(r.items.reduce((a, i) => a + (i.added || 0), 0))} new` : relTime(r.started)}
                  </span>
                </div>
                {r.error && <p className="break-all text-xs text-crit">{r.error}</p>}
                {r.items.filter((i) => i.error).map((i) => <p key={i.kind + i.name} className="break-all text-xs text-crit">{ITEM[i.kind]} {i.name}: {i.error}</p>)}
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="mt-3">
        <div className="mb-1 flex items-center justify-between text-xs font-medium text-muted">
          <span>Copies ({snaps.length})</span>
          <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => call("list", () => api("POST", `/api/backups/plans/${plan.id}/refresh`), "Asking the storage…")}><RefreshCw size={13} /> Refresh</Button>
        </div>
        {snaps.length === 0 ? <p className="text-xs text-muted">None yet.</p> : (
          <ul className="max-h-72 divide-y divide-line overflow-auto text-sm">
            {snaps.map((s) => (
              <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 py-1.5">
                <span className="min-w-0">
                  <span className="mr-2">{dateTime(s.time)}</span>
                  <span className="break-all font-mono text-xs text-muted">{s.paths.join(", ")}</span>
                  {s.size ? <span className="ml-2 text-xs text-muted">{fmtBytes(s.size)}</span> : null}
                </span>
                <Button size="sm" variant="ghost" onClick={() => setRestoreSnap(s)}><FolderOpen size={13} /> Restore</Button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <ConfirmDialog open={!!restoreSnap} title="Restore this copy?" confirmLabel="Restore"
        message={`The copy of ${dateTime(restoreSnap?.time ?? 0)} is put in a new folder on ${server}, next to the live data (nothing is overwritten). You then move back what you need.`}
        onCancel={() => setRestoreSnap(null)} onConfirm={async () => {
          const s = restoreSnap!;
          setRestoreSnap(null);
          try {
            const { runId } = await api<{ runId: string }>("POST", `/api/backups/plans/${plan.id}/restore`, { snapshot: s.id });
            onRestore(runId);
            toast("Restoring…");
          } catch (e) {
            toast((e as ApiError).message, "err");
          }
        }} />
      <ConfirmDialog open={del} danger title={`Remove “${plan.name}”?`} confirmLabel="Remove" message={`It stops running. Its copies stay in ${dest?.name ?? "the place"} / ${plan.folder}.`}
        onCancel={() => setDel(false)} onConfirm={() => { setDel(false); call("del", () => api("DELETE", `/api/backups/plans/${plan.id}`), "Backup removed (its copies stay)"); }} />
    </div>
  );
}

/** A backup's settings: new (no plan) or changing one. */
function PlanForm({ serverId, plan, data, scan, onClose, onSaved }: { serverId: string; plan?: Plan; data: Data; scan: () => void; onClose: () => void; onSaved: () => void }) {
  const { toast } = useStore();
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [p, setP] = useState<Plan>(plan ?? { id: "", name: "", folder: "", serverId, enabled: true, dest: data.dests[0]?.id ?? "", items: [], at: "03:00", timeZone: tz, keepDaily: 7, keepWeekly: 4, keepMonthly: 6 });
  const [busy, setBusy] = useState(false);
  const found = data.found[serverId]?.items ?? null;
  const folders = p.items.filter((i) => i.kind === "folder").map((i) => i.name).join("\n");
  const has = (it: Item) => p.items.some((x) => x.kind === it.kind && x.name === it.name);
  const toggle = (it: Item) => setP({ ...p, items: has(it) ? p.items.filter((x) => !(x.kind === it.kind && x.name === it.name)) : [...p.items, { kind: it.kind, name: it.name }] });
  const listed = [...(found ?? []), ...p.items.filter((i) => i.kind !== "folder" && !(found ?? []).some((f) => f.kind === i.kind && f.name === i.name))];
  const slug = p.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40) || "backup";
  const save = async () => {
    setBusy(true);
    try {
      await api(plan ? "PUT" : "POST", plan ? `/api/backups/plans/${plan.id}` : "/api/backups/plans", p);
      toast(plan ? "Backup saved" : `Backup “${p.name}” added`);
      onSaved();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="space-y-4 rounded-2xl bg-surface2 p-4">
      <Field label="Name" hint={plan ? `Its copies stay in the folder “${plan.folder}”.` : `Its copies go in a folder “${slug}” at the place (a number is added if that is taken).`}>
        <Input value={p.name} maxLength={60} placeholder="Shop database" onChange={(e) => setP({ ...p, name: e.target.value })} />
      </Field>
      <Field label="What to back up">
        <div className="space-y-1.5">
          {found === null && <p className="text-xs text-muted"><Spinner size={12} /> Looking for databases on the server…</p>}
          {found !== null && listed.length === 0 && <p className="text-xs text-muted">No PostgreSQL, MySQL or SQLite databases found. Add folders below.</p>}
          {listed.map((it) => (
            <label key={it.kind + it.name} className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={has(it)} onChange={() => toggle(it)} />
              <Badge>{ITEM[it.kind]}</Badge> <span className="break-all font-mono text-xs">{it.name}</span>
              {it.size ? <span className="text-xs text-muted">{fmtBytes(it.size)}</span> : null}
            </label>
          ))}
          <Button size="sm" variant="ghost" onClick={scan}><Search size={13} /> Look again</Button>
        </div>
      </Field>
      <Field label="Folders" hint="One full path per line, like /var/www/uploads.">
        <Textarea rows={2} className="font-mono" value={folders} placeholder="/var/www/app/storage"
          onChange={(e) => setP({ ...p, items: [...p.items.filter((i) => i.kind !== "folder"), ...e.target.value.split("\n").map((l) => l.trim()).filter(Boolean).map((name) => ({ kind: "folder" as const, name }))] })} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Where" hint={plan ? "A backup keeps its place." : undefined}>
          <Select value={p.dest} disabled={!!plan} onChange={(e) => setP({ ...p, dest: e.target.value })}>
            {data.dests.map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
          </Select>
        </Field>
        <Field label="Every day at" hint={p.timeZone}>
          <Input type="time" value={p.at} onChange={(e) => setP({ ...p, at: e.target.value })} />
        </Field>
        <Field label="Copies to keep" hint="daily · weekly · monthly">
          <div className="flex gap-2">
            {(["keepDaily", "keepWeekly", "keepMonthly"] as const).map((k) => <Input key={k} type="number" min={0} aria-label={k} value={p[k]} onChange={(e) => setP({ ...p, [k]: Number(e.target.value) })} />)}
          </div>
        </Field>
      </div>
      <ToggleRow label="Back up every day" desc="Off keeps the setup and the copies, without running by itself." checked={p.enabled} onChange={(v) => setP({ ...p, enabled: v })} />
      <div className="flex gap-2">
        <Button disabled={busy || !p.name.trim()} onClick={save}>{busy && <Spinner size={14} />} Save</Button>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
      </div>
    </div>
  );
}

function DestForm({ value, onClose, onSaved }: { value: Partial<Dest>; onClose: () => void; onSaved: () => void }) {
  const { toast } = useStore();
  const [f, setF] = useState<Partial<Dest>>(value);
  const [busy, setBusy] = useState(false);
  const upd = (p: Partial<Dest>) => setF({ ...f, ...p });
  const preset = S3.find((x) => x.endpoint === f.endpoint);
  const save = async () => {
    setBusy(true);
    try {
      await api(f.id ? "PUT" : "POST", f.id ? `/api/backups/dests/${f.id}` : "/api/backups/dests", f);
      toast(f.kind === "sftp" && !f.id ? "Saved: now add its key on that server (shown in the list)" : "Place saved");
      onSaved();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  const kept = f.hasSecret ? "Saved. Type to replace it." : undefined;
  return (
    <Modal open onClose={onClose} title={`${f.id ? "Edit" : "New"}: ${KIND[f.kind as Kind]}`} wide
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button disabled={busy || !f.name?.trim()} onClick={save}>{busy && <Spinner size={14} />} Save</Button></>}>
      <div className="space-y-4">
        <Field label="Name"><Input value={f.name ?? ""} maxLength={60} placeholder={f.kind === "gdrive" ? "My Google Drive" : f.kind === "sftp" ? "Backup server" : "Cloudflare R2"} onChange={(e) => upd({ name: e.target.value })} /></Field>
        {f.kind === "s3" && (
          <>
            <div className="flex flex-wrap gap-2">
              {S3.map((x) => <Button key={x.name} size="sm" variant={preset?.name === x.name ? "primary" : "secondary"} onClick={() => upd({ endpoint: x.endpoint, region: x.region, name: f.name || x.name })}>{x.name}</Button>)}
            </div>
            {preset && <p className="text-xs text-muted">{preset.note}</p>}
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Endpoint" className="sm:col-span-2"><Input value={f.endpoint ?? ""} className="font-mono" placeholder="https://<ACCOUNT_ID>.r2.cloudflarestorage.com" onChange={(e) => upd({ endpoint: e.target.value })} /></Field>
              <Field label="Bucket"><Input value={f.bucket ?? ""} className="font-mono" onChange={(e) => upd({ bucket: e.target.value.trim() })} /></Field>
              <Field label="Folder in the bucket" hint="Optional."><Input value={f.prefix ?? ""} className="font-mono" placeholder="fleetwatch" onChange={(e) => upd({ prefix: e.target.value })} /></Field>
              <Field label="Access key ID"><Input value={f.keyId ?? ""} className="font-mono" autoComplete="off" onChange={(e) => upd({ keyId: e.target.value.trim() })} /></Field>
              <Field label="Secret access key" hint={kept}><Input type="password" value={f.secret ?? ""} autoComplete="new-password" onChange={(e) => upd({ secret: e.target.value })} /></Field>
              <Field label="Region" hint="auto works for R2 and Google."><Input value={f.region ?? ""} className="font-mono" onChange={(e) => upd({ region: e.target.value.trim() })} /></Field>
            </div>
          </>
        )}
        {f.kind === "gdrive" && (
          <>
            <ol className="list-decimal space-y-1 pl-5 text-xs text-muted">
              <li>On your own computer, install rclone: <a className="underline" href="https://rclone.org/install/" target="_blank" rel="noreferrer">rclone.org/install</a> (one file, free).</li>
              <li>Run <code className="text-fg">rclone authorize "drive"</code>: a browser opens; sign in to Google and allow it.</li>
              <li>rclone prints a token between two lines of arrows, starting with <code className="text-fg">{`{"access_token"`}</code>. Copy all of it and paste it here.</li>
            </ol>
            <Field label="Token" hint={kept}><Textarea rows={3} className="font-mono" value={f.token ?? ""} placeholder='{"access_token":"…","token_type":"Bearer","refresh_token":"…","expiry":"…"}' onChange={(e) => upd({ token: e.target.value })} /></Field>
            <Field label="Folder in your Drive"><Input value={f.folder ?? "Fleetwatch backups"} onChange={(e) => upd({ folder: e.target.value })} /></Field>
            <p className="text-xs text-muted">Drive is slower than S3-style storage and has daily upload limits (750 GB); fine for databases and normal folders.</p>
          </>
        )}
        {f.kind === "sftp" && (
          <>
            <p className="text-xs text-muted">Any machine you can SSH into: another of your servers, a NAS, a storage box. Fleetwatch makes its own key for it; after saving, you add that key there once.</p>
            <div className="grid gap-4 sm:grid-cols-3">
              <Field label="Address" className="sm:col-span-2"><Input value={f.host ?? ""} className="font-mono" placeholder="backup.example.com" onChange={(e) => upd({ host: e.target.value.trim() })} /></Field>
              <Field label="SSH port"><Input type="number" value={f.port ?? 22} onChange={(e) => upd({ port: Number(e.target.value) })} /></Field>
              <Field label="User"><Input value={f.user ?? ""} className="font-mono" onChange={(e) => upd({ user: e.target.value.trim() })} /></Field>
              <Field label="Folder there" className="sm:col-span-2"><Input value={f.path ?? ""} className="font-mono" placeholder="/backups" onChange={(e) => upd({ path: e.target.value })} /></Field>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}
