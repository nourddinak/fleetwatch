import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ArrowUpCircle, ExternalLink, X } from "lucide-react";
import { api, type ApiError } from "../api/client";
import { Button, CopyButton, Modal, Spinner } from "./ui";

export interface UpdateStatus {
  current: string;
  latest: string;
  available: boolean;
  url: string;
  checkedAt: number;
  checkError?: string;
  disabled: boolean;
  canApply: boolean;
  applying: boolean;
  lastResult?: { ts: number; version: string; ok: boolean; error?: string };
}

const INSTALL_CMD = "curl -fsSL https://raw.githubusercontent.com/nourddinak/fleetwatch/main/scripts/install-server.sh | sudo bash -s -- --domain <your-domain>";
const DISMISS_KEY = "fw.update.dismissed";

/** Installs the latest release: asks the server, waits for it to come back on the new version, reloads. */
export function UpdateDialog({ st, open, onClose }: { st: UpdateStatus; open: boolean; onClose: () => void }) {
  const [phase, setPhase] = useState<"confirm" | "installing" | "failed">("confirm");
  const [err, setErr] = useState("");

  useEffect(() => {
    if (open) {
      setPhase("confirm");
      setErr("");
    }
  }, [open]);

  useEffect(() => {
    if (phase !== "installing") return;
    const started = Date.now();
    let stop = false;
    const poll = async () => {
      while (!stop && Date.now() - started < 5 * 60_000) {
        await new Promise((r) => setTimeout(r, 2000));
        try {
          const h = await api<{ version: string }>("GET", "/api/health");
          if (h.version !== st.current) return location.reload();
          // Still the old version: if the helper has finished and failed, say so.
          const s = await api<UpdateStatus>("GET", "/api/system/update");
          if (!s.applying && s.lastResult && !s.lastResult.ok && s.lastResult.ts > started) {
            setErr(s.lastResult.error ?? "The update failed.");
            return setPhase("failed");
          }
        } catch {
          /* the server is restarting */
        }
      }
      if (!stop) {
        setErr("The server did not come back on the new version within 5 minutes. Check: journalctl -u ops-server-update -u ops-server");
        setPhase("failed");
      }
    };
    poll();
    return () => {
      stop = true;
    };
  }, [phase, st.current]);

  const start = async () => {
    setErr("");
    try {
      await api("POST", "/api/system/update/apply", { version: st.latest });
      setPhase("installing");
    } catch (e) {
      setErr((e as ApiError).message);
      setPhase("failed");
    }
  };

  const installing = phase === "installing";
  return (
    <Modal
      open={open}
      onClose={installing ? () => {} : onClose}
      title={`Update Fleetwatch to ${st.latest}`}
      footer={
        installing ? null : !st.canApply ? (
          <Button onClick={onClose}>Close</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose}>Cancel</Button>
            <Button onClick={start}>{phase === "failed" ? "Try again" : "Update now"}</Button>
          </>
        )
      }
    >
      <div className="space-y-4 text-sm">
        {!st.canApply ? (
          <>
            <p>One-click updates are not set up on this server yet. Run the install command on the server once (it upgrades in place and keeps your data); after that, updates are one click.</p>
            <div className="flex items-center gap-2 rounded-2xl bg-surface2 px-4 py-3">
              <code className="flex-1 break-all font-mono text-xs">{INSTALL_CMD}</code>
              <CopyButton text={INSTALL_CMD} />
            </div>
          </>
        ) : installing ? (
          <div className="flex items-center gap-3">
            <Spinner />
            <span>Downloading {st.latest}, checking its SHA-256 and restarting the server. The page reloads by itself, usually within a minute.</span>
          </div>
        ) : (
          <>
            <p>You run <b>v{st.current}</b>. The server downloads <b>{st.latest}</b> from the official release, verifies it against the release's SHA256SUMS, replaces itself and restarts. The dashboard is unavailable for a few seconds; agents reconnect on their own.</p>
            <p className="text-muted">After that, update the agents from Settings → Agents.</p>
            {st.url && <a href={st.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 underline underline-offset-2">What's new in {st.latest} <ExternalLink size={12} /></a>}
          </>
        )}
        {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-crit">{err}</div>}
      </div>
    </Modal>
  );
}

/** Banner shown on every page while a newer release exists (dismissible per version). */
export function UpdateBanner() {
  const [st, setSt] = useState<UpdateStatus | null>(null);
  const [open, setOpen] = useState(false);
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(DISMISS_KEY) ?? "";
    } catch {
      return "";
    }
  });
  useEffect(() => {
    const load = () => api<UpdateStatus>("GET", "/api/system/update").then(setSt).catch(() => {});
    load();
    const t = setInterval(load, 30 * 60_000);
    return () => clearInterval(t);
  }, []);
  if (!st?.available || dismissed === st.latest) return null;
  const dismiss = () => {
    setDismissed(st.latest);
    try {
      localStorage.setItem(DISMISS_KEY, st.latest);
    } catch {
      /* private mode */
    }
  };
  return (
    <>
      <div className="mb-6 flex flex-wrap items-center gap-3 rounded-2xl border border-accent/40 bg-surface px-4 py-3 text-sm">
        <ArrowUpCircle size={18} className="shrink-0" />
        <span className="min-w-0 flex-1">
          <b>Fleetwatch {st.latest}</b> is available. You run v{st.current}.{" "}
          {st.url && <a href={st.url} target="_blank" rel="noreferrer" className="underline underline-offset-2">What's new</a>}
        </span>
        <Button size="sm" onClick={() => setOpen(true)}>Update now</Button>
        <button onClick={dismiss} className="text-muted hover:text-fg" aria-label="Hide until the next version"><X size={16} /></button>
      </div>
      <UpdateDialog st={st} open={open} onClose={() => setOpen(false)} />
    </>
  );
}

