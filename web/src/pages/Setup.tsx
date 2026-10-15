import { useState } from "react";
import { Link, Navigate } from "react-router-dom";
import { Bell, Check, KeyRound, Server, ShieldCheck, Sparkles } from "lucide-react";
import { useStore } from "../store/store";
import { api, type ApiError } from "../api/client";
import { Button, CopyButton, Field, Input, Spinner } from "../components/ui";
import { Shell } from "./Login";
import { cn } from "../utils/cn";

const LOG_CMD = "journalctl -u ops-server | grep 'setup code'";
const STEPS = ["Setup code", "Admin account", "Done"];

/** First-run wizard: shown while the server has no admin account. */
export function Setup() {
  const { auth, refreshMe } = useStore();
  const [step, setStep] = useState(0);
  const [code, setCode] = useState("");
  const [username, setUsername] = useState("admin");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  if (step < 2 && auth === "in") return <Navigate to="/" replace />;
  if (step < 2 && (auth === "out" || auth === "needs2fa")) return <Navigate to="/login" replace />;

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setErr("");
    if (pw !== pw2) return setErr("The passwords do not match.");
    setBusy(true);
    try {
      await api("POST", "/api/setup", { code, username, password: pw });
      setStep(2);
      await refreshMe();
    } catch (ex) {
      const e2 = ex as ApiError;
      setErr(e2.message);
      if (e2.code === "bad_setup_code") setStep(0);
    }
    setBusy(false);
  };

  return (
    <Shell>
      <ol className="mb-6 flex items-center gap-2 text-xs" aria-label="Setup progress">
        {STEPS.map((label, i) => (
          <li key={label} className="flex items-center gap-2" aria-current={i === step ? "step" : undefined}>
            <span className={cn("flex h-6 w-6 items-center justify-center rounded-full font-semibold", i < step ? "bg-ok text-white dark:text-black" : i === step ? "bg-fg text-bg" : "bg-surface2 text-muted")}>
              {i < step ? <Check size={12} /> : i + 1}
            </span>
            <span className={i === step ? "font-medium" : "text-muted"}>{label}</span>
            {i < STEPS.length - 1 && <span className="h-px w-4 bg-line" />}
          </li>
        ))}
      </ol>

      {step === 0 && (
        <form onSubmit={(e) => { e.preventDefault(); setErr(""); setStep(1); }} className="space-y-4">
          <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface2"><Sparkles size={20} /></div>
          <h1 className="text-2xl font-semibold tracking-tight">Welcome to Fleetwatch</h1>
          <p className="text-sm text-muted">This server has no admin yet. To prove you installed it, enter the one-time setup code from the server log:</p>
          <div className="flex items-center gap-2 rounded-2xl bg-surface2 px-4 py-3">
            <code className="flex-1 break-all font-mono text-xs">{LOG_CMD}</code>
            <CopyButton text={LOG_CMD} />
          </div>
          <Field label="Setup code">
            <Input value={code} onChange={(e) => setCode(e.target.value.toUpperCase())} placeholder="XXXX-XXXX-XXXX-XXXX" autoComplete="off" spellCheck={false} className="text-center font-mono tracking-widest" autoFocus />
          </Field>
          {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>}
          <Button type="submit" className="w-full" disabled={code.replace(/[^A-Za-z0-9]/g, "").length !== 16}>Continue</Button>
        </form>
      )}

      {step === 1 && (
        <form onSubmit={create} className="space-y-4">
          <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface2"><KeyRound size={20} /></div>
          <h1 className="text-2xl font-semibold tracking-tight">Create the admin account</h1>
          <p className="text-sm text-muted">This is the only account. You can turn on two-factor authentication right after.</p>
          <Field label="Username">
            <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
          </Field>
          <Field label="Password" hint="At least 12 characters">
            <Input type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete="new-password" autoFocus />
          </Field>
          <Field label="Repeat password">
            <Input type="password" value={pw2} onChange={(e) => setPw2(e.target.value)} autoComplete="new-password" />
          </Field>
          {err && <div role="alert" className="rounded-2xl bg-crit-soft px-4 py-2.5 text-sm text-crit">{err}</div>}
          <div className="flex gap-2">
            <Button type="button" variant="secondary" onClick={() => { setErr(""); setStep(0); }}>Back</Button>
            <Button type="submit" className="flex-1" disabled={busy || !username.trim() || !pw}>{busy && <Spinner />} Create account</Button>
          </div>
        </form>
      )}

      {step === 2 && (
        <div className="space-y-4">
          <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-ok-soft text-ok"><Check size={20} /></div>
          <h1 className="text-2xl font-semibold tracking-tight">You're all set</h1>
          <p className="text-sm text-muted">You are signed in as <b className="text-fg">{username}</b>. Recommended next steps:</p>
          <div className="space-y-2">
            {[
              { to: "/settings/2fa", icon: <ShieldCheck size={16} />, title: "Turn on two-factor authentication", sub: "Protects the dashboard that controls your servers" },
              { to: "/servers/new", icon: <Server size={16} />, title: "Add your first server", sub: "One command on the VPS installs the agent" },
              { to: "/settings/notifications", icon: <Bell size={16} />, title: "Set up alerts", sub: "ntfy, Telegram or Discord" },
            ].map((x) => (
              <Link key={x.to} to={x.to} className="flex items-center gap-3 rounded-2xl border border-line px-4 py-3 transition hover:border-fg/30">
                <span className="text-muted">{x.icon}</span>
                <span><span className="block text-sm font-medium">{x.title}</span><span className="block text-xs text-muted">{x.sub}</span></span>
              </Link>
            ))}
          </div>
          <Link to="/"><Button className="mt-2 w-full">Go to the dashboard</Button></Link>
        </div>
      )}
    </Shell>
  );
}
