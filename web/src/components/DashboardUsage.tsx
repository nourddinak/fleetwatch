import { Cpu, Eraser, Gauge, HardDrive, MemoryStick, RefreshCw, Timer } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { relTime } from "../mock/util";
import { fmtBytes } from "./DiskRescue";
import { Button, Card, ErrorState, IconButton, LoadingState, SectionTitle, Segmented, Spinner } from "./ui";

interface Item {
  name: string;
  bytes: number;
}
interface Usage {
  storage: { total: number; files: Item[]; tables: Item[]; free: number };
  memory: { rss: number; heap: number; sys: number };
  cpu: number;
  cores: number;
  goroutines: number;
  started: number;
  go: string;
}

/** What Fleetwatch itself takes on its server: disk, memory, CPU. */
export function DashboardUsage() {
  const u = useApi<Usage>("/api/system/usage");
  if (u.error) return <ErrorState message={u.error} onRetry={u.reload} />;
  if (!u.data) return <Card><LoadingState /></Card>;
  const d = u.data;
  return (
    <Card>
      <SectionTitle icon={<Gauge size={15} />} title="This dashboard's usage" sub="What Fleetwatch itself takes on the server it runs on."
        action={<IconButton aria-label="Refresh" title="Refresh" onClick={u.reload}><RefreshCw size={14} /></IconButton>} />
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Tile icon={<HardDrive size={14} />} label="Storage" value={fmtBytes(d.storage.total)} sub="data folder" />
        <Tile icon={<MemoryStick size={14} />} label="Memory" value={fmtBytes(d.memory.rss || d.memory.sys)} sub={`${fmtBytes(d.memory.heap)} in use by the app`} />
        <Tile icon={<Cpu size={14} />} label="CPU" value={`${d.cpu.toFixed(1)}%`} sub={`of one core (${d.cores} cores)`} />
        <Tile icon={<Timer size={14} />} label="Started" value={relTime(d.started)} sub={`${d.goroutines} tasks · ${d.go}`} />
      </div>
      <div className="mt-5 grid gap-5 lg:grid-cols-2">
        <Bars title="What takes the space" items={d.storage.files} />
        <Bars title="Database, by table" items={d.storage.tables}
          note={d.storage.free > 1 << 20 ? `${fmtBytes(d.storage.free)} of the database is empty space it reuses for new data.` : undefined} />
      </div>
      <Cleanup onDone={u.reload} />
    </Card>
  );
}

function Tile({ icon, label, value, sub }: { icon: ReactNode; label: string; value: string; sub: string }) {
  return (
    <div className="rounded-2xl border border-line p-3">
      <div className="flex items-center gap-1.5 text-xs text-muted">{icon}{label}</div>
      <div className="mt-1 text-lg font-semibold tabular-nums">{value}</div>
      <div className="truncate text-xs text-muted">{sub}</div>
    </div>
  );
}

function Bars({ title, items, note }: { title: string; items: Item[]; note?: string }) {
  const max = Math.max(1, ...items.map((i) => i.bytes));
  return (
    <div>
      <h4 className="mb-2 text-sm font-medium">{title}</h4>
      {items.length === 0 && <p className="text-xs text-muted">Nothing to show.</p>}
      <ul className="space-y-2">
        {items.map((i) => (
          <li key={i.name} className="text-xs" title={`${i.name}: ${fmtBytes(i.bytes)}`}>
            <div className="mb-1 flex justify-between gap-2"><span className="truncate font-mono">{i.name}</span><span className="shrink-0 tabular-nums text-muted">{fmtBytes(i.bytes)}</span></div>
            <div className="h-1.5 rounded-full bg-surface2"><div className="h-1.5 rounded-full bg-accent" style={{ width: `${Math.max(2, (i.bytes / max) * 100)}%` }} /></div>
          </li>
        ))}
      </ul>
      {note && <p className="mt-2 text-xs text-muted">{note}</p>}
    </div>
  );
}

/** Deletes old history (metrics, logs, check results…) and gives the space back. */
function Cleanup({ onDone }: { onDone: () => void }) {
  const { toast } = useStore();
  const [days, setDays] = useState<"7" | "30" | "90">("30");
  const [sure, setSure] = useState(false);
  const [busy, setBusy] = useState(false);
  const run = async () => {
    setBusy(true);
    try {
      const r = await api<{ deleted: number; before: number; after: number }>("POST", "/api/system/cleanup", { days: Number(days) });
      toast(`Cleaned up: ${r.deleted.toLocaleString()} old rows deleted, ${fmtBytes(Math.max(0, r.before - r.after))} freed`);
      setSure(false);
      onDone();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(false);
  };
  return (
    <div className="mt-5 space-y-3 border-t border-line pt-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="w-full">
          <div className="flex items-center gap-1.5 text-sm font-medium"><Eraser size={14} /> Clean up</div>
          <p className="text-xs text-muted">Deletes metrics, logs, uptime and health results, events, sent notifications and SSH logins older than you choose, then shrinks the database. The audit log, incidents and call history are kept.</p>
        </div>
        <Segmented value={days} onChange={setDays} options={[{ value: "7", label: "Keep 7 days" }, { value: "30", label: "30 days" }, { value: "90", label: "90 days" }]} />
        {!sure ? (
          <Button size="sm" variant="secondary" onClick={() => setSure(true)}>Clean up…</Button>
        ) : (
          <Button size="sm" variant="danger" disabled={busy} onClick={run}>{busy ? <Spinner size={13} /> : <Eraser size={13} />} Delete older than {days} days</Button>
        )}
      </div>
      {sure && <p className="text-xs text-warn">Charts and logs before then will be gone for good. It can take a minute on a big database.</p>}
    </div>
  );
}
