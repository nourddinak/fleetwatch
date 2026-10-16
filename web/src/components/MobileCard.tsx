import { useEffect, useMemo, useState } from "react";
import qrcode from "qrcode-generator";
import { Send, Smartphone, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Badge, Button, Card, CopyButton, IconButton, SectionTitle, Select, Spinner } from "./ui";
import { relTime } from "../mock/util";

interface Phone {
  id: string;
  name: string;
  platform: string;
  appVersion: string;
  alerts: "all" | "oncall";
  createdAt: number;
  lastSeenAt: number;
  connected: boolean;
}
interface Pairing {
  code: string;
  expiresAt: number;
  url: string;
  qr: string;
}

function QR({ text }: { text: string }) {
  const src = useMemo(() => {
    const q = qrcode(0, "M");
    q.addData(text);
    q.make();
    return q.createDataURL(6, 2);
  }, [text]);
  return <img src={src} alt="QR code to connect the Fleetwatch Notifications app" className="h-56 w-56 rounded-xl bg-white p-2 [image-rendering:pixelated]" />;
}

/** Settings → Mobile notifications: the phone app, pairing by QR code or by hand, and the
 *  signed-in person's own phones. Every person (owner or team member) pairs their own. */
export function MobileCard() {
  const { toast } = useStore();
  const phones = useApi<Phone[]>("/api/phones");
  const [pair, setPair] = useState<Pairing | null>(null);
  const [now, setNow] = useState(Date.now());
  const [busy, setBusy] = useState("");
  const count = phones.data?.length ?? 0;
  const { reload } = phones;

  // While a code is shown: count down, and notice when the phone has connected.
  useEffect(() => {
    if (!pair) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    const p = setInterval(reload, 3000);
    return () => (clearInterval(t), clearInterval(p));
  }, [pair, reload]);
  const [seen, setSeen] = useState(count);
  useEffect(() => {
    if (pair && count > seen) {
      const newest = [...(phones.data ?? [])].sort((a, b) => b.createdAt - a.createdAt)[0];
      toast(`${newest?.name ?? "Your phone"} is connected`);
      setPair(null);
    }
    setSeen(count);
  }, [count, pair, phones.data, seen, toast]);

  const run = async (what: string, f: () => Promise<unknown>) => {
    setBusy(what);
    try {
      await f();
    } catch (e) {
      toast((e as ApiError).message, "err");
    }
    setBusy("");
  };
  const newCode = () => run("pair", async () => setPair(await api<Pairing>("POST", "/api/phones/pairing")));
  const left = pair ? Math.max(0, Math.round((pair.expiresAt - now) / 1000)) : 0;

  return (
    <Card>
      <SectionTitle
        title="Mobile notifications"
        sub="The Fleetwatch Notifications app rings your phone for alerts and on-call pages, even with the browser closed. It connects straight to this dashboard: no other account or service in between."
      />
      <div className="grid gap-6 lg:grid-cols-2">
        <div className="space-y-3">
          <div className="text-sm font-medium">1. Get the app</div>
          <p className="text-sm text-muted">Install <b className="text-fg">Fleetwatch Notifications</b> on your Android phone (Google Play: coming soon). iPhone: later.</p>
          <div className="pt-2 text-sm font-medium">2. Connect your phone</div>
          {!pair ? (
            <Button size="sm" disabled={busy === "pair"} onClick={newCode}>{busy === "pair" ? <Spinner size={14} /> : <Smartphone size={14} />} Connect a phone</Button>
          ) : left === 0 ? (
            <div className="space-y-2">
              <p className="text-sm text-muted">The code expired.</p>
              <Button size="sm" onClick={newCode}>Make a new code</Button>
            </div>
          ) : (
            <p className="text-xs text-muted">In the app, scan the code, or type the address and the code. It works once and expires in {Math.floor(left / 60)}:{String(left % 60).padStart(2, "0")}.</p>
          )}
        </div>
        {pair && left > 0 && (
          <div className="flex flex-col items-center gap-3 sm:flex-row sm:items-start">
            <QR text={pair.qr} />
            <div className="space-y-3 text-sm">
              <div>
                <div className="text-xs text-muted">Dashboard address</div>
                <div className="flex items-center gap-1 font-mono text-xs">{pair.url}<CopyButton text={pair.url} /></div>
              </div>
              <div>
                <div className="text-xs text-muted">Pairing code</div>
                <div className="flex items-center gap-1 font-mono text-2xl font-semibold tracking-widest">{pair.code}<CopyButton text={pair.code} /></div>
              </div>
              <p className="flex items-center gap-2 text-xs text-muted"><Spinner size={12} /> Waiting for the phone…</p>
            </div>
          </div>
        )}
      </div>

      {count > 0 && (
        <ul className="mt-6 divide-y divide-line rounded-2xl border border-line">
          {phones.data!.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
              <Smartphone size={16} className="text-muted" />
              <div className="min-w-0 flex-1">
                <div className="font-medium">{p.name}</div>
                <div className="text-xs text-muted">
                  {p.connected ? <span className="text-ok">● connected</span> : p.lastSeenAt ? `last seen ${relTime(p.lastSeenAt)}` : "not connected yet"}
                  {p.appVersion ? ` · app ${p.appVersion}` : ""}
                </div>
              </div>
              <Select
                className="w-auto py-1.5 text-xs"
                value={p.alerts}
                onChange={(e) => run("a" + p.id, async () => (await api("PATCH", `/api/phones/${p.id}`, { alerts: e.target.value }), reload()))}
              >
                <option value="all">All alerts for my servers</option>
                <option value="oncall">Only on-call pages</option>
              </Select>
              {!p.connected && p.lastSeenAt > 0 && Date.now() - p.lastSeenAt > 3600_000 && <Badge tone="warn">offline</Badge>}
              <IconButton aria-label="Send a test" title="Send a test" disabled={!!busy} onClick={() => run("t" + p.id, async () => (await api("POST", `/api/phones/${p.id}/test`), toast("Test sent")))}>
                <Send size={14} />
              </IconButton>
              <IconButton aria-label="Remove phone" disabled={!!busy} onClick={() => run("r" + p.id, async () => (await api("DELETE", `/api/phones/${p.id}`), reload()))}>
                <Trash2 size={14} />
              </IconButton>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
