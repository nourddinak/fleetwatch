import { useEffect, useState } from "react";
import { Eraser, RefreshCw } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Bar, Button, Card, ConfirmDialog, SectionTitle, Spinner } from "./ui";
import { relTime } from "../mock/util";

interface Item {
  id: string;
  label: string;
  note: string;
  bytes: number;
  files: number;
}
interface DiskState {
  report: { at: number; items: Item[]; top: { path: string; bytes: number }[]; durationMs: number } | null;
  scanning: boolean;
  cleaning?: string;
  last?: { action: string; freed: number; error?: string };
}

// The journal keeps up to 500 MB, so only the rest counts as freeable.
const freeable = (i: Item) => (i.id === "journal" ? Math.max(0, i.bytes - 500 * 1024 * 1024) : i.bytes);

export function fmtBytes(b: number) {
  if (b < 1024) return `${b} B`;
  const u = ["KB", "MB", "GB", "TB"];
  let v = b / 1024;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`;
}

/** What fills a server's disk, and the safe cleanups the agent offers. */
export function DiskRescue({ serverId, disk }: { serverId: string; disk: number }) {
  const { subscribeLive, toast } = useStore();
  const st = useApi<DiskState>(`/api/servers/${serverId}/disk`);
  const [ask, setAsk] = useState<Item | null>(null);
  const { setData } = st;
  useEffect(
    () =>
      subscribeLive((m) => {
        const d = m.data as { serverId?: string; state?: DiskState };
        if (m.type === "disk" && d.serverId === serverId && d.state) {
          setData(d.state);
        }
      }),
    [subscribeLive, serverId, setData],
  );
  const call = async (path: string, body?: unknown) => {
    try {
      setData(await api<DiskState>("POST", `/api/servers/${serverId}/disk/${path}`, body));
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
  };
  const s = st.data;
  const rep = s?.report;
  const busy = !!s && (s.scanning || !!s.cleaning);
  const cleanable = rep?.items.filter((i) => i.bytes > 0) ?? [];
  const total = cleanable.reduce((n, i) => n + freeable(i), 0);
  const top = rep?.top[0]?.bytes ?? 1;

  return (
    <Card className="lg:col-span-5">
      <SectionTitle icon={<Eraser size={15} />}
        title="Free up space"
        sub={rep ? `Checked ${relTime(rep.at)}${total ? ` · about ${fmtBytes(total)} can be freed safely` : ""}` : "See what fills this disk and clean the safe parts with one click."}
        action={
          <Button size="sm" variant="secondary" disabled={busy || !s} onClick={() => call("scan")}>
            {s?.scanning ? <Spinner size={14} /> : <RefreshCw size={14} />} {s?.scanning ? "Checking…" : rep ? "Check again" : "Check now"}
          </Button>
        }
      />
      {disk >= 90 && !rep && !s?.scanning && <p className="mb-3 rounded-2xl bg-crit-soft px-4 py-3 text-sm text-crit">This disk is {disk.toFixed(0)}% full. Check what fills it.</p>}
      {s?.last && !s.cleaning && (
        <p className={`mb-3 rounded-2xl px-4 py-3 text-sm ${s.last.error ? "bg-warn-soft text-warn" : "bg-ok-soft text-ok"}`}>
          {s.last.error ? `Cleanup finished with a problem: ${s.last.error}` : `Freed ${fmtBytes(s.last.freed)}.`}
        </p>
      )}
      {!rep ? (
        s?.scanning ? <p className="flex items-center gap-2 text-sm text-muted"><Spinner size={14} /> Measuring… this can take up to a minute on big disks.</p> : null
      ) : (
        <div className="grid gap-6 lg:grid-cols-2">
          <div>
            <div className="mb-2 text-xs font-medium uppercase tracking-wide text-muted">Safe to clean</div>
            <ul className="divide-y divide-line">
              {rep.items.map((i) => (
                <li key={i.id} className="flex items-center gap-3 py-3">
                  <div className="min-w-0 flex-1">
                    <div className="text-sm font-medium">{i.label}</div>
                    <div className="text-xs text-muted">{i.note}</div>
                  </div>
                  <span className="shrink-0 text-sm tabular-nums">{i.bytes ? fmtBytes(i.bytes) : <span className="text-muted">—</span>}</span>
                  <Button size="sm" variant="secondary" disabled={busy || i.bytes === 0} onClick={() => setAsk(i)}>
                    {s?.cleaning === i.id ? <Spinner size={14} /> : <Eraser size={14} />} Clean
                  </Button>
                </li>
              ))}
            </ul>
          </div>
          <div>
            <div className="mb-2 text-xs font-medium uppercase tracking-wide text-muted">Biggest folders</div>
            <ul className="space-y-3">
              {rep.top.map((d) => (
                <li key={d.path}>
                  <div className="mb-1 flex justify-between gap-3 text-sm">
                    <span className="min-w-0 truncate font-mono text-xs">{d.path}</span>
                    <span className="shrink-0 tabular-nums text-muted">{fmtBytes(d.bytes)}</span>
                  </div>
                  <Bar value={(d.bytes / top) * 100} tone="accent" />
                </li>
              ))}
            </ul>
            <p className="mt-3 text-xs text-muted">For information only: Fleetwatch never deletes these. Look inside before removing anything by hand.</p>
          </div>
        </div>
      )}
      <ConfirmDialog
        open={!!ask}
        title={`Clean ${ask?.label.toLowerCase()}?`}
        message={<>{ask?.note} Up to {ask ? fmtBytes(ask.id === "journal" ? ask.bytes : freeable(ask)) : ""} will be freed.</>}
        confirmLabel="Clean"
        onConfirm={() => ask && (call("clean", { action: ask.id }), setAsk(null))}
        onCancel={() => setAsk(null)}
      />
    </Card>
  );
}

/** /api/forecasts: trends from the stored metrics. */
export interface Forecasts {
  servers: Record<string, { now: number; perDay: number; fullDays: number }>;
  apps: Record<string, { fromMb: number; nowMb: number; pctPerDay: number; since: number }>;
  at: number;
}

export const fullIn = (days: number) => (days < 1 ? `${Math.max(1, Math.round(days * 24))} hours` : days < 2 ? "1 day" : `${Math.round(days)} days`);
