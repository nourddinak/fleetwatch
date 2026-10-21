import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Database, Play, Plus, Server as ServerIcon, Table2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CopyButton, EmptyState, ErrorState, Field, Gate, Input, LoadingState, Modal, PageHeader, Select, Spinner, Textarea } from "../components/ui";
import { fmtBytes } from "../components/DiskRescue";
import { cn } from "../utils/cn";
import { ServerTabs } from "./Docker";

type Engine = "postgres" | "mysql" | "sqlite";
interface DB {
  kind: Engine | "folder";
  name: string;
  size?: number;
}
interface Result {
  databases?: DB[];
  engines?: Engine[];
  tables?: { name: string; rows: number; size?: number }[];
  columns?: string[];
  rows?: (string | null)[][];
  truncated?: boolean;
  tookMs: number;
}
const ENGINE: Record<Engine, string> = { postgres: "PostgreSQL", mysql: "MySQL", sqlite: "SQLite" };

/** Databases: look into a server's PostgreSQL, MySQL and SQLite databases with read-only
 * queries, and create a database with its own user. Owner only. */
export function Databases() {
  const { servers } = useStore();
  const [params, setParams] = useSearchParams();
  const picked = servers.find((s) => s.id === params.get("server")) ?? servers[0];
  return (
    <>
      <PageHeader title="Databases" sub="Your servers' PostgreSQL, MySQL and SQLite databases: see their tables, run read-only queries, and create a database with its own user." />
      <Gate>
        {!picked ? <EmptyState icon={<ServerIcon size={20} />} title="No servers yet" desc="Add a server first." /> : (
          <Card>
            <ServerTabs value={picked.id} onChange={(id) => { params.set("server", id); setParams(params, { replace: true }); }} />
            <ServerDatabases key={picked.id} serverId={picked.id} />
          </Card>
        )}
      </Gate>
    </>
  );
}

