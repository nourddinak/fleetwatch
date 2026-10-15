import { useEffect, useState } from "react";
import { BellRing, Smartphone, Trash2 } from "lucide-react";
import { api, useApi, type ApiError } from "../api/client";
import { useStore } from "../store/store";
import { Button, Card, SectionTitle, Spinner, Toggle } from "./ui";
import { relTime } from "../mock/util";

interface Device {
  id: string;
  device: string;
  createdAt: number;
  lastOkAt: number;
  lastError: string;
}

/** applicationServerKey from the base64url VAPID public key. */
function keyBytes(b64url: string) {
  const b64 = (b64url + "===".slice((b64url.length + 3) % 4)).replace(/-/g, "+").replace(/_/g, "/");
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

function deviceName(ua: string) {
  const os = /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Mac OS/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "Device";
  const br = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  return `${br} on ${os}`;
}

const supported = typeof window !== "undefined" && window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
const isIOS = typeof navigator !== "undefined" && (/iPhone|iPad|iPod/.test(navigator.userAgent) || (/Macintosh/.test(navigator.userAgent) && navigator.maxTouchPoints > 1));
// Push is set up from phones and tablets only; computers just see and manage the devices.
const isPhone = typeof navigator !== "undefined" && (isIOS || /Android|Mobile/i.test(navigator.userAgent));

/** Settings → Notifications: push notifications to this browser / installed app. */
export function PushCard({ enabled, onChange }: { enabled: boolean; onChange: () => void }) {
  const { toast } = useStore();
  const devices = useApi<Device[]>("/api/push/devices");
  const [mine, setMine] = useState<PushSubscription | null>(null);
  const [busy, setBusy] = useState("");
  const [res, setRes] = useState<{ ok: boolean; msg: string } | null>(null);

  useEffect(() => {
    if (!supported) return;
    navigator.serviceWorker.getRegistration("/").then((reg) => reg?.pushManager.getSubscription().then(setMine));
  }, []);

  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what);
    try {
      await fn();
    } catch (e) {
      toast((e as ApiError).message ?? String(e), "err");
    }
    setBusy("");
  };
  const turnOn = () =>
    run("on", async () => {
      const reg = await navigator.serviceWorker.register("/sw.js", { scope: "/" });
      await navigator.serviceWorker.ready;
      if ((await Notification.requestPermission()) !== "granted") {
        throw new Error("Notifications are blocked for this site. Allow them in the browser's site settings, then try again.");
      }
      const { publicKey } = await api<{ publicKey: string }>("GET", "/api/push/key");
      const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(publicKey) });
      await api("POST", "/api/push/subscribe", sub.toJSON());
      if (!enabled) await api("PUT", "/api/notifications/channels/push", { enabled: true, fields: {} });
      setMine(sub);
      devices.reload();
      onChange();
      toast("Push notifications are on for this device");
    });
  const turnOff = () =>
    run("off", async () => {
      if (mine) {
        await api("POST", "/api/push/unsubscribe", { endpoint: mine.endpoint });
        await mine.unsubscribe();
      }
      setMine(null);
      devices.reload();
      onChange();
    });
  const test = () =>
    run("test", async () => {
      const h = await api<{ result: string; error?: string }>("POST", "/api/notifications/channels/push/test");
      setRes(h.result === "delivered" ? { ok: true, msg: "Sent. It should appear in a few seconds." } : { ok: false, msg: h.error ?? "Failed" });
    });
  const setEnabled = (v: boolean) =>
    run("toggle", async () => {
      await api("PUT", "/api/notifications/channels/push", { enabled: v, fields: {} });
      onChange();
    });
  const remove = (d: Device) =>
    run("rm" + d.id, async () => {
      await api("DELETE", `/api/push/devices/${d.id}`);
      if (mine && d.id) navigator.serviceWorker.getRegistration("/").then((reg) => reg?.pushManager.getSubscription().then(setMine));
      devices.reload();
      onChange();
    });

  const list = devices.data ?? [];
  return (
    <Card>
      <SectionTitle
        title="Push notifications"
        sub="Alerts straight to your phone from this dashboard: no ntfy, Telegram or Discord needed. Turn it on from each phone that should get them."
        action={<Toggle checked={enabled} disabled={list.length === 0 || !!busy} onChange={setEnabled} />}
      />
      {!isPhone ? (
        <p className="rounded-2xl bg-surface2 px-4 py-3 text-sm text-muted">
          Open this page on your phone to turn push notifications on there.{isIOS ? "" : " On iPhone, add the dashboard to the home screen first."}
        </p>
      ) : !supported ? (
        <p className="rounded-2xl bg-surface2 px-4 py-3 text-sm text-muted">
          {isIOS
            ? "On iPhone and iPad, push works in the installed app: tap Share → Add to Home Screen, open Fleetwatch from the home screen, then turn it on here (iOS 16.4 or later)."
            : !window.isSecureContext
              ? "Push needs HTTPS: open the dashboard on its https:// domain."
              : "This browser does not support push notifications."}
        </p>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          {mine ? (
            <>
              <Button size="sm" variant="secondary" disabled={!!busy} onClick={test}>{busy === "test" ? <Spinner size={13} /> : <BellRing size={13} />} Send test</Button>
              <Button size="sm" variant="ghost" disabled={!!busy} onClick={turnOff}>{busy === "off" && <Spinner size={13} />} Turn off on this device</Button>
            </>
          ) : (
            <Button size="sm" disabled={!!busy} onClick={turnOn}>{busy === "on" ? <Spinner size={13} /> : <Smartphone size={13} />} Turn on for this device</Button>
          )}
          {res && <span className={res.ok ? "text-xs text-ok" : "text-xs text-crit"}>{res.ok ? "✓" : "✕"} {res.msg}</span>}
        </div>
      )}
      {list.length > 0 && (
        <ul className="mt-4 divide-y divide-line rounded-2xl border border-line">
          {list.map((d) => (
            <li key={d.id} className="flex items-center justify-between gap-3 px-4 py-2.5 text-sm">
              <span>
                <span className="font-medium">{deviceName(d.device)}</span>
                <span className="block text-xs text-muted">
                  {d.lastError ? <span className="text-crit">Last attempt failed: {d.lastError}</span> : d.lastOkAt ? `last delivered ${relTime(d.lastOkAt)}` : `added ${relTime(d.createdAt)}`}
                </span>
              </span>
              <Button size="sm" variant="ghost" disabled={!!busy} onClick={() => remove(d)} aria-label="Remove device"><Trash2 size={13} /></Button>
            </li>
          ))}
        </ul>
      )}
      {isPhone && !isIOS && supported && <p className="mt-3 text-xs text-muted">Tip: add the dashboard to your home screen (browser menu → Install app) so alerts open it like an app.</p>}
    </Card>
  );
}
