import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { EyeOff, FolderSearch, Inbox, Plus, Radar, RefreshCw, RotateCcw, Undo2 } from "lucide-react";
import { useStore } from "../store/store";
import { api, useApi, type ApiError } from "../api/client";
import type { App, Discovery } from "../mock/types";
import { relTime } from "../mock/util";
import { Badge, Button, Card, ConfirmDialog, EmptyState, Gate, PageHeader, Segmented, Spinner } from "../components/ui";
import { DiscoveryCard, InspectModal } from "../components/domain";

export function Discoveries() {
  const { discoveries, reloadDiscoveries, getServer, upsertApp, toast, servers } = useStore();
  const nav = useNavigate();
  const [tab, setTab] = useState<"pending" | "ignored">("pending");
  const [inspect, setInspect] = useState<Discovery | null>(null);
  const [scanning, setScanning] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [resetOpen, setResetOpen] = useState(false);
  const ignored = useApi<Discovery[]>("/api/discoveries?state=ignored");
  const roots = useApi<{ roots: string[] }>("/api/settings/discovery");

  const act = async (id: string, fn: () => Promise<void>) => {
    setBusy(id);
    try {
      await fn();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy(null);
  };
  const add = (d: Discovery) =>
    act(d.id, async () => {
      const app = await api<App>("POST", `/api/discoveries/${d.id}/add`);
      upsertApp(app);
      setInspect(null);
      reloadDiscoveries();
      ignored.reload();
      toast(`${d.name} added to monitored applications`);
      nav(`/applications/${app.id}`);
    });
  const ignore = (d: Discovery) =>
    act(d.id, async () => {
      await api("POST", `/api/discoveries/${d.id}/ignore`);
      reloadDiscoveries();
      ignored.reload();
      toast(`${d.name} ignored. It stays hidden until its process or folder changes.`, "info");
    });
  const restore = (d: Discovery) =>
    act(d.id, async () => {
      await api("POST", `/api/discoveries/${d.id}/restore`);
      reloadDiscoveries();
      ignored.reload();
      toast(`${d.name} restored to pending`);
    });
  const resetAll = () => {
    setResetOpen(false);
    act("reset", async () => {
      const r = await api<{ restored: number }>("POST", "/api/discoveries/reset-ignored");
      reloadDiscoveries();
      ignored.reload();
      toast(`${r.restored} ignored discover${r.restored === 1 ? "y" : "ies"} restored`);
    });
  };
  const scan = async () => {
    setScanning(true);
    try {
      const r = await api<{ sent: number }>("POST", "/api/discoveries/scan");
      toast(r.sent ? `Scan requested on ${r.sent} server${r.sent === 1 ? "" : "s"}. Results appear in a few seconds.` : "No agent is connected.", r.sent ? "ok" : "err");
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setTimeout(() => setScanning(false), 3000);
  };

  const ignoredList = ignored.data ?? [];
  return (
    <>
      <PageHeader
        title="Discoveries"
        sub="Agents scan PM2, systemd and project folders for applications you are not monitoring yet"
        actions={
          <Button variant="secondary" onClick={scan} disabled={scanning || servers.length === 0}>
            {scanning ? <Spinner size={14} /> : <RefreshCw size={14} />} {scanning ? "Scanning…" : "Scan now"}
          </Button>
        }
      />
      <p className="-mt-4 mb-6 text-sm text-muted">
        <FolderSearch size={14} className="mr-1.5 inline-block align-[-2px]" />Folder scan roots: <span className="font-mono">{roots.data?.roots.join(", ") || "—"}</span> · <Link to="/settings/system" className="underline-offset-2 hover:underline">change</Link>. Agents rescan every 30 seconds.
      </p>
      <Gate>
        <div className="mb-5 flex flex-wrap items-center gap-3">
          <Segmented value={tab} onChange={setTab} options={[{ value: "pending", label: <span className="inline-flex items-center gap-1.5"><Inbox size={13} />Pending ({discoveries.length})</span> }, { value: "ignored", label: <span className="inline-flex items-center gap-1.5"><EyeOff size={13} />Ignored ({ignoredList.length})</span> }]} />
          {tab === "ignored" && ignoredList.length > 0 && (
            <Button size="sm" variant="ghost" onClick={() => setResetOpen(true)} disabled={busy === "reset"}><RotateCcw size={13} /> Restore all ignored</Button>
          )}
        </div>
        {tab === "pending" ? (
          discoveries.length === 0 ? (
            <EmptyState icon={<Radar size={20} />} title="Nothing waiting for review" desc={servers.length ? "New PM2 processes, systemd services and project folders are detected automatically." : "Add a server first; its agent reports what runs on it."} action={servers.length ? <Button variant="secondary" onClick={scan}>Scan now</Button> : <Button onClick={() => nav("/servers/new")}>Add server</Button>} />
          ) : (
            <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
              {discoveries.map((d) => (
                <DiscoveryCard key={d.id} d={d} busy={busy === d.id} onAdd={() => add(d)} onIgnore={() => ignore(d)} onInspect={() => setInspect(d)} />
              ))}
            </div>
          )
        ) : ignoredList.length === 0 ? (
          <EmptyState icon={<EyeOff size={20} />} title="No ignored discoveries" desc="Items you ignore are kept here so you can restore them later." />
        ) : (
          <div className="grid gap-3 md:grid-cols-2">
            {ignoredList.map((d) => (
              <Card key={d.id} className="flex flex-wrap items-center gap-3">
                <span className="grid h-10 w-10 shrink-0 place-items-center rounded-xl border border-dashed border-line text-muted"><EyeOff size={16} /></span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2"><span className="font-semibold">{d.name}</span><Badge>Ignored</Badge></div>
                  <div className="truncate text-xs text-muted">{getServer(d.serverId)?.name} · {d.kind} · <span className="font-mono">{d.path}</span> · first seen {relTime(d.discoveredAt)}</div>
                </div>
                <Button size="sm" variant="secondary" disabled={busy === d.id} onClick={() => restore(d)}><Undo2 size={13} /> Restore</Button>
                <Button size="sm" disabled={busy === d.id} onClick={() => add(d)}><Plus size={13} /> Add</Button>
              </Card>
            ))}
          </div>
        )}
        <InspectModal d={inspect} onClose={() => setInspect(null)} onAdd={() => inspect && add(inspect)} />
        <ConfirmDialog open={resetOpen} title="Restore all ignored discoveries?" message="They move back to Pending. Anything no longer running disappears on the next scan." confirmLabel="Restore all" onCancel={() => setResetOpen(false)} onConfirm={resetAll} />
        <div className="mt-8"><Button variant="ghost" size="sm" onClick={() => nav("/applications")}>View monitored applications →</Button></div>
      </Gate>
    </>
  );
}