function quote(engine: Engine, name: string) {
  const parts = name.split(".");
  return parts.map((p) => (engine === "mysql" ? "`" + p.replace(/`/g, "``") + "`" : '"' + p.replace(/"/g, '""') + '"')).join(".");
}

function ServerDatabases({ serverId }: { serverId: string }) {
  const { toast } = useStore();
  const list = useApi<Result>(`/api/servers/${serverId}/db`);
  const [db, setDb] = useState<DB | null>(null);
  const [creating, setCreating] = useState(false);
  const dbs = (list.data?.databases ?? []).filter((d) => d.kind !== "folder") as (DB & { kind: Engine })[];
  useEffect(() => {
    if (!db && dbs.length) setDb(dbs[0]);
  }, [db, dbs]);
  if (list.error) return <ErrorState message={list.error} onRetry={list.reload} />;
  if (!list.data) return <LoadingState label="Asking the server which databases it has…" />;
  return (
    <div className="grid gap-5 lg:grid-cols-[16rem_1fr]">
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-xs font-medium uppercase tracking-wide text-muted">Databases ({dbs.length})</span>
          {(list.data.engines?.length ?? 0) > 0 && <Button size="sm" variant="ghost" onClick={() => setCreating(true)}><Plus size={13} /> New</Button>}
        </div>
        {dbs.length === 0 ? <p className="text-sm text-muted">No PostgreSQL, MySQL or SQLite databases found on this server.</p> : (
          <ul className="max-h-[60vh] space-y-1 overflow-auto">
            {dbs.map((d) => (
              <li key={d.kind + d.name}>
                <button onClick={() => setDb(d)} className={cn("w-full rounded-xl px-3 py-2 text-left text-sm", db?.kind === d.kind && db.name === d.name ? "bg-accent/10 font-medium" : "hover:bg-surface2")}>
                  <div className="flex items-center gap-2"><Database size={13} className="shrink-0 text-muted" /><span className="truncate" title={d.name}>{d.kind === "sqlite" ? d.name.split("/").pop() : d.name}</span></div>
                  <div className="ml-5 text-[11px] text-muted">{ENGINE[d.kind]}{d.size ? ` · ${fmtBytes(d.size)}` : ""}{d.kind === "sqlite" ? ` · ${d.name}` : ""}</div>
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      {db ? <DatabaseView key={db.kind + db.name} serverId={serverId} db={db as DB & { kind: Engine }} /> : <div />}
      {creating && <NewDatabase serverId={serverId} engines={list.data.engines ?? []} onClose={() => setCreating(false)} onDone={() => { list.reload(); toast("Database created"); }} />}
    </div>
  );
}

function DatabaseView({ serverId, db }: { serverId: string; db: DB & { kind: Engine } }) {
  const tables = useApi<Result>(`/api/servers/${serverId}/db/tables?engine=${db.kind}&db=${encodeURIComponent(db.name)}`);
  const [q, setQ] = useState("");
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<Result | null>(null);
  const [err, setErr] = useState("");
  const run = async (query = q) => {
    setBusy(true);
    setErr("");
    try {
      setRes(await api<Result>("POST", `/api/servers/${serverId}/db/query`, { engine: db.kind, database: db.name, query }));
    } catch (e) {
      setRes(null);
      setErr((e as ApiError).message);
    }
    setBusy(false);
  };
  const open = (t: string) => {
    const query = `SELECT * FROM ${quote(db.kind, t)} LIMIT 100`;
    setQ(query);
    run(query);
  };
  return (
    <div className="min-w-0 space-y-4">
      <div>
        <div className="mb-2 text-xs font-medium uppercase tracking-wide text-muted">Tables</div>
        {tables.error ? <p className="text-sm text-crit">{tables.error}</p> : !tables.data ? <Spinner /> : (tables.data.tables ?? []).length === 0 ? <p className="text-sm text-muted">No tables.</p> : (
          <div className="flex max-h-40 flex-wrap gap-1.5 overflow-auto">
            {tables.data.tables!.map((t) => (
              <button key={t.name} onClick={() => open(t.name)} className="inline-flex items-center gap-1.5 rounded-full border border-line px-2.5 py-1 text-xs hover:border-fg/30">
                <Table2 size={12} className="text-muted" />{t.name}<span className="text-muted">{t.rows.toLocaleString()}{t.size ? ` · ${fmtBytes(t.size)}` : ""}</span>
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="space-y-2">
        <Textarea rows={4} value={q} className="font-mono text-[12px]" placeholder={`SELECT * FROM ${db.kind === "mysql" ? "`users`" : '"users"'} LIMIT 100`}
          onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => { if ((e.ctrlKey || e.metaKey) && e.key === "Enter") run(); }} />
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-xs text-muted">Read-only: one SELECT, WITH, SHOW, EXPLAIN or DESCRIBE at a time, up to 500 rows. Ctrl+Enter runs it.</span>
          <Button size="sm" disabled={busy || !q.trim()} onClick={() => run()}>{busy ? <Spinner size={13} /> : <Play size={13} />} Run</Button>
        </div>
      </div>
      {err && <div className="rounded-2xl bg-crit-soft px-4 py-3 font-mono text-xs text-crit">{err}</div>}
      {res && (
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-xs text-muted">
            <Badge>{res.rows?.length ?? 0} row{res.rows?.length === 1 ? "" : "s"}</Badge>{res.tookMs} ms{res.truncated && <span className="text-warn">· only the first 500 rows</span>}
          </div>
          {(res.columns?.length ?? 0) > 0 && (
            <div className="max-h-[55vh] overflow-auto rounded-xl border border-line">
              <table className="w-full text-xs">
                <thead className="sticky top-0 bg-surface2 text-left"><tr>{res.columns!.map((c, i) => <th key={i} className="whitespace-nowrap px-3 py-2 font-medium">{c}</th>)}</tr></thead>
                <tbody className="divide-y divide-line font-mono">
                  {res.rows!.map((r, i) => (
                    <tr key={i} className="hover:bg-surface2/50">
                      {r.map((v, j) => <td key={j} className="max-w-[24rem] truncate px-3 py-1.5" title={v ?? "NULL"}>{v === null ? <span className="italic text-muted">NULL</span> : v}</td>)}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function NewDatabase({ serverId, engines, onClose, onDone }: { serverId: string; engines: Engine[]; onClose: () => void; onDone: () => void }) {
  const { toast } = useStore();
  const [engine, setEngine] = useState<Engine>(engines[0]);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [made, setMade] = useState<{ database: string; user: string; password: string; url: string } | null>(null);
  const create = async () => {
    setBusy(true);
    try {
      setMade(await api("POST", `/api/servers/${serverId}/db/create`, { engine, name }));
      onDone();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <Modal open onClose={onClose} title={made ? "Database created" : "New database"}
      footer={made ? <Button onClick={onClose}>Done</Button> : <><Button variant="ghost" onClick={onClose}>Cancel</Button><Button disabled={busy || !name} onClick={create}>{busy && <Spinner size={14} />} Create</Button></>}>
      {made ? (
        <div className="space-y-3 text-sm">
          <p className="text-warn">Copy the password now: it is not shown again.</p>
          {([["Database", made.database], ["User", made.user], ["Password", made.password], ["Connection URL", made.url]] as const).map(([k, v]) => (
            <Field key={k} label={k}><div className="flex items-center gap-2 rounded-xl bg-surface2 px-3 py-2"><code className="flex-1 break-all font-mono text-xs">{v}</code><CopyButton text={v} /></div></Field>
          ))}
          <p className="text-xs text-muted">The user can do anything in this database and nothing elsewhere. It signs in from this server (localhost); an app in Docker reaches it at the host's address.</p>
        </div>
      ) : (
        <div className="space-y-4">
          <Field label="Kind">
            <Select value={engine} onChange={(e) => setEngine(e.target.value as Engine)}>
              {engines.map((e) => <option key={e} value={e}>{ENGINE[e]}</option>)}
            </Select>
          </Field>
          <Field label="Name" hint="The database and its user get this name; a strong password is made for it.">
            <Input value={name} className="font-mono" placeholder="shop" onChange={(e) => setName(e.target.value.toLowerCase().replace(/[^a-z0-9_]/g, ""))} />
          </Field>
        </div>
      )}
    </Modal>
  );
}
