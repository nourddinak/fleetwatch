import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Bell, BellDot, Boxes, Check, CheckCircle2, Clock, Send, Server as ServerIcon, XCircle } from "lucide-react";
import { useStore } from "../store/store";
import { cn } from "../utils/cn";
import type { NotificationRec } from "../mock/types";
import { dateTime, relTime } from "../mock/util";
import { Badge, Button, Card, EmptyState, FilterBar, Gate, KV, MetricCard, Modal, PageHeader, SearchInput, Select, Table, Td, Tr } from "../components/ui";
import { providerLabel } from "../components/domain";

export function Notifications() {
  const { notifications, getApp, getServer, markAllRead, markRead, toast } = useStore();
  const [q, setQ] = useState("");
  const [provider, setProvider] = useState("all");
  const [event, setEvent] = useState("all");
  const [result, setResult] = useState("all");
  const [sel, setSel] = useState<NotificationRec | null>(null);

  const list = useMemo(() => {
    const s = q.trim().toLowerCase();
    return notifications.filter((n) => (provider === "all" || n.provider === provider) && (event === "all" || n.event === event) && (result === "all" || n.result === result) && (!s || [n.title, n.message, n.event, getApp(n.appId)?.name ?? "", getServer(n.serverId)?.name ?? ""].join(" ").toLowerCase().includes(s)));
  }, [notifications, q, provider, event, result, getApp, getServer]);
  const failed = notifications.filter((n) => n.result === "failed").length;
  const events = useMemo(() => [...new Set(notifications.map((n) => n.event))].sort(), [notifications]);

  const open = (n: NotificationRec) => { setSel(n); markRead(n.id); };

  return (
    <>
      <PageHeader title="Notifications" sub="Delivery history for ntfy, Telegram, Discord and webhooks" actions={<Button variant="secondary" size="sm" onClick={() => { markAllRead(); toast("All marked as read"); }}><Check size={14} /> Mark all read</Button>} />
      <Gate>
        <div className="mb-6 grid grid-cols-2 gap-3 md:grid-cols-4">
          <MetricCard icon={<Send size={14} />} label="Total sent" value={notifications.length} />
          <MetricCard icon={<CheckCircle2 size={14} />} label="Delivered" value={notifications.length - failed} tone="ok" sub={`${Math.round(((notifications.length - failed) / Math.max(1, notifications.length)) * 100)}% success`} />
          <MetricCard icon={<XCircle size={14} />} label="Failed" value={failed} tone={failed ? "crit" : "ok"} />
          <MetricCard icon={<BellDot size={14} />} label="Unread" value={notifications.filter((n) => !n.read).length} />
        </div>
        <FilterBar>
          <SearchInput value={q} onChange={setQ} placeholder="Search notifications…" className="lg:w-64" />
          <Select value={provider} onChange={(e) => setProvider(e.target.value)} className="lg:!w-auto">
            <option value="all">All providers</option><option value="ntfy">ntfy</option><option value="telegram">Telegram</option><option value="discord">Discord</option><option value="webhook">Webhook</option><option value="push">Push</option>
          </Select>
          <Select value={event} onChange={(e) => setEvent(e.target.value)} className="lg:!w-auto">
            <option value="all">All events</option>
            {events.map((e) => <option key={e}>{e}</option>)}
          </Select>
          <Select value={result} onChange={(e) => setResult(e.target.value)} className="lg:!w-auto">
            <option value="all">Any result</option><option value="delivered">Delivered</option><option value="failed">Failed</option>
          </Select>
          <span className="text-xs text-muted lg:ml-auto">{list.length} notifications</span>
        </FilterBar>
        {list.length === 0 ? (
          <EmptyState icon={<Bell size={20} />} title="No notifications" desc="Configure providers and rules in Settings → Notifications." action={<Link to="/settings/notifications"><Button variant="secondary">Notification settings</Button></Link>} />
        ) : (
          <Card pad={false}>
            <Table head={["Time", "Event", "Server", "Application", "Provider", "Result"]}>
              {list.slice(0, 80).map((n) => (
                <Tr key={n.id} onClick={() => open(n)}>
                  <Td className="whitespace-nowrap text-xs"><div className="flex items-center gap-1.5"><Clock size={12} className="text-muted" />{dateTime(n.ts)}</div><div className="pl-[18px] text-muted">{relTime(n.ts)}</div></Td>
                  <Td>
                    <div className="flex items-center gap-3">
                      <span className={cn("relative grid h-8 w-8 shrink-0 place-items-center rounded-xl", n.result === "failed" ? "bg-crit-soft text-crit" : "bg-surface2 text-muted")}>
                        <Bell size={14} />
                        {!n.read && <span className="absolute -right-0.5 -top-0.5 h-2.5 w-2.5 rounded-full border-2 border-surface bg-crit" />}
                      </span>
                      <div className="min-w-0"><div className="font-medium">{n.event}</div><div className="max-w-[300px] truncate text-xs text-muted">{n.title}</div></div>
                    </div>
                  </Td>
                  <Td className="whitespace-nowrap text-xs">{n.serverId ? <span className="inline-flex items-center gap-1.5"><ServerIcon size={12} className="text-muted" />{getServer(n.serverId)?.name ?? "—"}</span> : "—"}</Td>
                  <Td className="whitespace-nowrap text-xs">{n.appId ? <span className="inline-flex items-center gap-1.5"><Boxes size={12} className="text-muted" />{getApp(n.appId)?.name ?? "—"}</span> : "—"}</Td>
                  <Td><Badge tone="info"><Send size={10} />{providerLabel[n.provider]}</Badge></Td>
                  <Td><Result r={n.result} /></Td>
                </Tr>
              ))}
            </Table>
          </Card>
        )}
        <Modal open={!!sel} onClose={() => setSel(null)} title={<span className="flex items-center gap-2"><Bell size={16} /> Notification details</span>} footer={<Button onClick={() => setSel(null)}>Close</Button>}>
          {sel && (
            <div className="space-y-4">
              <div className="rounded-2xl bg-surface2 p-4 text-sm"><div className="font-medium">{sel.title}</div><div className="whitespace-pre-wrap text-muted">{sel.message}</div></div>
              <dl className="grid grid-cols-2 gap-x-6">
                <KV label="Event">{sel.event}</KV>
                <KV label="Result"><Result r={sel.result} /></KV>
                <KV label="Provider">{providerLabel[sel.provider]}</KV>
                <KV label="Delivery latency">{sel.latencyMs} ms</KV>
                <KV label="Sent">{dateTime(sel.ts)}</KV>
                <KV label="Server">{sel.serverId ? <Link to={`/servers/${sel.serverId}`} className="hover:underline">{getServer(sel.serverId)?.name ?? sel.serverId}</Link> : "—"}</KV>
                <KV label="Application">{sel.appId ? <Link to={`/applications/${sel.appId}`} className="hover:underline">{getApp(sel.appId)?.name}</Link> : "—"}</KV>
              </dl>
              {sel.error && <div className="flex gap-2 rounded-2xl bg-crit-soft px-4 py-3 text-sm text-crit"><XCircle size={16} className="mt-0.5 shrink-0" /><span><b>Error:</b> {sel.error}</span></div>}
            </div>
          )}
        </Modal>
      </Gate>
    </>
  );
}

const Result = ({ r }: { r: NotificationRec["result"] }) => <Badge tone={r === "delivered" ? "ok" : "crit"} dot>{r === "delivered" ? "Delivered" : "Failed"}</Badge>;