/** Settings → System card: version, check button and update action. */
export function UpdateCard() {
  const [st, setSt] = useState<UpdateStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    api<UpdateStatus>("GET", "/api/system/update").then(setSt).catch(() => {});
  }, []);
  const check = async () => {
    setBusy(true);
    try {
      setSt(await api<UpdateStatus>("POST", "/api/system/update/check"));
    } catch {
      /* shown via checkError */
    }
    setBusy(false);
  };
  if (!st) return null;
  return (
    <div className="space-y-3 text-sm">
      <div className="flex flex-wrap items-center gap-3">
        <span>Running <b>v{st.current}</b></span>
        {st.disabled ? (
          <span className="text-muted">Update checks are off (OPS_UPDATE_SOURCE=off).</span>
        ) : st.available ? (
          <span className="text-warn"><b>{st.latest}</b> is available</span>
        ) : st.latest ? (
          <span className="text-ok">Up to date</span>
        ) : null}
        {!st.disabled && (
          <span className="ml-auto flex gap-2">
            <Button size="sm" variant="secondary" disabled={busy} onClick={check}>{busy && <Spinner size={13} />} Check for updates</Button>
            {st.available && <Button size="sm" onClick={() => setOpen(true)}>Update now</Button>}
          </span>
        )}
      </div>
      {st.checkError && <p className="text-crit">{st.checkError}</p>}
      {st.checkedAt > 0 && <p className="text-xs text-muted">Last checked {new Date(st.checkedAt).toLocaleString()}. Checks run every 6 hours.</p>}
      {st.lastResult && !st.lastResult.ok && <p className="text-crit">Last update to {st.lastResult.version} failed: {st.lastResult.error}</p>}
      {st.available && <UpdateDialog st={st} open={open} onClose={() => setOpen(false)} />}
      <p className="text-xs text-muted">Agents: see <Link to="/settings/agents" className="underline underline-offset-2">Settings → Agents</Link>.</p>
    </div>
  );
}
